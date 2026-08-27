package ops

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// ---------------------------------------------------------------------------
// opportunities: a possible deal (mode A).
// ---------------------------------------------------------------------------

// CreateOpportunityInput is the payload of `opportunity.create`.
type CreateOpportunityInput struct {
	AccountID        string   `json:"account_id"`
	AreaID           string   `json:"area_id,omitempty"`
	PrimaryContactID string   `json:"primary_contact_id,omitempty"`
	Name             string   `json:"name"`
	Source           string   `json:"source,omitempty"`
	SourceBatch      string   `json:"source_batch,omitempty"`
	Stage            string   `json:"stage,omitempty"`
	EstAmount        *float64 `json:"est_amount,omitempty"`
	WinProbability   *float64 `json:"win_probability,omitempty"`
	ExpectedSignDate string   `json:"expected_sign_date,omitempty"`
	Owner            string   `json:"owner,omitempty"`
	NextStep         string   `json:"next_step,omitempty"`
	LostReason       string   `json:"lost_reason,omitempty"`
	LegacyRef        string   `json:"legacy_ref,omitempty"`
}

func (in *CreateOpportunityInput) normalize() error {
	if in.AccountID == "" {
		return protocol.BadInput("account_id is required")
	}
	if in.Name == "" {
		return protocol.BadInput("name is required")
	}
	if in.Stage == "" {
		in.Stage = "lead"
	}
	if !validOpportunityStage[in.Stage] {
		return protocol.BadInput("stage %q is not valid", in.Stage)
	}
	if in.Stage == "lost" && in.LostReason == "" {
		return protocol.BadInput("stage lost requires lost_reason")
	}
	if in.EstAmount != nil && *in.EstAmount < 0 {
		return protocol.BadInput("est_amount cannot be negative")
	}
	if in.WinProbability != nil && (*in.WinProbability < 0 || *in.WinProbability > 1) {
		return protocol.BadInput("win_probability must be between 0 and 1")
	}
	if in.ExpectedSignDate != "" {
		if err := ValidateDate("expected_sign_date", in.ExpectedSignDate); err != nil {
			return err
		}
	}
	return nil
}

// CreateOpportunity records a possible deal. Closing is a fact with a date -
// a terminal stage at creation gets closed_at set automatically, the same way
// a milestone created already-hit gets reached_at set.
func (s *Store) CreateOpportunity(ctx context.Context, wc WriteContext, in CreateOpportunityInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "opportunity.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		return createOpportunityTx(ctx, tx, wc, now, "", in)
	})
}

// UpdateOpportunityInput patches an opportunity under optimistic concurrency.
type UpdateOpportunityInput struct {
	OpportunityID    string   `json:"opportunity_id"`
	ExpectedVersion  int64    `json:"expected_version"`
	Name             *string  `json:"name,omitempty"`
	Source           *string  `json:"source,omitempty"`
	SourceBatch      *string  `json:"source_batch,omitempty"`
	PrimaryContactID *string  `json:"primary_contact_id,omitempty"`
	Stage            *string  `json:"stage,omitempty"`
	EstAmount        *float64 `json:"est_amount,omitempty"`
	WinProbability   *float64 `json:"win_probability,omitempty"`
	ExpectedSignDate *string  `json:"expected_sign_date,omitempty"`
	Owner            *string  `json:"owner,omitempty"`
	NextStep         *string  `json:"next_step,omitempty"`
	LostReason       *string  `json:"lost_reason,omitempty"`
	Note             *string  `json:"note,omitempty"`
}

// UpdateOpportunity applies a patch under optimistic concurrency control.
// Moving out of won/lost without a stated reason is refused: a closed deal
// reopening silently is exactly the kind of fact a review needs to catch.
func (s *Store) UpdateOpportunity(ctx context.Context, wc WriteContext, in UpdateOpportunityInput) (*Result, error) {
	if in.OpportunityID == "" {
		return nil, protocol.BadInput("opportunity_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the opportunity first")
	}
	if in.Stage != nil && !validOpportunityStage[*in.Stage] {
		return nil, protocol.BadInput("stage %q is not valid", *in.Stage)
	}
	if in.EstAmount != nil && *in.EstAmount < 0 {
		return nil, protocol.BadInput("est_amount cannot be negative")
	}
	if in.WinProbability != nil && (*in.WinProbability < 0 || *in.WinProbability > 1) {
		return nil, protocol.BadInput("win_probability must be between 0 and 1")
	}
	if in.ExpectedSignDate != nil && *in.ExpectedSignDate != "" {
		if err := ValidateDate("expected_sign_date", *in.ExpectedSignDate); err != nil {
			return nil, err
		}
	}
	return s.execute(ctx, "opportunity.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		return updateOpportunityTx(ctx, tx, wc, now, in)
	})
}

