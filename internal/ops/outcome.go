package ops

import (
	"context"
	"database/sql"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// CreateObjectiveInput is the payload of `objective.create`.
type CreateObjectiveInput struct {
	AreaID      string `json:"area_id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Horizon     string `json:"horizon,omitempty"`
}

// CreateObjective adds a direction with a horizon. It never becomes a task
// tree parent: work hangs off initiatives, and the link back to an objective
// runs through key results.
func (s *Store) CreateObjective(ctx context.Context, wc WriteContext, in CreateObjectiveInput) (*Result, error) {
	if in.Name == "" {
		return nil, protocol.BadInput("name is required")
	}
	return s.execute(ctx, "objective.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if in.AreaID != "" {
			if err := requireExists(ctx, tx, "areas", in.AreaID, "area"); err != nil {
				return nil, err
			}
		}
		id := system.NewID("obj")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO objectives (id, area_id, name, description, horizon, status,
                                    version, created_at, updated_at)
            VALUES (?,?,?,?,?,'active',1,?,?)`,
			id, nullString(in.AreaID), in.Name, nullString(in.Description),
			nullString(in.Horizon), ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "objective", id, "created", nil, in); err != nil {
			return nil, err
		}
		obj := Objective{ID: id, Name: in.Name, Status: "active", Version: 1, CreatedAt: ts, UpdatedAt: ts}
		assignOptional(&obj.AreaID, in.AreaID)
		assignOptional(&obj.Description, in.Description)
		assignOptional(&obj.Horizon, in.Horizon)
		return &Result{
			Data: obj,
			Changes: []protocol.Change{{EntityType: "objective", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"objectives"}}},
		}, nil
	})
}

// CreateKeyResultInput is the payload of `kr.create`. metric_name is required
// because a key result without a measurement is just a second objective.
type CreateKeyResultInput struct {
	ObjectiveID  string   `json:"objective_id"`
	Name         string   `json:"name"`
	MetricName   string   `json:"metric_name"`
	MetricUnit   string   `json:"metric_unit,omitempty"`
	TargetValue  *float64 `json:"target_value,omitempty"`
	CurrentValue *float64 `json:"current_value,omitempty"`
	Weight       *float64 `json:"weight,omitempty"`
	Horizon      string   `json:"horizon,omitempty"`
}

