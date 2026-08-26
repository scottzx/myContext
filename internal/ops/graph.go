package ops

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// AddDependencyInput is the payload of `dep.add`. Both ends name their type,
// so an edge may cross levels.
type AddDependencyInput struct {
	FromType       string `json:"from_type"`
	FromID         string `json:"from_id"`
	ToType         string `json:"to_type"`
	ToID           string `json:"to_id"`
	DependencyType string `json:"dependency_type,omitempty"`
	LagDays        int    `json:"lag_days,omitempty"`
	Note           string `json:"note,omitempty"`
}

// AddDependency records one edge. `blocks` and `requires` are hard edges and
// feed v_blocked; `supports` is a weak contribution edge that states influence
// without gating anything; `related` is a bare cross-reference.
func (s *Store) AddDependency(ctx context.Context, wc WriteContext, in AddDependencyInput) (*Result, error) {
	if in.FromID == "" || in.ToID == "" {
		return nil, protocol.BadInput("from_id and to_id are required")
	}
	if in.FromType == "" {
		in.FromType = "task"
	}
	if in.ToType == "" {
		in.ToType = "task"
	}
	if !validEntityType[in.FromType] || !validEntityType[in.ToType] {
		return nil, protocol.BadInput("from_type and to_type must be one of %s", EntityTypeList())
	}
	if in.DependencyType == "" {
		in.DependencyType = "blocks"
	}
	if !validDependency[in.DependencyType] {
		return nil, protocol.BadInput("dependency_type must be blocks|requires|related|supports")
	}
	if in.FromType == in.ToType && in.FromID == in.ToID {
		return nil, protocol.BadInput("a node cannot depend on itself")
	}
	if in.LagDays < 0 {
		return nil, protocol.BadInput("lag_days cannot be negative")
	}
	return s.execute(ctx, "dep.add", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, entityTables[in.FromType], in.FromID, in.FromType); err != nil {
			return nil, err
		}
		if err := requireExists(ctx, tx, entityTables[in.ToType], in.ToID, in.ToType); err != nil {
			return nil, err
		}
		// A hard edge that closes a loop would make "what unblocks what"
		// unanswerable, so refuse it rather than store it.
		if in.DependencyType == "blocks" || in.DependencyType == "requires" {
			cyclic, err := reachable(ctx, tx, in.ToType, in.ToID, in.FromType, in.FromID)
			if err != nil {
				return nil, err
			}
			if cyclic {
				return nil, protocol.BadInput("that edge would create a dependency cycle")
			}
		}
		id := system.NewID("dep")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO dependencies (id, from_type, from_id, to_type, to_id,
                                      dependency_type, lag_days, note, created_at)
            VALUES (?,?,?,?,?,?,?,?,?)
            ON CONFLICT(from_type, from_id, to_type, to_id, dependency_type)
            DO UPDATE SET note = excluded.note, lag_days = excluded.lag_days`,
			id, in.FromType, in.FromID, in.ToType, in.ToID, in.DependencyType,
			nullInt(in.LagDays), nullString(in.Note), ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, in.ToType, in.ToID, "linked", nil, in); err != nil {
			return nil, err
		}
		return &Result{
			Data: Dependency{ID: id, FromType: in.FromType, FromID: in.FromID,
				ToType: in.ToType, ToID: in.ToID, DependencyType: in.DependencyType,
				CreatedAt: ts},
			Changes: []protocol.Change{{EntityType: in.ToType, EntityID: in.ToID,
				EventType: "linked", ProjectionKeys: []string{"dependencies"}}},
		}, nil
	})
}

// reachable reports whether a hard edge path already runs from one node to
// another, walking `blocks`/`requires` edges only.
func reachable(ctx context.Context, tx *sql.Tx, fromType, fromID, toType, toID string) (bool, error) {
	var hit int
	err := tx.QueryRowContext(ctx, `
        WITH RECURSIVE walk(t, i) AS (
            SELECT ?, ?
            UNION
            SELECT d.to_type, d.to_id
              FROM dependencies d JOIN walk w
                ON d.from_type = w.t AND d.from_id = w.i
             WHERE d.dependency_type IN ('blocks','requires')
        )
        SELECT 1 FROM walk WHERE t = ? AND i = ? LIMIT 1`,
		fromType, fromID, toType, toID).Scan(&hit)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return hit == 1, err
}

// DependencyFilter selects edges by either endpoint.
type DependencyFilter struct {
	EntityType string
	EntityID   string
	Direction  string // outgoing | incoming | both
	Type       string
}

// ListDependencies returns edges touching a node, in both directions by
// default - "what am I waiting on" and "who is waiting on me" are the same
// question asked from two sides.
func (s *Store) ListDependencies(ctx context.Context, f DependencyFilter) ([]Dependency, error) {
	query := `
        SELECT id, from_type, from_id, to_type, to_id, dependency_type, lag_days, note, created_at
          FROM dependencies WHERE 1=1`
	var args []any
	if f.EntityID != "" {
		entityType := f.EntityType
		if entityType == "" {
			entityType = "task"
		}
		switch f.Direction {
		case "outgoing":
			query += " AND from_type = ? AND from_id = ?"
			args = append(args, entityType, f.EntityID)
		case "incoming":
			query += " AND to_type = ? AND to_id = ?"
			args = append(args, entityType, f.EntityID)
		default:
			query += " AND ((from_type = ? AND from_id = ?) OR (to_type = ? AND to_id = ?))"
			args = append(args, entityType, f.EntityID, entityType, f.EntityID)
		}
	}
	if f.Type != "" {
		query += " AND dependency_type = ?"
		args = append(args, f.Type)
	}
	query += " ORDER BY created_at"

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []Dependency{}
	for rows.Next() {
		var d Dependency
		if err := rows.Scan(&d.ID, &d.FromType, &d.FromID, &d.ToType, &d.ToID,
			&d.DependencyType, &d.LagDays, &d.Note, &d.CreatedAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, d)
	}
	return out, sqlite.Classify(rows.Err())
}

// SetTagsInput is the payload of `tag.set`.
type SetTagsInput struct {
	EntityType string   `json:"entity_type"`
	EntityID   string   `json:"entity_id"`
	Tags       []string `json:"tags"`
	Replace    bool     `json:"replace,omitempty"`
}

// SetTags attaches tags to a node. By default it adds; --replace makes the
// given set authoritative.
func (s *Store) SetTags(ctx context.Context, wc WriteContext, in SetTagsInput) (*Result, error) {
	if in.EntityID == "" {
		return nil, protocol.BadInput("entity_id is required")
	}
	if in.EntityType == "" {
		in.EntityType = "task"
	}
	if !validEntityType[in.EntityType] {
		return nil, protocol.BadInput("entity_type must be one of %s", EntityTypeList())
	}
	tags := NormalizeTags(in.Tags)
	if len(tags) == 0 && !in.Replace {
		return nil, protocol.BadInput("no tags to set")
	}
	return s.execute(ctx, "tag.set", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, entityTables[in.EntityType], in.EntityID, in.EntityType); err != nil {
			return nil, err
		}
		if in.Replace {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM tags WHERE entity_type = ? AND entity_id = ?`,
				in.EntityType, in.EntityID); err != nil {
				return nil, err
			}
		}
		ts := system.FormatTimestamp(now)
		for _, tag := range tags {
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO tags (entity_type, entity_id, tag, created_at) VALUES (?,?,?,?)
                ON CONFLICT(entity_type, entity_id, tag) DO NOTHING`,
				in.EntityType, in.EntityID, tag, ts); err != nil {
				return nil, err
			}
		}
		if err := recordEvent(ctx, tx, wc, now, in.EntityType, in.EntityID, "linked", nil, in); err != nil {
			return nil, err
		}
		return &Result{
			Data: map[string]any{"entity_type": in.EntityType, "entity_id": in.EntityID, "tags": tags},
			Changes: []protocol.Change{{EntityType: in.EntityType, EntityID: in.EntityID,
				EventType: "linked", ProjectionKeys: []string{"tags"}}},
		}, nil
	})
}

// ListTags returns the tag vocabulary with its usage count, or the tags on
// one entity when an id is given.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

func (s *Store) ListTags(ctx context.Context, entityType, entityID string) ([]TagCount, error) {
	query := `SELECT tag, COUNT(*) FROM tags WHERE 1=1`
	var args []any
	if entityID != "" {
		if entityType == "" {
			entityType = "task"
		}
		query += " AND entity_type = ? AND entity_id = ?"
		args = append(args, entityType, entityID)
	} else if entityType != "" {
		query += " AND entity_type = ?"
		args = append(args, entityType)
	}
	query += " GROUP BY tag ORDER BY COUNT(*) DESC, tag"

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []TagCount{}
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Count); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, t)
	}
	return out, sqlite.Classify(rows.Err())
}

// NormalizeTags splits, trims and de-duplicates a tag list. Both delimiters
// the legacy tree uses ("|" and ",") are accepted, because both appear in the
// same database.
func NormalizeTags(raw []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, chunk := range raw {
		for _, part := range strings.FieldsFunc(chunk, func(r rune) bool {
			return r == '|' || r == ',' || r == '，' || r == '｜'
		}) {
			tag := strings.TrimSpace(part)
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}
