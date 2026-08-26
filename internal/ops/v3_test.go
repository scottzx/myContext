package ops_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
)

// Every list path must survive a schema that gained columns. The v3 migration
// widened projects and tasks, and a scanner that was not widened with them
// fails only once a row exists - which no test had.
func TestListPathsSurviveAPopulatedDatabase(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	area, err := store.CreateArea(ctx, writeCtx("req-area"), ops.CreateAreaInput{Name: "现金流"})
	if err != nil {
		t.Fatalf("create area: %v", err)
	}
	areaID := area.Data.(ops.Area).ID

	init, err := store.CreateInitiative(ctx, writeCtx("req-init"),
		ops.CreateInitiativeInput{AreaID: areaID, Name: "数据标注"})
	if err != nil {
		t.Fatalf("create initiative: %v", err)
	}
	initID := init.Data.(ops.Initiative).ID

	target := 35.0
	project, err := store.CreateProject(ctx, writeCtx("req-proj"), ops.CreateProjectInput{
		InitiativeID: initID, Name: "数据标注批次",
		MetricName: "标注条数", MetricUnit: "条", TargetValue: &target,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectID := project.Data.(*ops.Project).ID

	if _, err := store.CreateTask(ctx, writeCtx("req-task"), ops.CreateTaskInput{
		ProjectID: projectID, Title: "提交 5 条",
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// The regression: ListProjects scans a wider row than the struct expected.
	projects, err := store.ListProjects(ctx, ops.ProjectFilter{})
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0].Project.MetricName == nil || *projects[0].Project.MetricName != "标注条数" {
		t.Fatalf("project metric did not survive the round trip: %+v", projects[0].Project.MetricName)
	}
	if projects[0].OpenTasks != 1 {
		t.Fatalf("expected 1 open task, got %d", projects[0].OpenTasks)
	}

	if _, err := store.Tree(ctx, false); err != nil {
		t.Fatalf("tree: %v", err)
	}
	if _, err := store.ListTasks(ctx, ops.TaskFilter{}); err != nil {
		t.Fatalf("list tasks: %v", err)
	}
}

// A sprint is a project with a parent and a window, so it must reject being
// created without one - otherwise "sprint" would just be a label.
func TestSprintRequiresAParentProject(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.CreateProject(ctx, writeCtx("req-orphan-sprint"), ops.CreateProjectInput{
		Name: "本周冲刺", Kind: "sprint",
	})
	if err == nil {
		t.Fatal("expected a sprint without a parent to be rejected")
	}
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT, got %s", code)
	}
}

// A milestone is a dated checkpoint; without a date there is nothing to check.
func TestMilestoneRequiresADate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.CreateMilestone(ctx, writeCtx("req-undated-ms"), ops.CreateMilestoneInput{
		Name: "上架完成",
	})
	if err == nil {
		t.Fatal("expected a milestone without target_date to be rejected")
	}
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT, got %s", code)
	}
}

// A milestone owns the work aimed at it, and reports whether that work is
// actually there.
func TestMilestoneOwnsItsTasks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateMilestone(ctx, writeCtx("req-ms"), ops.CreateMilestoneInput{
		Name: "App 上架审核通过", TargetDate: "2026-08-30", Importance: "P0",
	})
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	msID := created.Data.(*ops.Milestone).ID

	// Before any task points at it, the milestone is flagged: a date with no
	// work behind it will not arrive by itself.
	issues, err := store.QualityIssues(ctx)
	if err != nil {
		t.Fatalf("quality: %v", err)
	}
	if !hasIssue(issues, msID, "milestone_without_tasks") {
		t.Fatal("a milestone with no task should be reported")
	}

	for i, title := range []string{"准备上架材料", "提交审核"} {
		if _, err := store.CreateTask(ctx, writeCtx(fmt.Sprintf("req-mt-%d", i)),
			ops.CreateTaskInput{Title: title, MilestoneID: msID}); err != nil {
			t.Fatalf("create task %q: %v", title, err)
		}
	}

	list, err := store.ListMilestones(ctx, ops.MilestoneFilter{})
	if err != nil {
		t.Fatalf("list milestones: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 milestone, got %d", len(list))
	}
	if list[0].TaskCount != 2 || list[0].OpenTasks != 2 {
		t.Fatalf("expected 2 tasks all open, got task_count=%d open=%d",
			list[0].TaskCount, list[0].OpenTasks)
	}

	issues, err = store.QualityIssues(ctx)
	if err != nil {
		t.Fatalf("quality: %v", err)
	}
	if hasIssue(issues, msID, "milestone_without_tasks") {
		t.Fatal("the milestone now has tasks and should no longer be flagged")
	}
}

