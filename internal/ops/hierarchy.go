package ops

import (
	"context"
	"database/sql"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// CreateAreaInput is the payload of `area.create`.
type CreateAreaInput struct {
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order,omitempty"`
	Note      string `json:"note,omitempty"`
}

// CreateArea adds a long-lived domain of work (cash flow, product, market...).
func (s *Store) CreateArea(ctx context.Context, wc WriteContext, in CreateAreaInput) (*Result, error) {
	if in.Name == "" {
		return nil, protocol.BadInput("name is required")
	}
	return s.execute(ctx, "area.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		id := system.NewID("area")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO areas (id, name, status, sort_order, note, version, created_at, updated_at)
            VALUES (?,?,'active',?,?,1,?,?)`,
			id, in.Name, in.SortOrder, nullString(in.Note), ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "area", id, "created", nil, in); err != nil {
			return nil, err
		}
		return &Result{
			Data: Area{ID: id, Name: in.Name, Status: "active", SortOrder: in.SortOrder,
				Version: 1, CreatedAt: ts, UpdatedAt: ts},
			Changes: []protocol.Change{{EntityType: "area", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"areas"}}},
		}, nil
	})
}

// CreateInitiativeInput is the payload of `initiative.create`.
type CreateInitiativeInput struct {
	AreaID      string `json:"area_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	StartDate   string `json:"start_date,omitempty"`
	ReviewDate  string `json:"review_date,omitempty"`
	SortOrder   int    `json:"sort_order,omitempty"`
}

// CreateInitiative adds a direction inside an area. Unlike a project, an
// initiative is not required to have an end condition.
func (s *Store) CreateInitiative(ctx context.Context, wc WriteContext, in CreateInitiativeInput) (*Result, error) {
	if in.AreaID == "" || in.Name == "" {
		return nil, protocol.BadInput("area_id and name are required")
	}
	for field, value := range map[string]string{"start_date": in.StartDate, "review_date": in.ReviewDate} {
		if value != "" {
			if err := ValidateDate(field, value); err != nil {
				return nil, err
			}
		}
	}
	return s.execute(ctx, "initiative.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, "areas", in.AreaID, "area"); err != nil {
			return nil, err
		}
		id := system.NewID("init")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO initiatives (id, area_id, name, status, start_date, review_date,
                                     description, sort_order, version, created_at, updated_at)
            VALUES (?,?,?,'active',?,?,?,?,1,?,?)`,
			id, in.AreaID, in.Name, nullString(in.StartDate), nullString(in.ReviewDate),
			nullString(in.Description), in.SortOrder, ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "initiative", id, "created", nil, in); err != nil {
			return nil, err
		}
		return &Result{
			Data: Initiative{ID: id, AreaID: in.AreaID, Name: in.Name, Status: "active",
				SortOrder: in.SortOrder, Version: 1, CreatedAt: ts, UpdatedAt: ts},
			Changes: []protocol.Change{{EntityType: "initiative", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"initiatives"}}},
		}, nil
	})
}

// TreeArea is one branch of the Area > Initiative > Project navigation.
type TreeArea struct {
	Area        Area             `json:"area"`
	Initiatives []TreeInitiative `json:"initiatives"`
}

type TreeInitiative struct {
	Initiative Initiative        `json:"initiative"`
	Projects   []*ProjectSummary `json:"projects"`
}

// Tree returns the whole hierarchy, which is what the project navigator and
// the "explain my structure in one glance" criterion need.
func (s *Store) Tree(ctx context.Context, includeArchived bool) ([]TreeArea, error) {
	areaFilter := " WHERE status = 'active'"
	if includeArchived {
		areaFilter = ""
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT id, name, status, sort_order, note, version, created_at, updated_at
          FROM areas`+areaFilter+` ORDER BY sort_order, name`)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	areas := []TreeArea{}
	for rows.Next() {
		var a Area
		if err := rows.Scan(&a.ID, &a.Name, &a.Status, &a.SortOrder, &a.Note,
			&a.Version, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		areas = append(areas, TreeArea{Area: a, Initiatives: []TreeInitiative{}})
	}
	if err := rows.Err(); err != nil {
		return nil, sqlite.Classify(err)
	}

	for i := range areas {
		initiatives, err := s.listInitiatives(ctx, areas[i].Area.ID, includeArchived)
		if err != nil {
			return nil, err
		}
		for _, init := range initiatives {
			projects, err := s.ListProjects(ctx, ProjectFilter{InitiativeID: init.ID})
			if err != nil {
				return nil, err
			}
			areas[i].Initiatives = append(areas[i].Initiatives,
				TreeInitiative{Initiative: init, Projects: projects})
		}
	}
	return areas, nil
}

func (s *Store) listInitiatives(ctx context.Context, areaID string, includeArchived bool) ([]Initiative, error) {
	query := `
        SELECT id, area_id, name, status, start_date, review_date, description,
               sort_order, version, created_at, updated_at
          FROM initiatives WHERE area_id = ?`
	if !includeArchived {
		query += " AND status <> 'archived'"
	}
	query += " ORDER BY sort_order, name"

	rows, err := s.db.SQL().QueryContext(ctx, query, areaID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []Initiative{}
	for rows.Next() {
		var i Initiative
		if err := rows.Scan(&i.ID, &i.AreaID, &i.Name, &i.Status, &i.StartDate, &i.ReviewDate,
			&i.Description, &i.SortOrder, &i.Version, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, i)
	}
	return out, sqlite.Classify(rows.Err())
}

// SetCapacityInput declares how many minutes a day actually has.
type SetCapacityInput struct {
	Date             string `json:"date"`
	AvailableMinutes int    `json:"available_minutes"`
	Note             string `json:"note,omitempty"`
}

// SetCapacity records user-declared capacity. The system never infers it.
func (s *Store) SetCapacity(ctx context.Context, wc WriteContext, in SetCapacityInput) (*Result, error) {
	if err := ValidateDate("date", in.Date); err != nil {
		return nil, err
	}
	if in.AvailableMinutes < 0 {
		return nil, protocol.BadInput("available_minutes cannot be negative")
	}
	return s.execute(ctx, "capacity.set", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO daily_capacity (date, available_minutes, note, updated_at)
            VALUES (?,?,?,?)
            ON CONFLICT(date) DO UPDATE SET
                available_minutes = excluded.available_minutes,
                note = excluded.note,
                updated_at = excluded.updated_at`,
			in.Date, in.AvailableMinutes, nullString(in.Note), system.FormatTimestamp(now)); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "capacity", in.Date, "updated", nil, in); err != nil {
			return nil, err
		}
		return &Result{
			Data: in,
			Changes: []protocol.Change{{EntityType: "capacity", EntityID: in.Date,
				EventType: "updated", ProjectionKeys: []string{"day:" + in.Date, "week"}}},
		}, nil
	})
}

// EventFilter is the query surface of `mycontext event list`.
type EventFilter struct {
	EntityType string
	EntityID   string
	EventType  string
	Since      string
	Limit      int
}

// ListEvents returns the audit trail, newest first.
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	query := `
        SELECT id, entity_type, entity_id, event_type, before_json, after_json,
               actor_type, actor_id, entry_point, reason, confirmed, request_id,
               correlation_id, occurred_at
          FROM events WHERE 1=1`
	var args []any
	if f.EntityType != "" {
		query += " AND entity_type = ?"
		args = append(args, f.EntityType)
	}
	if f.EntityID != "" {
		query += " AND entity_id = ?"
		args = append(args, f.EntityID)
	}
	if f.EventType != "" {
		query += " AND event_type = ?"
		args = append(args, f.EventType)
	}
	if f.Since != "" {
		query += " AND occurred_at >= ?"
		args = append(args, f.Since)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	query += " ORDER BY occurred_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var e Event
		var confirmed int
		if err := rows.Scan(&e.ID, &e.EntityType, &e.EntityID, &e.EventType, &e.BeforeJSON,
			&e.AfterJSON, &e.ActorType, &e.ActorID, &e.EntryPoint, &e.Reason, &confirmed,
			&e.RequestID, &e.CorrelationID, &e.OccurredAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		e.Confirmed = confirmed == 1
		out = append(out, e)
	}
	return out, sqlite.Classify(rows.Err())
}
