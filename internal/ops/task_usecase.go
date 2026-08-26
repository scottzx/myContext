package ops

import (
	"context"
	"database/sql"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// CreateTaskInput is the payload of `task.create`.
type CreateTaskInput struct {
	ProjectID          string   `json:"project_id,omitempty"`
	ParentTaskID       string   `json:"parent_task_id,omitempty"`
	MilestoneID        string   `json:"milestone_id,omitempty"`
	Title              string   `json:"title"`
	Detail             string   `json:"detail,omitempty"`
	CompletionCriteria string   `json:"completion_criteria,omitempty"`
	Status             string   `json:"status,omitempty"`
	Importance         string   `json:"importance,omitempty"`
	HardDueAt          string   `json:"hard_due_at,omitempty"`
	EarliestStartAt    string   `json:"earliest_start_at,omitempty"`
	NextReviewAt       string   `json:"next_review_at,omitempty"`
	EstimateMinutes    int      `json:"estimate_minutes,omitempty"`
	MetricName         string   `json:"metric_name,omitempty"`
	MetricUnit         string   `json:"metric_unit,omitempty"`
	TargetValue        *float64 `json:"target_value,omitempty"`
	CurrentValue       *float64 `json:"current_value,omitempty"`
	WaitingFor         string   `json:"waiting_for,omitempty"`
	PlannedDate        string   `json:"planned_date,omitempty"`
	TimeSlot           string   `json:"time_slot,omitempty"`
	PlannedMinutes     int      `json:"planned_minutes,omitempty"`
	LegacyRef          string   `json:"legacy_ref,omitempty"`
}

func (in *CreateTaskInput) normalize() error {
	if in.Title == "" {
		return protocol.BadInput("title is required")
	}
	if in.Status == "" {
		in.Status = string(TaskTodo)
	}
	if in.Importance == "" {
		in.Importance = string(P2)
	}
	if err := validateTaskStatus(in.Status); err != nil {
		return err
	}
	if err := validateImportance(in.Importance); err != nil {
		return err
	}
	if in.HardDueAt != "" {
		if err := ValidateTimestamp("hard_due_at", in.HardDueAt); err != nil {
			return err
		}
	}
	if in.EarliestStartAt != "" {
		if err := ValidateTimestamp("earliest_start_at", in.EarliestStartAt); err != nil {
			return err
		}
	}
	if in.NextReviewAt != "" {
		if err := ValidateDate("next_review_at", in.NextReviewAt); err != nil {
			return err
		}
	}
	if in.PlannedDate != "" {
		if err := ValidateDate("planned_date", in.PlannedDate); err != nil {
			return err
		}
	}
	if in.TimeSlot != "" && !validTimeSlot[in.TimeSlot] {
		return protocol.BadInput("time_slot must be morning/afternoon/evening, got %q", in.TimeSlot)
	}
	if in.EstimateMinutes < 0 || in.PlannedMinutes < 0 {
		return protocol.BadInput("minutes must be positive")
	}
	if in.TimeSlot != "" && in.PlannedDate == "" {
		return protocol.BadInput("time_slot requires planned_date")
	}
	return nil
}

// CreateTask inserts a task and, when a planned date is supplied, its first
// active schedule.
func (s *Store) CreateTask(ctx context.Context, wc WriteContext, in CreateTaskInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "task.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if in.ProjectID != "" {
			if err := requireExists(ctx, tx, "projects", in.ProjectID, "project"); err != nil {
				return nil, err
			}
		}
		if in.ParentTaskID != "" {
			if err := requireExists(ctx, tx, "tasks", in.ParentTaskID, "parent task"); err != nil {
				return nil, err
			}
		}
		if in.MilestoneID != "" {
			if err := requireExists(ctx, tx, "milestones", in.MilestoneID, "milestone"); err != nil {
				return nil, err
			}
		}

		id := system.NewID("task")
		ts := system.FormatTimestamp(now)
		_, err := tx.ExecContext(ctx, `
            INSERT INTO tasks (id, project_id, parent_task_id, milestone_id, title, detail,
                               completion_criteria, status, importance, hard_due_at,
                               earliest_start_at, next_review_at, estimate_minutes,
                               metric_name, metric_unit, target_value, current_value,
                               waiting_for, legacy_ref, version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			id, nullString(in.ProjectID), nullString(in.ParentTaskID),
			nullString(in.MilestoneID), in.Title,
			nullString(in.Detail), nullString(in.CompletionCriteria), in.Status, in.Importance,
			nullString(in.HardDueAt), nullString(in.EarliestStartAt), nullString(in.NextReviewAt),
			nullInt(in.EstimateMinutes), nullString(in.MetricName), nullString(in.MetricUnit),
			nullFloat(in.TargetValue), nullFloat(in.CurrentValue),
			nullString(in.WaitingFor), nullString(in.LegacyRef), ts, ts)
		if err != nil {
			return nil, err
		}

		if in.PlannedDate != "" {
			if err := insertSchedule(ctx, tx, wc, now, system.NewID("sch"), id,
				in.PlannedDate, in.TimeSlot, in.PlannedMinutes, ""); err != nil {
				return nil, err
			}
		}
		if err := recordEvent(ctx, tx, wc, now, "task", id, "created", nil, in); err != nil {
			return nil, err
		}

		task, err := loadTaskTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: task,
			Changes: []protocol.Change{{
				EntityType: "task", EntityID: id, EventType: "created", Version: 1,
				ProjectionKeys: projectionKeysForTask(task),
			}},
		}, nil
	})
}

// UpdateTaskInput patches a task. Only non-nil fields are touched, so an
// omitted field is never confused with "clear this field".
type UpdateTaskInput struct {
	TaskID          string  `json:"task_id"`
	ExpectedVersion int64   `json:"expected_version"`
	Title           *string `json:"title,omitempty"`
	Detail          *string `json:"detail,omitempty"`
	Status          *string `json:"status,omitempty"`
	Importance      *string `json:"importance,omitempty"`
	EstimateMinutes *int    `json:"estimate_minutes,omitempty"`
	WaitingFor      *string `json:"waiting_for,omitempty"`
	ProjectID       *string `json:"project_id,omitempty"`
	MilestoneID     *string `json:"milestone_id,omitempty"`
	CompletionCrit  *string `json:"completion_criteria,omitempty"`

	// HardDueAt is deliberately separate from planning: changing a real
	// deadline needs an explicit reason (§7.2, §15).
	HardDueAt    *string `json:"hard_due_at,omitempty"`
	NextReviewAt *string `json:"next_review_at,omitempty"`
}

// UpdateTask applies a patch under optimistic concurrency control.
func (s *Store) UpdateTask(ctx context.Context, wc WriteContext, in UpdateTaskInput) (*Result, error) {
	if in.TaskID == "" {
		return nil, protocol.BadInput("task_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the task first")
	}
	if in.Status != nil {
		if err := validateTaskStatus(*in.Status); err != nil {
			return nil, err
		}
	}
	if in.Importance != nil {
		if err := validateImportance(*in.Importance); err != nil {
			return nil, err
		}
	}
	if in.HardDueAt != nil && *in.HardDueAt != "" {
		if err := ValidateTimestamp("hard_due_at", *in.HardDueAt); err != nil {
			return nil, err
		}
	}
	if in.NextReviewAt != nil && *in.NextReviewAt != "" {
		if err := ValidateDate("next_review_at", *in.NextReviewAt); err != nil {
			return nil, err
		}
	}
	if in.HardDueAt != nil && wc.Reason == "" {
		return nil, &protocol.AppError{
			Code:    protocol.CodeForbidden,
			Message: "changing a hard deadline requires --reason",
		}
	}

	return s.execute(ctx, "task.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("task", in.ExpectedVersion, before.Version)
		}

		set := newPatch()
		set.str("title", in.Title)
		set.str("detail", in.Detail)
		set.str("status", in.Status)
		set.str("importance", in.Importance)
		set.str("waiting_for", in.WaitingFor)
		set.str("project_id", in.ProjectID)
		set.str("milestone_id", in.MilestoneID)
		set.str("completion_criteria", in.CompletionCrit)
		set.str("hard_due_at", in.HardDueAt)
		set.str("next_review_at", in.NextReviewAt)
		set.num("estimate_minutes", in.EstimateMinutes)

		if in.Status != nil && isTerminal(*in.Status) && before.CompletedAt == nil {
			set.raw("completed_at", system.FormatTimestamp(now))
		}
		if set.empty() {
			return nil, protocol.BadInput("no fields to update")
		}
		if in.ProjectID != nil && *in.ProjectID != "" {
			if err := requireExists(ctx, tx, "projects", *in.ProjectID, "project"); err != nil {
				return nil, err
			}
		}
		if in.MilestoneID != nil && *in.MilestoneID != "" {
			if err := requireExists(ctx, tx, "milestones", *in.MilestoneID, "milestone"); err != nil {
				return nil, err
			}
		}

		if err := set.applyToTask(ctx, tx, in.TaskID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}

		after, err := loadTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return nil, err
		}
		for _, eventType := range eventTypesForUpdate(in) {
			if err := recordEvent(ctx, tx, wc, now, "task", in.TaskID, eventType, before, after); err != nil {
				return nil, err
			}
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{
				EntityType: "task", EntityID: in.TaskID, EventType: "updated", Version: after.Version,
				ProjectionKeys: projectionKeysForTask(after),
			}},
		}, nil
	})
}

// eventTypesForUpdate records a specific event per meaningful dimension, so
// the history answers "when did the deadline move" without diffing JSON.
func eventTypesForUpdate(in UpdateTaskInput) []string {
	var types []string
	if in.Status != nil {
		types = append(types, "status_changed")
	}
	if in.Importance != nil {
		types = append(types, "importance_changed")
	}
	if in.HardDueAt != nil {
		types = append(types, "deadline_changed")
	}
	if in.NextReviewAt != nil {
		types = append(types, "review_set")
	}
	if len(types) == 0 {
		types = append(types, "updated")
	}
	return types
}

// RescheduleInput moves a task to another day.
type RescheduleInput struct {
	TaskID          string `json:"task_id"`
	ExpectedVersion int64  `json:"expected_version"`
	NewDate         string `json:"new_date"`
	TimeSlot        string `json:"time_slot,omitempty"`
	PlannedMinutes  int    `json:"planned_minutes,omitempty"`
	Note            string `json:"note,omitempty"`
}

// RescheduleTask supersedes the current plan and creates a new one. The old
// date stays in the history; nothing is overwritten (§7.4).
func (s *Store) RescheduleTask(ctx context.Context, wc WriteContext, in RescheduleInput) (*Result, error) {
	if in.TaskID == "" {
		return nil, protocol.BadInput("task_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the task first")
	}
	if err := ValidateDate("new_date", in.NewDate); err != nil {
		return nil, err
	}
	if in.TimeSlot != "" && !validTimeSlot[in.TimeSlot] {
		return nil, protocol.BadInput("time_slot must be morning/afternoon/evening, got %q", in.TimeSlot)
	}

	return s.execute(ctx, "task.reschedule", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("task", in.ExpectedVersion, before.Version)
		}
		if isTerminal(string(before.Status)) {
			return nil, protocol.BadInput("cannot reschedule a %s task", before.Status)
		}

		minutes := in.PlannedMinutes
		if minutes == 0 && before.Schedule != nil && before.Schedule.PlannedMinutes != nil {
			minutes = *before.Schedule.PlannedMinutes
		}
		// Order matters twice over: the old plan must stop being active before
		// the new one is inserted (the "one active plan per task" index), and
		// its superseded_by can only be set once the new row exists (foreign
		// key). So: retire, insert, then link.
		newScheduleID := system.NewID("sch")
		if before.Schedule != nil {
			if _, err := tx.ExecContext(ctx, `
                UPDATE task_schedules SET status = 'superseded' WHERE id = ?`,
				before.Schedule.ID); err != nil {
				return nil, err
			}
		}
		if err := insertSchedule(ctx, tx, wc, now, newScheduleID, in.TaskID,
			in.NewDate, in.TimeSlot, minutes, in.Note); err != nil {
			return nil, err
		}
		if before.Schedule != nil {
			if _, err := tx.ExecContext(ctx, `
                UPDATE task_schedules SET superseded_by = ? WHERE id = ?`,
				newScheduleID, before.Schedule.ID); err != nil {
				return nil, err
			}
		}
		if err := bumpTaskVersion(ctx, tx, in.TaskID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "task", in.TaskID, "rescheduled",
			scheduleSummary(before.Schedule),
			map[string]any{"planned_date": in.NewDate, "time_slot": in.TimeSlot, "schedule_id": newScheduleID}); err != nil {
			return nil, err
		}

		after, err := loadTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return nil, err
		}
		keys := projectionKeysForTask(after)
		if before.Schedule != nil {
			keys = append(keys, "day:"+before.Schedule.PlannedDate)
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{
				EntityType: "task", EntityID: in.TaskID, EventType: "rescheduled",
				Version: after.Version, ProjectionKeys: keys,
			}},
		}, nil
	})
}

// SetReviewInput parks a task until a date without hiding it.
type SetReviewInput struct {
	TaskID          string `json:"task_id"`
	ExpectedVersion int64  `json:"expected_version"`
	ReviewDate      string `json:"review_date"`
	Status          string `json:"status,omitempty"`
	WaitingFor      string `json:"waiting_for,omitempty"`
}

// SetTaskReview drops the active plan and sets a review date, so the task
// leaves today's board but reappears in v_review_due (§7.4).
func (s *Store) SetTaskReview(ctx context.Context, wc WriteContext, in SetReviewInput) (*Result, error) {
	if in.TaskID == "" {
		return nil, protocol.BadInput("task_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the task first")
	}
	if err := ValidateDate("review_date", in.ReviewDate); err != nil {
		return nil, err
	}
	if in.Status != "" {
		if err := validateTaskStatus(in.Status); err != nil {
			return nil, err
		}
	}

	return s.execute(ctx, "task.set-review", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("task", in.ExpectedVersion, before.Version)
		}
		if before.Schedule != nil {
			if _, err := tx.ExecContext(ctx, `
                UPDATE task_schedules SET status = 'cancelled' WHERE id = ?`,
				before.Schedule.ID); err != nil {
				return nil, err
			}
		}
		set := newPatch()
		set.raw("next_review_at", in.ReviewDate)
		if in.Status != "" {
			set.raw("status", in.Status)
		}
		if in.WaitingFor != "" {
			set.raw("waiting_for", in.WaitingFor)
		}
		if err := set.applyToTask(ctx, tx, in.TaskID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "task", in.TaskID, "review_set", before,
			map[string]any{"next_review_at": in.ReviewDate, "status": in.Status}); err != nil {
			return nil, err
		}
		after, err := loadTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{
				EntityType: "task", EntityID: in.TaskID, EventType: "review_set",
				Version: after.Version, ProjectionKeys: append(projectionKeysForTask(after), "review_due"),
			}},
		}, nil
	})
}

// CompleteTaskInput closes out a task.
type CompleteTaskInput struct {
	TaskID          string `json:"task_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Note            string `json:"note,omitempty"`
}

// CompleteTask marks a task done and completes its active schedule.
func (s *Store) CompleteTask(ctx context.Context, wc WriteContext, in CompleteTaskInput) (*Result, error) {
	if in.TaskID == "" {
		return nil, protocol.BadInput("task_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the task first")
	}
	return s.execute(ctx, "task.complete", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("task", in.ExpectedVersion, before.Version)
		}
		if before.Status == TaskDone {
			return &Result{Data: before, Warnings: []string{"task was already done"}}, nil
		}
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            UPDATE tasks SET status = 'done', completed_at = ?, updated_at = ?, version = version + 1
             WHERE id = ? AND version = ?`, ts, ts, in.TaskID, in.ExpectedVersion); err != nil {
			return nil, err
		}
		if before.Schedule != nil {
			if _, err := tx.ExecContext(ctx, `
                UPDATE task_schedules SET status = 'completed' WHERE id = ?`,
				before.Schedule.ID); err != nil {
				return nil, err
			}
		}
		if err := recordEvent(ctx, tx, wc, now, "task", in.TaskID, "completed", before,
			map[string]any{"status": "done", "completed_at": ts, "note": in.Note}); err != nil {
			return nil, err
		}
		after, err := loadTaskTx(ctx, tx, in.TaskID)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{
				EntityType: "task", EntityID: in.TaskID, EventType: "completed",
				Version: after.Version, ProjectionKeys: projectionKeysForTask(after),
			}},
		}, nil
	})
}

func insertSchedule(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	id, taskID, date, slot string, minutes int, note string) error {

	createdBy := "cli"
	switch wc.Actor.Type {
	case "ui":
		createdBy = "user_ui"
	case "agent":
		createdBy = "agent"
	case "migration":
		createdBy = "migration"
	}
	_, err := tx.ExecContext(ctx, `
        INSERT INTO task_schedules (id, task_id, planned_date, time_slot, planned_minutes,
                                    status, created_by, note, created_at)
        VALUES (?,?,?,?,?,'active',?,?,?)`,
		id, taskID, date, nullString(slot), nullInt(minutes), createdBy, nullString(note),
		system.FormatTimestamp(now))
	return err
}

func scheduleSummary(s *Schedule) any {
	if s == nil {
		return map[string]any{"planned_date": nil}
	}
	return map[string]any{"planned_date": s.PlannedDate, "time_slot": s.TimeSlot, "schedule_id": s.ID}
}

// projectionKeysForTask tells the UI which read models this change touched.
func projectionKeysForTask(t *Task) []string {
	keys := []string{"tasks"}
	if t.Schedule != nil {
		keys = append(keys, "day:"+t.Schedule.PlannedDate, "week")
	}
	if t.ProjectID != nil {
		keys = append(keys, "project:"+*t.ProjectID)
	}
	if t.HardDueAt != nil {
		keys = append(keys, "overdue")
	}
	return keys
}

func isTerminal(status string) bool {
	return status == "done" || status == "cancelled" || status == "archived"
}

func loadTaskTx(ctx context.Context, tx *sql.Tx, id string) (*Task, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+taskColumns+taskFrom+` WHERE t.id = ?`, id)
	task, err := scanTask(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("task %s does not exist", id)
	}
	return task, err
}

func bumpTaskVersion(ctx context.Context, tx *sql.Tx, id string, expected int64, now time.Time) error {
	res, err := tx.ExecContext(ctx, `
        UPDATE tasks SET updated_at = ?, version = version + 1
         WHERE id = ? AND version = ?`, system.FormatTimestamp(now), id, expected)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return protocol.VersionConflict("task", expected, -1)
	}
	return nil
}

func requireExists(ctx context.Context, tx *sql.Tx, table, id, label string) error {
	var found int
	if table == "" {
		// An entity type that passed validation but has no table would other-
		// wise splice an empty name into the statement and surface as a SQL
		// syntax error. entityTables is the single source of both, so this is
		// unreachable; it fails loudly rather than malformed if that changes.
		return protocol.Internal("no table is registered for %s", label)
	}
	// The table name is a compile-time constant from the caller, never input.
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&found)
	if err == sql.ErrNoRows {
		return protocol.NotFound("%s %s does not exist", label, id)
	}
	return err
}

func nullInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}