// Moving a checkpoint is exactly how a commitment quietly evaporates, so it
// needs a stated reason - the same rule a hard deadline follows.
func TestMovingAMilestoneDateRequiresAReason(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateMilestone(ctx, writeCtx("req-ms-move"), ops.CreateMilestoneInput{
		Name: "结算确认", TargetDate: "2026-09-05",
	})
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	m := created.Data.(*ops.Milestone)

	later := "2026-09-15"
	_, err = store.UpdateMilestone(ctx, writeCtx("req-ms-move-2"), ops.UpdateMilestoneInput{
		MilestoneID: m.ID, ExpectedVersion: m.Version, TargetDate: &later,
	})
	if err == nil {
		t.Fatal("expected the undated move to be refused")
	}
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT, got %s", code)
	}

	wc := writeCtx("req-ms-move-3")
	wc.Reason = "用户最新决定:9/5 延到 9/15"
	if _, err := store.UpdateMilestone(ctx, wc, ops.UpdateMilestoneInput{
		MilestoneID: m.ID, ExpectedVersion: m.Version, TargetDate: &later,
	}); err != nil {
		t.Fatalf("dated move with a reason: %v", err)
	}
}

func hasIssue(issues []ops.QualityIssue, entityID, issue string) bool {
	for _, i := range issues {
		if i.EntityID == entityID && i.Issue == issue {
			return true
		}
	}
	return false
}

// A parked task must stay visible as parked rather than disappearing.
func TestPausedTaskStaysOpenAndIsFlaggedWithoutAReview(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateTask(ctx, writeCtx("req-paused"), ops.CreateTaskInput{
		Title: "暂缓的事", Status: "paused",
	})
	if err != nil {
		t.Fatalf("create paused task: %v", err)
	}
	task := created.Data.(*ops.Task)
	if task.Status != ops.TaskPaused {
		t.Fatalf("expected paused, got %s", task.Status)
	}

	issues, err := store.QualityIssues(ctx)
	if err != nil {
		t.Fatalf("data quality: %v", err)
	}
	var found bool
	for _, i := range issues {
		if i.EntityID == task.ID && i.Issue == "paused_without_review" {
			found = true
		}
	}
	if !found {
		t.Fatal("a paused task with no review date should be reported: nothing would bring it back")
	}
}

