package ops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
)

const taskColumns = `
    t.id, t.project_id, t.parent_task_id, t.title, t.detail, t.completion_criteria,
    t.status, t.importance, t.hard_due_at, t.earliest_start_at, t.next_review_at,
    t.estimate_minutes, t.waiting_for, t.legacy_ref, t.legacy_due_date,
    t.version, t.created_at, t.updated_at, t.completed_at,
    s.id, s.planned_date, s.time_slot, s.planned_minutes, s.status, s.created_by, s.note, s.created_at`

const taskFrom = `
    FROM tasks t
    LEFT JOIN task_schedules s ON s.task_id = t.id AND s.status = 'active'`

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	var sched Schedule
	var schedID, plannedDate, timeSlot, schedStatus, createdBy, note, schedCreated sql.NullString
	var plannedMinutes sql.NullInt64

	err := row.Scan(
		&t.ID, &t.ProjectID, &t.ParentTaskID, &t.Title, &t.Detail, &t.CompletionCriteria,
		&t.Status, &t.Importance, &t.HardDueAt, &t.EarliestStartAt, &t.NextReviewAt,
		&t.EstimateMinutes, &t.WaitingFor, &t.LegacyRef, &t.LegacyDueDate,
		&t.Version, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt,
		&schedID, &plannedDate, &timeSlot, &plannedMinutes, &schedStatus, &createdBy, &note, &schedCreated)
	if err != nil {
		return nil, err
	}
	if schedID.Valid {
		sched.ID = schedID.String
		sched.TaskID = t.ID
		sched.PlannedDate = plannedDate.String
		sched.Status = schedStatus.String
		sched.CreatedBy = createdBy.String
		sched.CreatedAt = schedCreated.String
		if timeSlot.Valid {
			sched.TimeSlot = &timeSlot.String
		}
		if note.Valid {
			sched.Note = &note.String
		}
		if plannedMinutes.Valid {
			m := int(plannedMinutes.Int64)
			sched.PlannedMinutes = &m
		}
		t.Schedule = &sched
	}
	return &t, nil
}

// GetTask loads one task by ID, including its active schedule.
func (s *Store) GetTask(ctx context.Context, id string) (*Task, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+taskColumns+taskFrom+` WHERE t.id = ?`, id)
	task, err := scanTask(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("task %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return task, nil
}

// TaskFilter is the query surface of `mycontext task list`.
type TaskFilter struct {
	Status      []string
	Importance  []string
	ProjectID   string
	Search      string
	PlannedDate string
	Open        bool // exclude done/cancelled/archived
	Unscheduled bool
	Limit       int
	Offset      int
}

// ListTasks applies a filter. Every value is bound as a parameter; no query
// text is ever assembled from user input (§20.2).
func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]*Task, error) {
	var where []string
	var args []any

	if len(f.Status) > 0 {
		for _, st := range f.Status {
			if err := validateTaskStatus(st); err != nil {
				return nil, err
			}
		}
		where = append(where, "t.status IN ("+placeholders(len(f.Status))+")")
		args = append(args, toAny(f.Status)...)
	}
	if f.Open {
		where = append(where, "t.status NOT IN ('done','cancelled','archived')")
	}
	if len(f.Importance) > 0 {
		for _, imp := range f.Importance {
			if err := validateImportance(imp); err != nil {
				return nil, err
			}
		}
		where = append(where, "t.importance IN ("+placeholders(len(f.Importance))+")")
		args = append(args, toAny(f.Importance)...)
	}
	if f.ProjectID != "" {
		where = append(where, "t.project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.PlannedDate != "" {
		if err := ValidateDate("planned_date", f.PlannedDate); err != nil {
			return nil, err
		}
		where = append(where, "s.planned_date = ?")
		args = append(args, f.PlannedDate)
	}
	if f.Unscheduled {
		where = append(where, "s.id IS NULL")
	}
	if f.Search != "" {
		where = append(where, "(t.title LIKE ? ESCAPE '\\' OR t.detail LIKE ? ESCAPE '\\')")
		pattern := "%" + escapeLike(f.Search) + "%"
		args = append(args, pattern, pattern)
	}

	query := `SELECT ` + taskColumns + taskFrom
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	// Deterministic order: soonest plan, then importance, then creation.
	query += ` ORDER BY s.planned_date IS NULL, s.planned_date, t.importance, t.created_at`

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, max(f.Offset, 0))

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	tasks := []*Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		tasks = append(tasks, task)
	}
	return tasks, sqlite.Classify(rows.Err())
}

// FindTaskByReference resolves either an exact ID or a title search. A search
// that matches several tasks returns AMBIGUOUS_MATCH with the candidates
// rather than guessing (§10.4).
func (s *Store) FindTaskByReference(ctx context.Context, ref string) (*Task, error) {
	if ref == "" {
		return nil, protocol.BadInput("a task id or search term is required")
	}
	if task, err := s.GetTask(ctx, ref); err == nil {
		return task, nil
	} else if appErr, ok := err.(*protocol.AppError); !ok || appErr.Code != protocol.CodeNotFound {
		return nil, err
	}

	matches, err := s.ListTasks(ctx, TaskFilter{Search: ref, Open: true, Limit: 10})
	if err != nil {
		return nil, err
	}
	switch len(matches) {
	case 0:
		return nil, protocol.NotFound("no open task matches %q", ref)
	case 1:
		return matches[0], nil
	default:
		candidates := make([]map[string]string, 0, len(matches))
		for _, m := range matches {
			candidates = append(candidates, map[string]string{"id": m.ID, "title": m.Title})
		}
		return nil, protocol.Ambiguous(
			fmt.Sprintf("%d open tasks match %q; pass an explicit id", len(matches), ref),
			map[string]any{"candidates": candidates})
	}
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toAny(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// escapeLike neutralises LIKE wildcards in user input.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
