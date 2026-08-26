package ops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

const projectColumns = `
    p.id, p.initiative_id, p.parent_project_id, p.kind, p.name, p.description,
    p.status, p.stage, p.importance, p.target_date, p.start_date, p.end_date,
    p.hard_due_at, p.next_review_at, p.outcome, p.completion_criteria,
    p.metric_name, p.metric_unit, p.target_value, p.current_value,
    p.legacy_ref, p.version, p.created_at, p.updated_at, p.completed_at`

func scanProject(row interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.InitiativeID, &p.ParentProjectID, &p.Kind, &p.Name, &p.Description,
		&p.Status, &p.Stage, &p.Importance, &p.TargetDate, &p.StartDate, &p.EndDate,
		&p.HardDueAt, &p.NextReviewAt, &p.Outcome, &p.CompletionCriteria,
		&p.MetricName, &p.MetricUnit, &p.TargetValue, &p.CurrentValue,
		&p.LegacyRef, &p.Version, &p.CreatedAt, &p.UpdatedAt, &p.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProject loads one project by ID.
func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects p WHERE p.id = ?`, id)
	project, err := scanProject(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("project %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return project, nil
}

// ProjectFilter is the query surface of `mycontext project list`.
type ProjectFilter struct {
	Status       []string
	InitiativeID string
	AreaID       string
	Open         bool
	Search       string
	Limit        int
}

// ProjectSummary adds the counts a project list needs to be useful.
type ProjectSummary struct {
	*Project
	InitiativeName  *string `json:"initiative_name"`
	AreaName        *string `json:"area_name"`
	OpenTasks       int     `json:"open_tasks"`
	NextPlannedDate *string `json:"next_planned_date"`
}

// ListProjects returns projects with their open-task counts and next plan.
func (s *Store) ListProjects(ctx context.Context, f ProjectFilter) ([]*ProjectSummary, error) {
	var where []string
	var args []any

	if len(f.Status) > 0 {
		for _, st := range f.Status {
			if err := validateProjectStatus(st); err != nil {
				return nil, err
			}
		}
		where = append(where, "p.status IN ("+placeholders(len(f.Status))+")")
		args = append(args, toAny(f.Status)...)
	}
	if f.Open {
		where = append(where, "p.status IN ('planned','active','waiting','paused')")
	}
	if f.InitiativeID != "" {
		where = append(where, "p.initiative_id = ?")
		args = append(args, f.InitiativeID)
	}
	if f.AreaID != "" {
		where = append(where, "i.area_id = ?")
		args = append(args, f.AreaID)
	}
	if f.Search != "" {
		where = append(where, "p.name LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}

	query := `
        SELECT ` + projectColumns + `, i.name, a.name,
               (SELECT COUNT(*) FROM tasks t
                 WHERE t.project_id = p.id
                   AND t.status IN ('inbox','todo','doing','waiting','paused')),
               (SELECT MIN(s.planned_date) FROM task_schedules s
                  JOIN tasks t2 ON t2.id = s.task_id
                 WHERE t2.project_id = p.id AND s.status = 'active')
          FROM projects p
          LEFT JOIN initiatives i ON i.id = p.initiative_id
          LEFT JOIN areas a ON a.id = i.area_id`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY p.importance, p.status, p.name"
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*ProjectSummary{}
	for rows.Next() {
		var p Project
		var summary ProjectSummary
		err := rows.Scan(&p.ID, &p.InitiativeID, &p.ParentProjectID, &p.Kind, &p.Name, &p.Description,
			&p.Status, &p.Stage, &p.Importance, &p.TargetDate, &p.StartDate, &p.EndDate,
			&p.HardDueAt, &p.NextReviewAt, &p.Outcome, &p.CompletionCriteria,
			&p.MetricName, &p.MetricUnit, &p.TargetValue, &p.CurrentValue,
			&p.LegacyRef, &p.Version, &p.CreatedAt, &p.UpdatedAt, &p.CompletedAt,
			&summary.InitiativeName, &summary.AreaName, &summary.OpenTasks, &summary.NextPlannedDate)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		summary.Project = &p
		out = append(out, &summary)
	}
	return out, sqlite.Classify(rows.Err())
}

// CreateProjectInput is the payload of `project.create`.
type CreateProjectInput struct {
	InitiativeID       string   `json:"initiative_id,omitempty"`
	ParentProjectID    string   `json:"parent_project_id,omitempty"`
	Kind               string   `json:"kind,omitempty"`
	Name               string   `json:"name"`
	Description        string   `json:"description,omitempty"`
	Status             string   `json:"status,omitempty"`
	Stage              string   `json:"stage,omitempty"`
	Importance         string   `json:"importance,omitempty"`
	StartDate          string   `json:"start_date,omitempty"`
	EndDate            string   `json:"end_date,omitempty"`
	TargetDate         string   `json:"target_date,omitempty"`
	HardDueAt          string   `json:"hard_due_at,omitempty"`
	NextReviewAt       string   `json:"next_review_at,omitempty"`
	Outcome            string   `json:"outcome,omitempty"`
	CompletionCriteria string   `json:"completion_criteria,omitempty"`
	MetricName         string   `json:"metric_name,omitempty"`
	MetricUnit         string   `json:"metric_unit,omitempty"`
	TargetValue        *float64 `json:"target_value,omitempty"`
	CurrentValue       *float64 `json:"current_value,omitempty"`
	LegacyRef          string   `json:"legacy_ref,omitempty"`
	SortOrder          int      `json:"sort_order,omitempty"`
}

func (in *CreateProjectInput) normalize() error {
	if in.Name == "" {
		return protocol.BadInput("name is required")
	}
	if in.Status == "" {
		in.Status = string(ProjectPlanned)
	}
	if in.Importance == "" {
		in.Importance = string(P2)
	}
	if in.Kind == "" {
		in.Kind = "project"
	}
	if !validProjectKind[in.Kind] {
		return protocol.BadInput("kind must be project|sprint")
	}
	// A sprint is a time box inside something. Without a parent it is just a
	// project, and calling it a sprint would misdescribe it.
	if in.Kind == "sprint" && in.ParentProjectID == "" {
		return protocol.BadInput("a sprint requires parent_project_id")
	}
	if err := validateProjectStatus(in.Status); err != nil {
		return err
	}
	if err := validateImportance(in.Importance); err != nil {
		return err
	}
	if in.Stage != "" && !validStage[in.Stage] {
		return protocol.BadInput("stage %q is not valid", in.Stage)
	}
	for field, value := range map[string]string{
		"target_date": in.TargetDate, "next_review_at": in.NextReviewAt,
		"start_date": in.StartDate, "end_date": in.EndDate,
	} {
		if value != "" {
			if err := ValidateDate(field, value); err != nil {
				return err
			}
		}
	}
	if in.StartDate != "" && in.EndDate != "" && in.StartDate > in.EndDate {
		return protocol.BadInput("start_date must not be after end_date")
	}
	if in.HardDueAt != "" {
		if err := ValidateTimestamp("hard_due_at", in.HardDueAt); err != nil {
			return err
		}
	}
	return nil
}

// CreateProject inserts a project under an initiative.
func (s *Store) CreateProject(ctx context.Context, wc WriteContext, in CreateProjectInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "project.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if in.InitiativeID != "" {
			if err := requireExists(ctx, tx, "initiatives", in.InitiativeID, "initiative"); err != nil {
				return nil, err
			}
		}
		if in.ParentProjectID != "" {
			if err := requireExists(ctx, tx, "projects", in.ParentProjectID, "parent project"); err != nil {
				return nil, err
			}
		}
		id := system.NewID("proj")
		if in.Kind == "sprint" {
			id = system.NewID("sprint")
		}
		ts := system.FormatTimestamp(now)
		_, err := tx.ExecContext(ctx, `
            INSERT INTO projects (id, initiative_id, parent_project_id, kind, name, description,
                                  status, stage, importance, start_date, end_date,
                                  target_date, hard_due_at, next_review_at, outcome,
                                  completion_criteria, metric_name, metric_unit,
                                  target_value, current_value, legacy_ref, sort_order,
                                  version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			id, nullString(in.InitiativeID), nullString(in.ParentProjectID), in.Kind,
			in.Name, nullString(in.Description), in.Status, nullString(in.Stage), in.Importance,
			nullString(in.StartDate), nullString(in.EndDate),
			nullString(in.TargetDate), nullString(in.HardDueAt),
			nullString(in.NextReviewAt), nullString(in.Outcome), nullString(in.CompletionCriteria),
			nullString(in.MetricName), nullString(in.MetricUnit),
			nullFloat(in.TargetValue), nullFloat(in.CurrentValue),
			nullString(in.LegacyRef), in.SortOrder, ts, ts)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "project", id, "created", nil, in); err != nil {
			return nil, err
		}
		project, err := loadProjectTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: project,
			Changes: []protocol.Change{{
				EntityType: "project", EntityID: id, EventType: "created", Version: 1,
				ProjectionKeys: []string{"projects", "project:" + id},
			}},
		}, nil
	})
}