const opportunityColumns = `
    id, account_id, area_id, primary_contact_id, name, source, source_batch, stage,
    est_amount, win_probability, expected_sign_date, owner, next_step, lost_reason,
    closed_at, note, legacy_ref, version, created_at, updated_at`

func scanOpportunity(row interface{ Scan(...any) error }) (*Opportunity, error) {
	var o Opportunity
	err := row.Scan(&o.ID, &o.AccountID, &o.AreaID, &o.PrimaryContactID, &o.Name, &o.Source,
		&o.SourceBatch, &o.Stage, &o.EstAmount, &o.WinProbability, &o.ExpectedSignDate,
		&o.Owner, &o.NextStep, &o.LostReason, &o.ClosedAt, &o.Note, &o.LegacyRef,
		&o.Version, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func loadOpportunity(ctx context.Context, tx *sql.Tx, id string) (*Opportunity, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+opportunityColumns+` FROM opportunities WHERE id = ?`, id)
	o, err := scanOpportunity(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("opportunity %s does not exist", id)
	}
	return o, err
}

// GetOpportunity loads one opportunity by id.
func (s *Store) GetOpportunity(ctx context.Context, id string) (*Opportunity, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+opportunityColumns+` FROM opportunities WHERE id = ?`, id)
	o, err := scanOpportunity(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("opportunity %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return o, nil
}

// OpportunityFilter is the query surface of `mycontext opportunity list`.
type OpportunityFilter struct {
	AccountID string
	AreaID    string
	Stage     string
	OpenOnly  bool
	Search    string
	Limit     int
}

// ListOpportunities returns opportunities, nearest expected sign date first.
func (s *Store) ListOpportunities(ctx context.Context, f OpportunityFilter) ([]*Opportunity, error) {
	var where []string
	var args []any
	if f.AccountID != "" {
		where = append(where, "account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.AreaID != "" {
		where = append(where, "area_id = ?")
		args = append(args, f.AreaID)
	}
	if f.Stage != "" {
		where = append(where, "stage = ?")
		args = append(args, f.Stage)
	}
	if f.OpenOnly {
		where = append(where, "stage NOT IN ('won','lost')")
	}
	if f.Search != "" {
		where = append(where, "name LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}

	query := `SELECT ` + opportunityColumns + ` FROM opportunities`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY expected_sign_date IS NULL, expected_sign_date, created_at LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*Opportunity{}
	for rows.Next() {
		o, err := scanOpportunity(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, o)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// applications: we apply, someone else decides (mode D).
// ---------------------------------------------------------------------------

// CreateApplicationInput is the payload of `application.create`.
type CreateApplicationInput struct {
	AreaID       string   `json:"area_id,omitempty"`
	AccountID    string   `json:"account_id,omitempty"`
	ProjectID    string   `json:"project_id,omitempty"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind,omitempty"`
	Stage        string   `json:"stage,omitempty"`
	SubmittedAt  string   `json:"submitted_at,omitempty"`
	DecidedAt    string   `json:"decided_at,omitempty"`
	PrizeAmount  *float64 `json:"prize_amount,omitempty"`
	OutcomeNote  string   `json:"outcome_note,omitempty"`
	RejectReason string   `json:"reject_reason,omitempty"`
	Owner        string   `json:"owner,omitempty"`
	NextStep     string   `json:"next_step,omitempty"`
	LegacyRef    string   `json:"legacy_ref,omitempty"`
}

func (in *CreateApplicationInput) normalize() error {
	if in.Name == "" {
		return protocol.BadInput("name is required")
	}
	if in.Kind == "" {
		in.Kind = "competition"
	}
	if !validApplicationKind[in.Kind] {
		return protocol.BadInput("kind must be competition|program|job|listing|partnership")
	}
	if in.Stage == "" {
		in.Stage = "discovered"
	}
	if !validApplicationStage[in.Stage] {
		return protocol.BadInput("stage %q is not valid", in.Stage)
	}
	if in.Stage == "rejected" && in.RejectReason == "" {
		return protocol.BadInput("stage rejected requires reject_reason")
	}
	if in.PrizeAmount != nil && *in.PrizeAmount < 0 {
		return protocol.BadInput("prize_amount cannot be negative")
	}
	if in.SubmittedAt != "" {
		if err := ValidateTimestamp("submitted_at", in.SubmittedAt); err != nil {
			return err
		}
	}
	if in.DecidedAt != "" {
		if err := ValidateTimestamp("decided_at", in.DecidedAt); err != nil {
			return err
		}
	}
	return nil
}

func isTerminalApplicationStage(stage string) bool {
	return stage == "won" || stage == "rejected" || stage == "withdrawn"
}

// CreateApplication records something we apply for. A terminal stage at
// creation gets decided_at set automatically if not supplied.
func (s *Store) CreateApplication(ctx context.Context, wc WriteContext, in CreateApplicationInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "application.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if in.AreaID != "" {
			if err := requireExists(ctx, tx, "areas", in.AreaID, "area"); err != nil {
				return nil, err
			}
		}
		if in.AccountID != "" {
			if err := requireExists(ctx, tx, "accounts", in.AccountID, "account"); err != nil {
				return nil, err
			}
		}
		if in.ProjectID != "" {
			if err := requireExists(ctx, tx, "projects", in.ProjectID, "project"); err != nil {
				return nil, err
			}
		}
		id := system.NewID("app")
		ts := system.FormatTimestamp(now)
		decidedAt := in.DecidedAt
		if isTerminalApplicationStage(in.Stage) && decidedAt == "" {
			decidedAt = ts
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO applications (id, area_id, account_id, project_id, name, kind, stage,
                                      submitted_at, decided_at, prize_amount, outcome_note,
                                      reject_reason, owner, next_step, legacy_ref,
                                      version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			id, nullString(in.AreaID), nullString(in.AccountID), nullString(in.ProjectID),
			in.Name, in.Kind, in.Stage, nullString(in.SubmittedAt), nullString(decidedAt),
			nullFloat(in.PrizeAmount), nullString(in.OutcomeNote), nullString(in.RejectReason),
			nullString(in.Owner), nullString(in.NextStep), nullString(in.LegacyRef), ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "application", id, "created", nil, in); err != nil {
			return nil, err
		}
		a, err := loadApplication(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: a,
			Changes: []protocol.Change{{EntityType: "application", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"applications"}}},
		}, nil
	})
}

// UpdateApplicationInput patches an application under optimistic concurrency.
type UpdateApplicationInput struct {
	ApplicationID   string   `json:"application_id"`
	ExpectedVersion int64    `json:"expected_version"`
	Name            *string  `json:"name,omitempty"`
	ProjectID       *string  `json:"project_id,omitempty"`
	AccountID       *string  `json:"account_id,omitempty"`
	AreaID          *string  `json:"area_id,omitempty"`
	Stage           *string  `json:"stage,omitempty"`
	SubmittedAt     *string  `json:"submitted_at,omitempty"`
	DecidedAt       *string  `json:"decided_at,omitempty"`
	PrizeAmount     *float64 `json:"prize_amount,omitempty"`
	OutcomeNote     *string  `json:"outcome_note,omitempty"`
	RejectReason    *string  `json:"reject_reason,omitempty"`
	Owner           *string  `json:"owner,omitempty"`
	NextStep        *string  `json:"next_step,omitempty"`
}

// UpdateApplication applies a patch under optimistic concurrency control.
// Rejecting without a reason, or reaching a terminal stage without a decision
// date, is refused - the same "state a fact with its evidence" rule the SQL
// CHECKs already enforce, given here so the error is BAD_INPUT rather than a
// driver constraint failure.
func (s *Store) UpdateApplication(ctx context.Context, wc WriteContext, in UpdateApplicationInput) (*Result, error) {
	if in.ApplicationID == "" {
		return nil, protocol.BadInput("application_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the application first")
	}
	if in.Stage != nil && !validApplicationStage[*in.Stage] {
		return nil, protocol.BadInput("stage %q is not valid", *in.Stage)
	}
	if in.PrizeAmount != nil && *in.PrizeAmount < 0 {
		return nil, protocol.BadInput("prize_amount cannot be negative")
	}
	if in.SubmittedAt != nil && *in.SubmittedAt != "" {
		if err := ValidateTimestamp("submitted_at", *in.SubmittedAt); err != nil {
			return nil, err
		}
	}
	if in.DecidedAt != nil && *in.DecidedAt != "" {
		if err := ValidateTimestamp("decided_at", *in.DecidedAt); err != nil {
			return nil, err
		}
	}
	return s.execute(ctx, "application.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadApplication(ctx, tx, in.ApplicationID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("application", in.ExpectedVersion, before.Version)
		}
		if in.ProjectID != nil && *in.ProjectID != "" {
			if err := requireExists(ctx, tx, "projects", *in.ProjectID, "project"); err != nil {
				return nil, err
			}
		}
		if in.AccountID != nil && *in.AccountID != "" {
			if err := requireExists(ctx, tx, "accounts", *in.AccountID, "account"); err != nil {
				return nil, err
			}
		}

		set := newPatch()
		set.str("name", in.Name)
		set.str("project_id", in.ProjectID)
		set.str("account_id", in.AccountID)
		set.str("area_id", in.AreaID)
		set.str("submitted_at", in.SubmittedAt)
		set.str("outcome_note", in.OutcomeNote)
		set.str("reject_reason", in.RejectReason)
		set.str("owner", in.Owner)
		set.str("next_step", in.NextStep)
		set.flt("prize_amount", in.PrizeAmount)

		eventType := "updated"
		if in.Stage != nil {
			if *in.Stage == "rejected" {
				reason := before.RejectReason
				if in.RejectReason != nil {
					reason = in.RejectReason
				}
				if reason == nil || *reason == "" {
					return nil, protocol.BadInput("stage rejected requires reject_reason")
				}
			}
			set.raw("stage", *in.Stage)
			if isTerminalApplicationStage(*in.Stage) {
				decided := before.DecidedAt
				if in.DecidedAt != nil {
					decided = in.DecidedAt
				}
				if decided == nil || *decided == "" {
					set.raw("decided_at", system.FormatTimestamp(now))
				}
			} else if before.DecidedAt != nil {
				set.raw("decided_at", nil)
			}
			if *in.Stage == "won" {
				eventType = "won"
			} else {
				eventType = "stage_changed"
			}
		} else if in.DecidedAt != nil {
			set.str("decided_at", in.DecidedAt)
		}
		if set.empty() {
			return nil, protocol.BadInput("no fields to update")
		}
		if err := set.apply(ctx, tx, "applications", "application", in.ApplicationID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		after, err := loadApplication(ctx, tx, in.ApplicationID)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "application", in.ApplicationID, eventType, before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "application", EntityID: in.ApplicationID,
				EventType: eventType, Version: after.Version, ProjectionKeys: []string{"applications"}}},
		}, nil
	})
}

