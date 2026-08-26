package ops

import (
	"context"
	"database/sql"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// CreateMilestoneInput is the payload of `milestone.create`.
type CreateMilestoneInput struct {
	ProjectID    string   `json:"project_id,omitempty"`
	KeyResultID  string   `json:"key_result_id,omitempty"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	TargetDate   string   `json:"target_date"`
	Status       string   `json:"status,omitempty"`
	Importance   string   `json:"importance,omitempty"`
	MetricName   string   `json:"metric_name,omitempty"`
	MetricUnit   string   `json:"metric_unit,omitempty"`
	TargetValue  *float64 `json:"target_value,omitempty"`
	CurrentValue *float64 `json:"current_value,omitempty"`
	Note         string   `json:"note,omitempty"`
	LegacyRef    string   `json:"legacy_ref,omitempty"`
	SortOrder    int      `json:"sort_order,omitempty"`
}

// CreateMilestone records a dated point that work is aiming at. The date is
// required: a checkpoint without one is a goal, and goals live in the outcome
// system, not here.
func (s *Store) CreateMilestone(ctx context.Context, wc WriteContext, in CreateMilestoneInput) (*Result, error) {
	if in.Name == "" {
		return nil, protocol.BadInput("name is required")
	}
	if err := ValidateDate("target_date", in.TargetDate); err != nil {
		return nil, err
	}
	if in.Status == "" {
		in.Status = "pending"
	}
	if !validMilestoneStatus[in.Status] {
		return nil, protocol.BadInput("status must be pending|at_risk|hit|missed|cancelled")
	}
	if in.Importance == "" {
		in.Importance = string(P2)
	}
	if !validImportance[in.Importance] {
		return nil, protocol.BadInput("importance must be P0|P1|P2|P3")
	}
	return s.execute(ctx, "milestone.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if in.ProjectID != "" {
			if err := requireExists(ctx, tx, "projects", in.ProjectID, "project"); err != nil {
				return nil, err
			}
		}
		if in.KeyResultID != "" {
			if err := requireExists(ctx, tx, "key_results", in.KeyResultID, "key result"); err != nil {
				return nil, err
			}
		}
		id := system.NewID("ms")
		ts := system.FormatTimestamp(now)
		var reachedAt any
		if in.Status == "hit" {
			reachedAt = ts
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO milestones (id, project_id, key_result_id, name, description,
                                    target_date, status, importance, metric_name, metric_unit,
                                    target_value, current_value, note, legacy_ref,
                                    sort_order, version, created_at, updated_at, reached_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?)`,
			id, nullString(in.ProjectID), nullString(in.KeyResultID), in.Name,
			nullString(in.Description), in.TargetDate, in.Status, in.Importance,
			nullString(in.MetricName), nullString(in.MetricUnit),
			nullFloat(in.TargetValue), nullFloat(in.CurrentValue),
			nullString(in.Note), nullString(in.LegacyRef), in.SortOrder,
			ts, ts, reachedAt); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "milestone", id, "created", nil, in); err != nil {
			return nil, err
		}
		m, err := loadMilestone(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: m,
			Changes: []protocol.Change{{EntityType: "milestone", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"milestones", "day:" + in.TargetDate}}},
		}, nil
	})
}

// UpdateMilestoneInput patches a milestone under optimistic concurrency.
type UpdateMilestoneInput struct {
	MilestoneID     string   `json:"milestone_id"`
	ExpectedVersion int64    `json:"expected_version"`
	Name            *string  `json:"name,omitempty"`
	Description     *string  `json:"description,omitempty"`
	ProjectID       *string  `json:"project_id,omitempty"`
	KeyResultID     *string  `json:"key_result_id,omitempty"`
	TargetDate      *string  `json:"target_date,omitempty"`
	Status          *string  `json:"status,omitempty"`
	Importance      *string  `json:"importance,omitempty"`
	MetricName      *string  `json:"metric_name,omitempty"`
	MetricUnit      *string  `json:"metric_unit,omitempty"`
	TargetValue     *float64 `json:"target_value,omitempty"`
	CurrentValue    *float64 `json:"current_value,omitempty"`
	Note            *string  `json:"note,omitempty"`
}