// Hard edges must not form a loop, or "what unblocks what" has no answer.
func TestHardDependencyCycleIsRefused(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	a, err := store.CreateTask(ctx, writeCtx("req-a"), ops.CreateTaskInput{Title: "A"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	b, err := store.CreateTask(ctx, writeCtx("req-b"), ops.CreateTaskInput{Title: "B"})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	aID := a.Data.(*ops.Task).ID
	bID := b.Data.(*ops.Task).ID

	if _, err := store.AddDependency(ctx, writeCtx("req-ab"), ops.AddDependencyInput{
		FromID: aID, ToID: bID, DependencyType: "blocks",
	}); err != nil {
		t.Fatalf("A blocks B: %v", err)
	}
	if _, err := store.AddDependency(ctx, writeCtx("req-ba"), ops.AddDependencyInput{
		FromID: bID, ToID: aID, DependencyType: "blocks",
	}); err == nil {
		t.Fatal("expected the cycle to be refused")
	}
}

// Both legacy delimiters appear in the same source database.
func TestTagsSplitOnBothDelimiters(t *testing.T) {
	got := ops.NormalizeTags([]string{"外勤|企业咨询|8/18", "goai,疆行,决策"})
	want := map[string]bool{"外勤": true, "企业咨询": true, "8/18": true,
		"goai": true, "疆行": true, "决策": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d tags, got %d: %v", len(want), len(got), got)
	}
	for _, tag := range got {
		if !want[tag] {
			t.Fatalf("unexpected tag %q in %v", tag, got)
		}
	}
}

// The doctor reports a total, and a capped list is not a total.
func TestQualityIssueCountIsNotCappedLikeTheList(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Each of these trips `important_without_commitment` on its own.
	const n = ops.ListCap + 5
	for i := 0; i < n; i++ {
		if _, err := store.CreateTask(ctx, writeCtx(fmt.Sprintf("req-q-%d", i)),
			ops.CreateTaskInput{Title: fmt.Sprintf("重要但没承诺 %d", i), Importance: "P0"}); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}

	listed, err := store.QualityIssues(ctx)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	count, err := store.QualityIssueCount(ctx)
	if err != nil {
		t.Fatalf("count issues: %v", err)
	}
	// The list stops at ListCap+1: the extra row is the "there is more" probe.
	if len(listed) > ops.ListCap+1 {
		t.Fatalf("the list should stay capped near %d, got %d", ops.ListCap, len(listed))
	}
	if count <= len(listed) {
		t.Fatalf("the count must exceed the capped list: count=%d listed=%d", count, len(listed))
	}
}

// entityTables is what turns an entity type into a table name to check a row
// against. It used to be a second, hand-kept list that had fallen behind
// validEntityType: "milestone" validated fine, then resolved to an empty table
// name and spliced `SELECT 1 FROM  WHERE id = ?` into the driver, so a legal
// call surfaced as an internal SQL syntax error instead of working. The two
// lists are now one; this covers the paths that were broken and the invariant
// that keeps them from parting again.
func TestEveryValidEntityTypeCanCarryTagsAndEdges(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ms, err := store.CreateMilestone(ctx, writeCtx("req-ms"), ops.CreateMilestoneInput{
		Name: "首版发布", TargetDate: "2026-09-30",
	})
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	msID := ms.Data.(*ops.Milestone).ID

	task, err := store.CreateTask(ctx, writeCtx("req-task"), ops.CreateTaskInput{Title: "打包"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	taskID := task.Data.(*ops.Task).ID

	// The tag path: this is the call that produced malformed SQL.
	if _, err := store.SetTags(ctx, writeCtx("req-tag"), ops.SetTagsInput{
		EntityType: "milestone", EntityID: msID, Tags: []string{"发布"},
	}); err != nil {
		t.Fatalf("tagging a milestone must work: %v", err)
	}

	// The edge path, with a milestone at each end in turn.
	if _, err := store.AddDependency(ctx, writeCtx("req-dep"), ops.AddDependencyInput{
		FromType: "task", FromID: taskID,
		ToType: "milestone", ToID: msID, DependencyType: "supports",
	}); err != nil {
		t.Fatalf("edge into a milestone must work: %v", err)
	}
	if _, err := store.AddDependency(ctx, writeCtx("req-dep2"), ops.AddDependencyInput{
		FromType: "milestone", FromID: msID,
		ToType: "task", ToID: taskID, DependencyType: "related",
	}); err != nil {
		t.Fatalf("edge out of a milestone must work: %v", err)
	}

	// A missing row must still read as NOT_FOUND, not as a driver error.
	_, err = store.SetTags(ctx, writeCtx("req-tag-missing"), ops.SetTagsInput{
		EntityType: "milestone", EntityID: "ms_does_not_exist", Tags: []string{"x"},
	})
	if code := appErrCode(t, err); code != protocol.CodeNotFound {
		t.Fatalf("expected NOT_FOUND for an absent milestone, got %s", code)
	}

	// The invariant: every type the validator accepts resolves to a table, and
	// the advertised vocabulary lists exactly those types. A new entity type
	// that only gets added to one of the three fails here.
	advertised := strings.Split(ops.EntityTypeList(), "|")
	if len(advertised) == 0 {
		t.Fatal("the entity vocabulary is empty")
	}
	for _, typ := range advertised {
		if _, err := store.SetTags(ctx, writeCtx("req-probe-"+typ), ops.SetTagsInput{
			EntityType: typ, EntityID: "id_does_not_exist", Tags: []string{"probe"},
		}); appErrCode(t, err) != protocol.CodeNotFound {
			t.Errorf("entity type %q does not resolve to a real table: %v", typ, err)
		}
	}
}