// CreateKeyResult adds one measurement under an objective.
func (s *Store) CreateKeyResult(ctx context.Context, wc WriteContext, in CreateKeyResultInput) (*Result, error) {
	if in.ObjectiveID == "" || in.Name == "" {
		return nil, protocol.BadInput("objective_id and name are required")
	}
	if in.MetricName == "" {
		return nil, protocol.BadInput("metric_name is required: a key result must be measurable")
	}
	if in.Weight != nil && (*in.Weight < 0 || *in.Weight > 1) {
		return nil, protocol.BadInput("weight must be between 0 and 1")
	}
	return s.execute(ctx, "kr.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, "objectives", in.ObjectiveID, "objective"); err != nil {
			return nil, err
		}
		id := system.NewID("kr")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO key_results (id, objective_id, name, metric_name, metric_unit,
                                     target_value, current_value, weight, horizon, status,
                                     version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,'active',1,?,?)`,
			id, in.ObjectiveID, in.Name, in.MetricName, nullString(in.MetricUnit),
			nullFloat(in.TargetValue), nullFloat(in.CurrentValue), nullFloat(in.Weight),
			nullString(in.Horizon), ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "key_result", id, "created", nil, in); err != nil {
			return nil, err
		}
		kr := KeyResult{ID: id, ObjectiveID: in.ObjectiveID, Name: in.Name,
			MetricName: in.MetricName, TargetValue: in.TargetValue, CurrentValue: in.CurrentValue,
			Weight: in.Weight, Status: "active", Version: 1, CreatedAt: ts, UpdatedAt: ts}
		assignOptional(&kr.MetricUnit, in.MetricUnit)
		assignOptional(&kr.Horizon, in.Horizon)
		return &Result{
			Data: kr,
			Changes: []protocol.Change{{EntityType: "key_result", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"objectives", "key_results"}}},
		}, nil
	})
}

// UpdateKeyResultInput patches a key result. Moving current_value is the
// common case and is recorded as its own event type so the metric history is
// readable without diffing JSON.
type UpdateKeyResultInput struct {
	KeyResultID     string   `json:"key_result_id"`
	ExpectedVersion int64    `json:"expected_version"`
	Name            *string  `json:"name,omitempty"`
	MetricName      *string  `json:"metric_name,omitempty"`
	MetricUnit      *string  `json:"metric_unit,omitempty"`
	TargetValue     *float64 `json:"target_value,omitempty"`
	CurrentValue    *float64 `json:"current_value,omitempty"`
	Weight          *float64 `json:"weight,omitempty"`
	Horizon         *string  `json:"horizon,omitempty"`
	Status          *string  `json:"status,omitempty"`
}

// UpdateKeyResult patches a key result under optimistic concurrency.
func (s *Store) UpdateKeyResult(ctx context.Context, wc WriteContext, in UpdateKeyResultInput) (*Result, error) {
	if in.KeyResultID == "" {
		return nil, protocol.BadInput("key_result_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required")
	}
	if in.Status != nil && !validOutcomeStatus[*in.Status] {
		return nil, protocol.BadInput("status must be active|done|dropped|archived")
	}
	if in.Weight != nil && (*in.Weight < 0 || *in.Weight > 1) {
		return nil, protocol.BadInput("weight must be between 0 and 1")
	}
	return s.execute(ctx, "kr.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadKeyResult(ctx, tx, in.KeyResultID)
		if err != nil {
			return nil, err
		}
		p := newPatch()
		p.str("name", in.Name)
		p.str("metric_name", in.MetricName)
		p.str("metric_unit", in.MetricUnit)
		p.flt("target_value", in.TargetValue)
		p.flt("current_value", in.CurrentValue)
		p.flt("weight", in.Weight)
		p.str("horizon", in.Horizon)
		p.str("status", in.Status)
		if err := p.applyToKeyResult(ctx, tx, in.KeyResultID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		after, err := loadKeyResult(ctx, tx, in.KeyResultID)
		if err != nil {
			return nil, err
		}
		// A metric move is the thing a review actually reads; give it a name
		// rather than burying it in a generic update.
		eventType := "updated"
		if in.CurrentValue != nil {
			eventType = "metric_updated"
		}
		if err := recordEvent(ctx, tx, wc, now, "key_result", in.KeyResultID, eventType, before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "key_result", EntityID: in.KeyResultID,
				EventType: eventType, Version: after.Version,
				ProjectionKeys: []string{"objectives", "key_results"}}},
		}, nil
	})
}

const keyResultColumns = `
    id, objective_id, name, metric_name, metric_unit, target_value, current_value,
    weight, horizon, status, version, created_at, updated_at`

func scanKeyResult(row interface{ Scan(...any) error }) (*KeyResult, error) {
	var k KeyResult
	err := row.Scan(&k.ID, &k.ObjectiveID, &k.Name, &k.MetricName, &k.MetricUnit,
		&k.TargetValue, &k.CurrentValue, &k.Weight, &k.Horizon, &k.Status,
		&k.Version, &k.CreatedAt, &k.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &k, nil
}

func loadKeyResult(ctx context.Context, tx *sql.Tx, id string) (*KeyResult, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+keyResultColumns+` FROM key_results WHERE id = ?`, id)
	k, err := scanKeyResult(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("key result %s does not exist", id)
	}
	return k, err
}

// ListObjectives returns objectives with their key results attached.
type ObjectiveTree struct {
	Objective  Objective   `json:"objective"`
	KeyResults []KeyResult `json:"key_results"`
}

func (s *Store) ListObjectives(ctx context.Context, includeArchived bool) ([]ObjectiveTree, error) {
	query := `
        SELECT id, area_id, name, description, horizon, status, version, created_at, updated_at
          FROM objectives`
	if !includeArchived {
		query += ` WHERE status NOT IN ('archived','dropped')`
	}
	query += ` ORDER BY created_at`

	rows, err := s.db.SQL().QueryContext(ctx, query)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []ObjectiveTree{}
	for rows.Next() {
		var o Objective
		if err := rows.Scan(&o.ID, &o.AreaID, &o.Name, &o.Description, &o.Horizon,
			&o.Status, &o.Version, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, ObjectiveTree{Objective: o, KeyResults: []KeyResult{}})
	}
	if err := rows.Err(); err != nil {
		return nil, sqlite.Classify(err)
	}
	for i := range out {
		krs, err := s.listKeyResults(ctx, out[i].Objective.ID)
		if err != nil {
			return nil, err
		}
		out[i].KeyResults = krs
	}
	return out, nil
}

func (s *Store) listKeyResults(ctx context.Context, objectiveID string) ([]KeyResult, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT `+keyResultColumns+` FROM key_results WHERE objective_id = ? ORDER BY created_at`,
		objectiveID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []KeyResult{}
	for rows.Next() {
		k, err := scanKeyResult(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, *k)
	}
	return out, sqlite.Classify(rows.Err())
}

func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// assignOptional points a nullable field at value, unless value is empty.
func assignOptional(dst **string, value string) {
	if value == "" {
		return
	}
	v := value
	*dst = &v
}