// UpdateMilestone patches a milestone. Moving the date is recorded as its own
// event type, because a slipped checkpoint is the thing a review looks for.
func (s *Store) UpdateMilestone(ctx context.Context, wc WriteContext, in UpdateMilestoneInput) (*Result, error) {
	if in.MilestoneID == "" {
		return nil, protocol.BadInput("milestone_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required")
	}
	if in.TargetDate != nil {
		if err := ValidateDate("target_date", *in.TargetDate); err != nil {
			return nil, err
		}
		// The same rule a task's hard deadline follows: a date that moves
		// without a stated reason is how a commitment quietly evaporates.
		if wc.Reason == "" {
			return nil, protocol.BadInput("moving a milestone date requires a reason")
		}
	}
	if in.Status != nil && !validMilestoneStatus[*in.Status] {
		return nil, protocol.BadInput("status must be pending|at_risk|hit|missed|cancelled")
	}
	if in.Importance != nil && !validImportance[*in.Importance] {
		return nil, protocol.BadInput("importance must be P0|P1|P2|P3")
	}
	return s.execute(ctx, "milestone.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadMilestone(ctx, tx, in.MilestoneID)
		if err != nil {
			return nil, err
		}
		if in.ProjectID != nil && *in.ProjectID != "" {
			if err := requireExists(ctx, tx, "projects", *in.ProjectID, "project"); err != nil {
				return nil, err
			}
		}
		if in.KeyResultID != nil && *in.KeyResultID != "" {
			if err := requireExists(ctx, tx, "key_results", *in.KeyResultID, "key result"); err != nil {
				return nil, err
			}
		}

		p := newPatch()
		p.str("name", in.Name)
		p.str("description", in.Description)
		p.str("project_id", in.ProjectID)
		p.str("key_result_id", in.KeyResultID)
		p.str("metric_name", in.MetricName)
		p.str("metric_unit", in.MetricUnit)
		p.str("note", in.Note)
		p.flt("target_value", in.TargetValue)
		p.flt("current_value", in.CurrentValue)
		if in.Importance != nil {
			p.raw("importance", *in.Importance)
		}

		eventType := "updated"
		if in.TargetDate != nil {
			p.raw("target_date", *in.TargetDate)
			eventType = "deadline_changed"
		}
		if in.Status != nil {
			p.raw("status", *in.Status)
			eventType = "status_changed"
			switch *in.Status {
			case "hit":
				if before.ReachedAt == nil {
					p.raw("reached_at", system.FormatTimestamp(now))
				}
				eventType = "completed"
			case "pending", "at_risk":
				// Reopening clears the reached mark, or the CHECK would keep
				// asserting a date that no longer means anything.
				p.raw("reached_at", nil)
			}
		} else if in.CurrentValue != nil {
			eventType = "metric_updated"
		}
		if err := p.applyToMilestone(ctx, tx, in.MilestoneID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		after, err := loadMilestone(ctx, tx, in.MilestoneID)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "milestone", in.MilestoneID, eventType, before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "milestone", EntityID: in.MilestoneID,
				EventType: eventType, Version: after.Version,
				ProjectionKeys: []string{"milestones", "day:" + after.TargetDate}}},
		}, nil
	})
}

const milestoneColumns = `
    id, project_id, key_result_id, name, description, target_date, status, importance,
    metric_name, metric_unit, target_value, current_value, note, legacy_ref,
    sort_order, version, created_at, updated_at, reached_at`

func scanMilestone(row interface{ Scan(...any) error }) (*Milestone, error) {
	var m Milestone
	err := row.Scan(&m.ID, &m.ProjectID, &m.KeyResultID, &m.Name, &m.Description,
		&m.TargetDate, &m.Status, &m.Importance, &m.MetricName, &m.MetricUnit,
		&m.TargetValue, &m.CurrentValue, &m.Note, &m.LegacyRef,
		&m.SortOrder, &m.Version, &m.CreatedAt, &m.UpdatedAt, &m.ReachedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func loadMilestone(ctx context.Context, tx *sql.Tx, id string) (*Milestone, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+milestoneColumns+` FROM milestones WHERE id = ?`, id)
	m, err := scanMilestone(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("milestone %s does not exist", id)
	}
	return m, err
}

// GetMilestone loads one milestone by id.
func (s *Store) GetMilestone(ctx context.Context, id string) (*Milestone, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+milestoneColumns+` FROM milestones WHERE id = ?`, id)
	m, err := scanMilestone(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("milestone %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return m, nil
}

// MilestoneFilter is the query surface of `mycontext milestone list`.
type MilestoneFilter struct {
	ProjectID   string
	KeyResultID string
	Status      string
	OpenOnly    bool
	Through     string // only milestones due on or before this date
	Limit       int
}

// ListMilestones returns milestones with the state of the work aimed at them,
// nearest date first.
func (s *Store) ListMilestones(ctx context.Context, f MilestoneFilter) ([]MilestoneProgress, error) {
	query := `
        SELECT milestone_id, name, status, importance, target_date, reached_at,
               metric_name, metric_unit, target_value, current_value,
               project_id, project_name, area_name, key_result_id, key_result_name,
               days_left, task_count, done_count, open_tasks, open_minutes
          FROM v_milestones WHERE 1=1`
	var args []any
	if f.ProjectID != "" {
		query += " AND project_id = ?"
		args = append(args, f.ProjectID)
	}
	if f.KeyResultID != "" {
		query += " AND key_result_id = ?"
		args = append(args, f.KeyResultID)
	}
	if f.Status != "" {
		query += " AND status = ?"
		args = append(args, f.Status)
	}
	if f.OpenOnly {
		query += " AND status NOT IN ('hit','cancelled')"
	}
	if f.Through != "" {
		query += " AND target_date <= ?"
		args = append(args, f.Through)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY target_date, importance LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []MilestoneProgress{}
	for rows.Next() {
		var m MilestoneProgress
		var days sql.NullInt64
		if err := rows.Scan(&m.MilestoneID, &m.Name, &m.Status, &m.Importance, &m.TargetDate,
			&m.ReachedAt, &m.MetricName, &m.MetricUnit, &m.TargetValue, &m.CurrentValue,
			&m.ProjectID, &m.ProjectName, &m.AreaName, &m.KeyResultID, &m.KeyResultName,
			&days, &m.TaskCount, &m.DoneCount, &m.OpenTasks, &m.OpenMinutes); err != nil {
			return nil, sqlite.Classify(err)
		}
		if days.Valid {
			d := int(days.Int64)
			m.DaysLeft = &d
		}
		out = append(out, m)
	}
	return out, sqlite.Classify(rows.Err())
}
