package ops_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	mycontext "github.com/scottzx/mycontext"
	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// fixedNow anchors every test to one instant so day-boundary behaviour is
// reproducible rather than depending on when the suite runs.
var fixedNow = time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

const today = "2026-08-21"

func newTestStore(t *testing.T) *ops.Store {
	t.Helper()
	// The deterministic views compare against the process local day, so pin
	// the timezone the same way the CLI does.
	original := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = original })

	path := filepath.Join(t.TempDir(), "ops.db")
	db, err := sqlite.Open(path, sqlite.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	migrations, err := sqlite.LoadMigrations(mycontext.Migrations, mycontext.MigrationsDirOps)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := sqlite.Migrate(context.Background(), db, migrations, "test"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return ops.NewStore(db, system.FixedClock{At: fixedNow})
}

func writeCtx(requestID string) ops.WriteContext {
	return ops.WriteContext{
		RequestID: requestID,
		Actor:     ops.Actor{Type: "user", EntryPoint: "cli"},
	}
}

func mustCreateTask(t *testing.T, store *ops.Store, in ops.CreateTaskInput) *ops.Task {
	t.Helper()
	result, err := store.CreateTask(context.Background(), writeCtx(system.NewID("req")), in)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	task, ok := result.Data.(*ops.Task)
	if !ok {
		t.Fatalf("create task returned %T", result.Data)
	}
	return task
}

func appErrCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	app, ok := err.(*protocol.AppError)
	if !ok {
		t.Fatalf("expected *protocol.AppError, got %T: %v", err, err)
	}
	return app.Code
}

// ---------------------------------------------------------------------------
// Migrations and schema
// ---------------------------------------------------------------------------