// UpdateProjectInput patches a project.
type UpdateProjectInput struct {
	ProjectID          string  `json:"project_id"`
	ExpectedVersion    int64   `json:"expected_version"`
	Name               *string `json:"name,omitempty"`
	Description        *string `json:"description,omitempty"`
	Status             *string `json:"status,omitempty"`
	Stage              *string `json:"stage,omitempty"`
	Importance         *string `json:"importance,omitempty"`
	TargetDate         *string `json:"target_date,omitempty"`
	HardDueAt          *string `json:"hard_due_at,omitempty"`
	NextReviewAt       *string `json:"next_review_at,omitempty"`
	Outcome            *string `json:"outcome,omitempty"`
	CompletionCriteria *string `json:"completion_criteria,omitempty"`
	InitiativeID       *string `json:"initiative_id,omitempty"`
}

// UpdateProject applies a patch under optimistic concurrency control.
// Pausing without a review date is refused: that is how work disappears.
func (s *Store) UpdateProject(ctx context.Context, wc WriteContext, in UpdateProjectInput) (*Result, error) {
	if in.ProjectID == "" {
		return nil, protocol.BadInput("project_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the project first")
	}
	if in.Status != nil {
		if err := validateProjectStatus(*in.Status); err != nil {
			return nil, err
		}
	}
	if in.Importance != nil {
		if err := validateImportance(*in.Importance); err != nil {
			return nil, err
		}
	}
	if in.Stage != nil && *in.Stage != "" && !validStage[*in.Stage] {
		return nil, protocol.BadInput("stage %q is not valid", *in.Stage)
	}
	if in.HardDueAt != nil && *in.HardDueAt != "" {
		if err := ValidateTimestamp("hard_due_at", *in.HardDueAt); err != nil {
			return nil, err
		}
	}
	if in.HardDueAt != nil && wc.Reason == "" {
		return nil, &protocol.AppError{
			Code:    protocol.CodeForbidden,
			Message: "changing a hard deadline requires --reason",
		}
	}

	return s.execute(ctx, "project.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadProjectTx(ctx, tx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("project", in.ExpectedVersion, before.Version)
		}
		// §15: a paused project must carry a review date or be archived.
		if in.Status != nil && (*in.Status == "paused" || *in.Status == "waiting") {
			review := before.NextReviewAt
			if in.NextReviewAt != nil && *in.NextReviewAt != "" {
				review = in.NextReviewAt
			}
			if review == nil || *review == "" {
				return nil, protocol.BadInput(
					"pausing a project requires next_review_at, otherwise it disappears silently")
			}
		}

		set := newPatch()
		set.str("name", in.Name)
		set.str("description", in.Description)
		set.str("status", in.Status)
		set.str("stage", in.Stage)
		set.str("importance", in.Importance)
		set.str("target_date", in.TargetDate)
		set.str("hard_due_at", in.HardDueAt)
		set.str("next_review_at", in.NextReviewAt)
		set.str("outcome", in.Outcome)
		set.str("completion_criteria", in.CompletionCriteria)
		set.str("initiative_id", in.InitiativeID)
		if in.Status != nil && isTerminal(*in.Status) && before.CompletedAt == nil {
			set.raw("completed_at", system.FormatTimestamp(now))
		}
		if set.empty() {
			return nil, protocol.BadInput("no fields to update")
		}
		if err := set.applyToProject(ctx, tx, in.ProjectID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}

		eventType := "updated"
		if in.Status != nil {
			eventType = "status_changed"
		}
		after, err := loadProjectTx(ctx, tx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "project", in.ProjectID, eventType, before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{
				EntityType: "project", EntityID: in.ProjectID, EventType: eventType,
				Version: after.Version, ProjectionKeys: []string{"projects", "project:" + in.ProjectID},
			}},
		}, nil
	})
}