const applicationColumns = `
    id, area_id, account_id, project_id, name, kind, stage, submitted_at, decided_at,
    prize_amount, outcome_note, reject_reason, owner, next_step, legacy_ref,
    version, created_at, updated_at`

func scanApplication(row interface{ Scan(...any) error }) (*Application, error) {
	var a Application
	err := row.Scan(&a.ID, &a.AreaID, &a.AccountID, &a.ProjectID, &a.Name, &a.Kind, &a.Stage,
		&a.SubmittedAt, &a.DecidedAt, &a.PrizeAmount, &a.OutcomeNote, &a.RejectReason,
		&a.Owner, &a.NextStep, &a.LegacyRef, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func loadApplication(ctx context.Context, tx *sql.Tx, id string) (*Application, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE id = ?`, id)
	a, err := scanApplication(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("application %s does not exist", id)
	}
	return a, err
}

// GetApplication loads one application by id.
func (s *Store) GetApplication(ctx context.Context, id string) (*Application, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE id = ?`, id)
	a, err := scanApplication(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("application %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return a, nil
}

// ApplicationFilter is the query surface of `mycontext application list`.
type ApplicationFilter struct {
	AreaID    string
	AccountID string
	Kind      string
	Stage     string
	OpenOnly  bool
	Search    string
	Limit     int
}

// ListApplications returns applications, most recently submitted first.
func (s *Store) ListApplications(ctx context.Context, f ApplicationFilter) ([]*Application, error) {
	var where []string
	var args []any
	if f.AreaID != "" {
		where = append(where, "area_id = ?")
		args = append(args, f.AreaID)
	}
	if f.AccountID != "" {
		where = append(where, "account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.Stage != "" {
		where = append(where, "stage = ?")
		args = append(args, f.Stage)
	}
	if f.OpenOnly {
		where = append(where, "stage NOT IN ('won','rejected','withdrawn')")
	}
	if f.Search != "" {
		where = append(where, "name LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}

	query := `SELECT ` + applicationColumns + ` FROM applications`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY submitted_at IS NULL, submitted_at DESC, created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*Application{}
	for rows.Next() {
		a, err := scanApplication(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, a)
	}
	return out, sqlite.Classify(rows.Err())
}