func TestMigrationsAreIdempotent(t *testing.T) {
	store := newTestStore(t)
	migrations, _ := sqlite.LoadMigrations(mycontext.Migrations, mycontext.MigrationsDirOps)

	status, err := sqlite.Migrate(context.Background(), store.DB(), migrations, "test")
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(status.Pending) != 0 {
		t.Fatalf("expected no pending migrations, got %d", len(status.Pending))
	}
	if status.CurrentVersion != status.TargetVersion {
		t.Fatalf("current %d != target %d", status.CurrentVersion, status.TargetVersion)
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	store := newTestStore(t)
	_, err := store.CreateTask(context.Background(), writeCtx("req_fk"), ops.CreateTaskInput{
		Title:     "orphan",
		ProjectID: "proj_does_not_exist",
	})
	if code := appErrCode(t, err); code != protocol.CodeNotFound {
		t.Fatalf("expected NOT_FOUND for a missing project, got %s", code)
	}
}

// ---------------------------------------------------------------------------
// Optimistic concurrency (§14.2)
// ---------------------------------------------------------------------------

func TestStaleVersionIsRejected(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task := mustCreateTask(t, store, ops.CreateTaskInput{Title: "concurrency"})

	newTitle := "first writer"
	if _, err := store.UpdateTask(ctx, writeCtx("req_a"), ops.UpdateTaskInput{
		TaskID: task.ID, ExpectedVersion: task.Version, Title: &newTitle,
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}

	// The second writer still holds version 1: it must lose, not overwrite.
	otherTitle := "second writer"
	_, err := store.UpdateTask(ctx, writeCtx("req_b"), ops.UpdateTaskInput{
		TaskID: task.ID, ExpectedVersion: task.Version, Title: &otherTitle,
	})
	if code := appErrCode(t, err); code != protocol.CodeVersionConflict {
		t.Fatalf("expected VERSION_CONFLICT, got %s", code)
	}

	current, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if current.Title != "first writer" {
		t.Fatalf("last-write-wins leaked through: title is %q", current.Title)
	}
}

// ---------------------------------------------------------------------------
// Idempotency (§14.1)
// ---------------------------------------------------------------------------

func TestSameRequestIDReplaysInsteadOfDuplicating(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	in := ops.CreateTaskInput{Title: "retry me", Importance: "P1"}

	first, err := store.CreateTask(ctx, writeCtx("req_retry"), in)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := store.CreateTask(ctx, writeCtx("req_retry"), in)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !second.Replayed {
		t.Fatal("expected the retry to be reported as a replay")
	}

	tasks, err := store.ListTasks(ctx, ops.TaskFilter{Search: "retry me"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("retry created %d tasks, want 1", len(tasks))
	}
	if tasks[0].ID != first.Data.(*ops.Task).ID {
		t.Fatal("replay returned a different task id")
	}
}

func TestSameRequestIDWithDifferentPayloadConflicts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, err := store.CreateTask(ctx, writeCtx("req_dup"), ops.CreateTaskInput{Title: "one"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := store.CreateTask(ctx, writeCtx("req_dup"), ops.CreateTaskInput{Title: "two"})
	if code := appErrCode(t, err); code != protocol.CodeIdempotency {
		t.Fatalf("expected IDEMPOTENCY_CONFLICT, got %s", code)
	}
}

func TestDryRunCommitsNothingAndLeavesRequestIDUsable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	wc := writeCtx("req_dry")
	wc.DryRun = true
	if _, err := store.CreateTask(ctx, wc, ops.CreateTaskInput{Title: "not persisted"}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	tasks, _ := store.ListTasks(ctx, ops.TaskFilter{Search: "not persisted"})
	if len(tasks) != 0 {
		t.Fatalf("dry run persisted %d tasks", len(tasks))
	}

	// The same request id must still work for the real attempt.
	if _, err := store.CreateTask(ctx, writeCtx("req_dry"), ops.CreateTaskInput{Title: "not persisted"}); err != nil {
		t.Fatalf("real attempt after dry run: %v", err)
	}
	tasks, _ = store.ListTasks(ctx, ops.TaskFilter{Search: "not persisted"})
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task after the real attempt, got %d", len(tasks))
	}
}

// ---------------------------------------------------------------------------
// Rescheduling never loses history (§7.4)
// ---------------------------------------------------------------------------

func TestRescheduleSupersedesRatherThanOverwrites(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task := mustCreateTask(t, store, ops.CreateTaskInput{
		Title: "国金证券物业事项", PlannedDate: today, EstimateMinutes: 60,
	})

	result, err := store.RescheduleTask(ctx, writeCtx("req_resched"), ops.RescheduleInput{
		TaskID: task.ID, ExpectedVersion: task.Version, NewDate: "2026-08-25",
	})
	if err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	moved := result.Data.(*ops.Task)
	if moved.Schedule == nil || moved.Schedule.PlannedDate != "2026-08-25" {
		t.Fatalf("task did not move: %+v", moved.Schedule)
	}
	if moved.Version != task.Version+1 {
		t.Fatalf("version %d, want %d", moved.Version, task.Version+1)
	}

	// The old plan survives, superseded and pointing at its replacement.
	rows, err := store.DB().SQL().QueryContext(ctx, `
        SELECT planned_date, status, superseded_by FROM task_schedules
         WHERE task_id = ? ORDER BY created_at`, task.ID)
	if err != nil {
		t.Fatalf("query schedules: %v", err)
	}
	defer rows.Close()

	type row struct {
		date, status string
		superseded   *string
	}
	var history []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.date, &r.status, &r.superseded); err != nil {
			t.Fatalf("scan: %v", err)
		}
		history = append(history, r)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 schedule rows, got %d", len(history))
	}
	if history[0].date != today || history[0].status != "superseded" {
		t.Fatalf("old plan was not preserved: %+v", history[0])
	}
	if history[0].superseded == nil {
		t.Fatal("old plan does not point at its replacement")
	}
	if history[1].date != "2026-08-25" || history[1].status != "active" {
		t.Fatalf("new plan is wrong: %+v", history[1])
	}

	events, err := store.ListEvents(ctx, ops.EventFilter{EntityID: task.ID, EventType: "rescheduled"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 rescheduled event, got %d", len(events))
	}
}

func TestOnlyOneActivePlanPerTask(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task := mustCreateTask(t, store, ops.CreateTaskInput{Title: "one plan", PlannedDate: today})

	for i, date := range []string{"2026-08-22", "2026-08-23", "2026-08-24"} {
		current, err := store.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if _, err := store.RescheduleTask(ctx, writeCtx(system.NewID("req")), ops.RescheduleInput{
			TaskID: task.ID, ExpectedVersion: current.Version, NewDate: date,
		}); err != nil {
			t.Fatalf("reschedule %d: %v", i, err)
		}
	}
	var active int
	if err := store.DB().SQL().QueryRowContext(ctx, `
        SELECT COUNT(*) FROM task_schedules WHERE task_id = ? AND status = 'active'`,
		task.ID).Scan(&active); err != nil {
		t.Fatalf("count: %v", err)
	}
	if active != 1 {
		t.Fatalf("expected exactly 1 active plan, got %d", active)
	}
}

// ---------------------------------------------------------------------------
// Nothing important disappears silently (§15, success criteria 3)
// ---------------------------------------------------------------------------

func TestSetReviewRemovesFromTodayButNotFromSight(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task := mustCreateTask(t, store, ops.CreateTaskInput{
		Title: "park me", Importance: "P1", PlannedDate: today, EstimateMinutes: 30,
	})

	if _, err := store.SetTaskReview(ctx, writeCtx("req_review"), ops.SetReviewInput{
		TaskID: task.ID, ExpectedVersion: task.Version,
		ReviewDate: today, Status: "waiting", WaitingFor: "对方回复",
	}); err != nil {
		t.Fatalf("set review: %v", err)
	}

	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, e := range status.TodayAgenda {
		if e.TaskID == task.ID && e.Reason == "scheduled" {
			t.Fatal("parked task is still on today's plan")
		}
	}
	found := false
	for _, e := range status.ReviewDue {
		if e.TaskID == task.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("parked task vanished: it is not in review_due either")
	}
}

func TestImportantUnscheduledTaskIsSurfaced(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task := mustCreateTask(t, store, ops.CreateTaskInput{Title: "P0 with no commitment", Importance: "P0"})

	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	found := false
	for _, e := range status.UnscheduledImportant {
		if e.TaskID == task.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("a P0 task with no plan, deadline or review is not surfaced")
	}
	issue := false
	for _, q := range status.QualityIssues {
		if q.EntityID == task.ID && q.Issue == "important_without_commitment" {
			issue = true
		}
	}
	if !issue {
		t.Fatal("expected an important_without_commitment data quality issue")
	}
}

func TestPausingAProjectRequiresAReviewDate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	created, err := store.CreateProject(ctx, writeCtx("req_proj"), ops.CreateProjectInput{
		Name: "pause me", Status: "active",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	project := created.Data.(*ops.Project)

	paused := "paused"
	_, err = store.UpdateProject(ctx, writeCtx("req_pause"), ops.UpdateProjectInput{
		ProjectID: project.ID, ExpectedVersion: project.Version, Status: &paused,
	})
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT when pausing without a review date, got %s", code)
	}

	review := "2026-09-01"
	if _, err := store.UpdateProject(ctx, writeCtx("req_pause2"), ops.UpdateProjectInput{
		ProjectID: project.ID, ExpectedVersion: project.Version,
		Status: &paused, NextReviewAt: &review,
	}); err != nil {
		t.Fatalf("pause with review date: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Capacity and overload are arithmetic, not judgement (§7.3)
// ---------------------------------------------------------------------------

func TestOverloadIsReportedAsFactsOnly(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, err := store.SetCapacity(ctx, writeCtx("req_cap"), ops.SetCapacityInput{
		Date: today, AvailableMinutes: 240,
	}); err != nil {
		t.Fatalf("set capacity: %v", err)
	}
	for _, minutes := range []int{90, 180, 120} {
		mustCreateTask(t, store, ops.CreateTaskInput{
			Title: "load", PlannedDate: today, EstimateMinutes: minutes,
		})
	}

	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	load := status.TodayLoad
	if load.PlannedMinutes != 390 {
		t.Fatalf("planned minutes %d, want 390", load.PlannedMinutes)
	}
	if load.AvailableMinutes != 240 {
		t.Fatalf("available minutes %d, want 240", load.AvailableMinutes)
	}
	if load.OverloadMinutes != 150 {
		t.Fatalf("overload %d, want 150", load.OverloadMinutes)
	}
	if load.IsDefaultCapacity {
		t.Fatal("declared capacity was reported as a default")
	}
	if load.TaskCount != 3 {
		t.Fatalf("task count %d, want 3", load.TaskCount)
	}
}

func TestDefaultCapacityIsLabelled(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, ops.CreateTaskInput{
		Title: "no declared capacity", PlannedDate: today, EstimateMinutes: 30,
	})

	day, err := store.Day(ctx, today)
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if !day.Load.IsDefaultCapacity {
		t.Fatal("a day with no declared capacity must be labelled as using the default")
	}
	if day.Load.AvailableMinutes != 240 {
		t.Fatalf("weekday default %d, want 240", day.Load.AvailableMinutes)
	}
}

func TestWeekendUsesTheWeekendDefault(t *testing.T) {
	store := newTestStore(t)
	// 2026-08-22 is a Saturday.
	day, err := store.Day(context.Background(), "2026-08-22")
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if day.Load.AvailableMinutes != 120 {
		t.Fatalf("weekend default %d, want 120", day.Load.AvailableMinutes)
	}
}

// ---------------------------------------------------------------------------
// Input validation happens before SQL
// ---------------------------------------------------------------------------

func TestInvalidEnumsAndDatesAreRejected(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		in   ops.CreateTaskInput
	}{
		{"missing title", ops.CreateTaskInput{}},
		{"bad importance", ops.CreateTaskInput{Title: "x", Importance: "critical"}},
		{"bad status", ops.CreateTaskInput{Title: "x", Status: "frozen"}},
		{"bad planned date", ops.CreateTaskInput{Title: "x", PlannedDate: "2026/08/21"}},
		{"bad hard due", ops.CreateTaskInput{Title: "x", HardDueAt: "2026-08-21"}},
		{"bad time slot", ops.CreateTaskInput{Title: "x", PlannedDate: today, TimeSlot: "night"}},
		{"slot without date", ops.CreateTaskInput{Title: "x", TimeSlot: "morning"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.CreateTask(ctx, writeCtx(system.NewID("req")), tc.in)
			if code := appErrCode(t, err); code != protocol.CodeBadInput {
				t.Fatalf("expected BAD_INPUT, got %s", code)
			}
		})
	}
}

func TestChangingAHardDeadlineRequiresAReason(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task := mustCreateTask(t, store, ops.CreateTaskInput{Title: "deadline"})

	due := "2026-09-03T18:00:00+08:00"
	_, err := store.UpdateTask(ctx, writeCtx("req_due"), ops.UpdateTaskInput{
		TaskID: task.ID, ExpectedVersion: task.Version, HardDueAt: &due,
	})
	if code := appErrCode(t, err); code != protocol.CodeForbidden {
		t.Fatalf("expected CONFIRMATION_REQUIRED, got %s", code)
	}

	wc := writeCtx("req_due2")
	wc.Reason = "主办方公布了截止日期"
	if _, err := store.UpdateTask(ctx, wc, ops.UpdateTaskInput{
		TaskID: task.ID, ExpectedVersion: task.Version, HardDueAt: &due,
	}); err != nil {
		t.Fatalf("update with reason: %v", err)
	}
	events, _ := store.ListEvents(ctx, ops.EventFilter{EntityID: task.ID, EventType: "deadline_changed"})
	if len(events) != 1 {
		t.Fatalf("expected 1 deadline_changed event, got %d", len(events))
	}
	if events[0].Reason == nil || *events[0].Reason == "" {
		t.Fatal("the deadline change did not record its reason")
	}
}

func TestAmbiguousSearchReturnsCandidates(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	mustCreateTask(t, store, ops.CreateTaskInput{Title: "国金证券物业费缴纳"})
	mustCreateTask(t, store, ops.CreateTaskInput{Title: "国风小游戏原型"})

	_, err := store.FindTaskByReference(ctx, "国")
	if code := appErrCode(t, err); code != protocol.CodeAmbiguous {
		t.Fatalf("expected AMBIGUOUS_MATCH, got %s", code)
	}
}

// ---------------------------------------------------------------------------
// Every mutation is audited (§15)
// ---------------------------------------------------------------------------

func TestEveryMutationWritesAnEvent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task := mustCreateTask(t, store, ops.CreateTaskInput{Title: "audited", PlannedDate: today})

	current, _ := store.GetTask(ctx, task.ID)
	if _, err := store.RescheduleTask(ctx, writeCtx("req_1"), ops.RescheduleInput{
		TaskID: task.ID, ExpectedVersion: current.Version, NewDate: "2026-08-26",
	}); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	current, _ = store.GetTask(ctx, task.ID)
	if _, err := store.CompleteTask(ctx, writeCtx("req_2"), ops.CompleteTaskInput{
		TaskID: task.ID, ExpectedVersion: current.Version,
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	events, err := store.ListEvents(ctx, ops.EventFilter{EntityID: task.ID})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	want := map[string]bool{"created": false, "rescheduled": false, "completed": false}
	for _, e := range events {
		if _, ok := want[e.EventType]; ok {
			want[e.EventType] = true
		}
		if e.RequestID == nil || *e.RequestID == "" {
			t.Fatalf("event %s has no request id", e.EventType)
		}
	}
	for eventType, seen := range want {
		if !seen {
			t.Fatalf("no %s event was recorded", eventType)
		}
	}
}

func TestCompletingATaskClosesItsPlan(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task := mustCreateTask(t, store, ops.CreateTaskInput{Title: "finish", PlannedDate: today})

	if _, err := store.CompleteTask(ctx, writeCtx("req_done"), ops.CompleteTaskInput{
		TaskID: task.ID, ExpectedVersion: task.Version,
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	done, _ := store.GetTask(ctx, task.ID)
	if done.Status != ops.TaskDone {
		t.Fatalf("status %s, want done", done.Status)
	}
	if done.CompletedAt == nil {
		t.Fatal("completed_at was not set")
	}
	if done.Schedule != nil {
		t.Fatal("the active plan should have been closed out")
	}
	day, _ := store.Day(ctx, today)
	for _, e := range day.Entries {
		if e.TaskID == task.ID {
			t.Fatal("a completed task is still on the day board")
		}
	}
}