// LinkProjectKRInput connects a project to a key result (many-to-many).
type LinkProjectKRInput struct {
	ProjectID   string `json:"project_id"`
	KeyResultID string `json:"key_result_id"`
	Note        string `json:"note,omitempty"`
}

// LinkProjectKR records that a project contributes to a KR, instead of
// forcing the project into the KR's tree.
func (s *Store) LinkProjectKR(ctx context.Context, wc WriteContext, in LinkProjectKRInput) (*Result, error) {
	if in.ProjectID == "" || in.KeyResultID == "" {
		return nil, protocol.BadInput("project_id and key_result_id are required")
	}
	return s.execute(ctx, "project.link-kr", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, "projects", in.ProjectID, "project"); err != nil {
			return nil, err
		}
		if err := requireExists(ctx, tx, "key_results", in.KeyResultID, "key result"); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO project_kr_links (project_id, key_result_id, note, created_at)
            VALUES (?,?,?,?)
            ON CONFLICT(project_id, key_result_id) DO UPDATE SET note = excluded.note`,
			in.ProjectID, in.KeyResultID, nullString(in.Note), system.FormatTimestamp(now)); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "project", in.ProjectID, "linked", nil, in); err != nil {
			return nil, err
		}
		return &Result{
			Data: in,
			Changes: []protocol.Change{{
				EntityType: "project", EntityID: in.ProjectID, EventType: "linked",
				ProjectionKeys: []string{"project:" + in.ProjectID},
			}},
		}, nil
	})
}

func loadProjectTx(ctx context.Context, tx *sql.Tx, id string) (*Project, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects p WHERE p.id = ?`, id)
	project, err := scanProject(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("project %s does not exist", id)
	}
	return project, err
}

// FindProjectByReference resolves an ID or a name search, refusing ambiguity.
func (s *Store) FindProjectByReference(ctx context.Context, ref string) (*Project, error) {
	if ref == "" {
		return nil, protocol.BadInput("a project id or search term is required")
	}
	if project, err := s.GetProject(ctx, ref); err == nil {
		return project, nil
	} else if appErr, ok := err.(*protocol.AppError); !ok || appErr.Code != protocol.CodeNotFound {
		return nil, err
	}
	matches, err := s.ListProjects(ctx, ProjectFilter{Search: ref, Limit: 10})
	if err != nil {
		return nil, err
	}
	switch len(matches) {
	case 0:
		return nil, protocol.NotFound("no project matches %q", ref)
	case 1:
		return matches[0].Project, nil
	default:
		candidates := make([]map[string]string, 0, len(matches))
		for _, m := range matches {
			candidates = append(candidates, map[string]string{"id": m.ID, "name": m.Name})
		}
		return nil, protocol.Ambiguous(
			fmt.Sprintf("%d projects match %q; pass an explicit id", len(matches), ref),
			map[string]any{"candidates": candidates})
	}
}
