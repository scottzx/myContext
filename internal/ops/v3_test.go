package ops_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/scottzx/mycontext/internal/library"
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

// The referential guards in v_biz_quality_issues skip entity types they cannot
// resolve. That list must come from the schema (v_entity_types, a list of
// literals), never from the data (SELECT DISTINCT entity_type FROM
// v_entity_index, whose rows exist only where a table is non-empty).
//
// It was briefly the latter, and the failure mode was silent: with an empty
// content_pieces table, "content_piece" was not a known type, so an edge
// pointing at a nonexistent piece was not reported at all. The check stopped
// checking on a fresh instance - precisely where a dangling reference is most
// likely and least visible. Inserting one unrelated row switched the same
// check back on, which is how it was found.
func TestDanglingReferencesAreFoundWithEmptyEntityTables(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sql := store.DB().SQL()

	var pieces int
	if err := sql.QueryRowContext(ctx, `SELECT count(*) FROM content_pieces`).Scan(&pieces); err != nil {
		t.Fatalf("count content_pieces: %v", err)
	}
	if pieces != 0 {
		t.Fatalf("this test is only meaningful against an empty table, found %d rows", pieces)
	}

	// Every type the schema declares must be resolvable with zero rows behind it.
	var known int
	if err := sql.QueryRowContext(ctx,
		`SELECT count(*) FROM v_entity_types WHERE entity_type = 'content_piece'`).Scan(&known); err != nil {
		t.Fatalf("query v_entity_types: %v", err)
	}
	if known != 1 {
		t.Fatal("content_piece is not a known entity type when its table is empty; " +
			"the guard is reading the data, not the schema")
	}

	if _, err := sql.ExecContext(ctx, `
        INSERT INTO documents (id, kind, title, lineage_id, version, created_at, updated_at)
        VALUES ('doc_probe','report','探针文档','doc_probe',1,'2026-08-26','2026-08-26')`); err != nil {
		t.Fatalf("insert document: %v", err)
	}
	if _, err := sql.ExecContext(ctx, `
        INSERT INTO context_edges (id, from_type, from_id, to_type, to_id, edge_type, created_at)
        VALUES ('ce_probe','document','doc_probe','content_piece','cp_missing','references','2026-08-26')`); err != nil {
		t.Fatalf("insert context_edge: %v", err)
	}

	var dangling int
	if err := sql.QueryRowContext(ctx,
		`SELECT count(*) FROM v_biz_quality_issues WHERE issue = 'dangling_context_edge'`).Scan(&dangling); err != nil {
		t.Fatalf("query quality issues: %v", err)
	}
	if dangling != 1 {
		t.Fatalf("a soft relation pointing at a nonexistent content_piece went unreported "+
			"(got %d rows); an empty table must not switch the guard off", dangling)
	}

	// And the guard must stay quiet when the target genuinely exists.
	if _, err := sql.ExecContext(ctx, `
        INSERT INTO channels (id, platform, name, status, version, created_at, updated_at)
        VALUES ('ch_probe','xiaohongshu','探针号','active',1,'2026-08-26','2026-08-26');
        INSERT INTO content_pieces (id, channel_id, title, status, version, created_at, updated_at)
        VALUES ('cp_missing','ch_probe','补上的内容','idea',1,'2026-08-26','2026-08-26')`); err != nil {
		t.Fatalf("insert content piece: %v", err)
	}
	if err := sql.QueryRowContext(ctx,
		`SELECT count(*) FROM v_biz_quality_issues WHERE issue = 'dangling_context_edge'`).Scan(&dangling); err != nil {
		t.Fatalf("re-query quality issues: %v", err)
	}
	if dangling != 0 {
		t.Fatalf("the edge resolves now, but it is still reported dangling (%d rows)", dangling)
	}
}

// Products are the hub the revenue-bearing lines meet at, and 006 added two
// columns (current_release_id, launch_date) after the struct was first
// written. A scanner that was not widened with the table fails only once a
// row exists, so this exercises the whole round trip rather than just the
// insert.
func TestProductRoundTripsThrough006Columns(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateProduct(ctx, writeCtx("req-prod"), ops.CreateProductInput{
		Name: "听记", Kind: "product", Status: "developing",
		Positioning: "本地录音转文字，基于 funasr", LaunchDate: "2026-07-30",
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	prod := created.Data.(*ops.Product)
	if prod.LaunchDate == nil || *prod.LaunchDate != "2026-07-30" {
		t.Fatalf("launch_date did not round trip: %v", prod.LaunchDate)
	}
	if prod.CurrentReleaseID != nil {
		t.Fatal("a new product should not claim a current release")
	}

	// The list path scans the same widened row.
	list, err := store.ListProducts(ctx, ops.ProductFilter{})
	if err != nil {
		t.Fatalf("list products: %v", err)
	}
	if len(list) != 1 || list[0].Name != "听记" {
		t.Fatalf("expected the product back, got %+v", list)
	}

	// Pointing at a release that does not exist must read as NOT_FOUND, not as
	// a foreign-key constraint error surfacing from the driver.
	missing := "rel_nope"
	_, err = store.UpdateProduct(ctx, writeCtx("req-prod-bad"), ops.UpdateProductInput{
		ProductID: prod.ID, ExpectedVersion: prod.Version, CurrentReleaseID: &missing,
	})
	if code := appErrCode(t, err); code != protocol.CodeNotFound {
		t.Fatalf("expected NOT_FOUND for an absent release, got %s", code)
	}

	// A stale version must lose, not silently overwrite.
	name := "听记 Pro"
	_, err = store.UpdateProduct(ctx, writeCtx("req-prod-stale"), ops.UpdateProductInput{
		ProductID: prod.ID, ExpectedVersion: prod.Version + 5, Name: &name,
	})
	if code := appErrCode(t, err); code != protocol.CodeVersionConflict {
		t.Fatalf("expected VERSION_CONFLICT, got %s", code)
	}
}

// The rule the whole revenue model rests on: a disagreement between the
// declared contract amount and the sum of its receivable plans is a FACT to
// report, never something to block or quietly correct. This is the same
// treatment overload gets - state it, let the user decide. A well-meaning
// validation added here would silently destroy that guarantee, so the test
// asserts the write succeeds, the declared number is untouched, and the
// quality view is the thing that speaks up.
func TestAmountMismatchIsRecordedNotRefused(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	acct, err := store.CreateAccount(ctx, writeCtx("req-acct"), ops.CreateAccountInput{
		Name: "杭州云深处科技股份有限公司", AccountType: "customer",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	acctID := acct.Data.(*ops.Account).ID

	contract, err := store.CreateContract(ctx, writeCtx("req-ctr"), ops.CreateContractInput{
		AccountID: acctID, Kind: "sales", ContractNo: "YS-2026-001",
		Name: "云深处二期服务合同", SignDate: "2026-08-01", Amount: 100000, Status: "signed",
	})
	if err != nil {
		t.Fatalf("create contract: %v", err)
	}
	ctrID := contract.Data.(*ops.Contract).ID

	// One plan for 80k against a 100k contract - deliberately inconsistent.
	if _, err := store.SetReceivablePlan(ctx, writeCtx("req-plan"), ops.SetReceivablePlanInput{
		ContractID: ctrID, Seq: 1, DueDate: "2026-09-01", Amount: 80000,
	}); err != nil {
		t.Fatalf("a plan that disagrees with the contract total must still be accepted: %v", err)
	}

	// The declared amount is the user's number and stays put.
	after, err := store.GetContract(ctx, ctrID)
	if err != nil {
		t.Fatalf("get contract: %v", err)
	}
	if after.Amount != 100000 {
		t.Fatalf("the declared contract amount was rewritten to %v; it must never be "+
			"reconciled behind the user's back", after.Amount)
	}

	// And the mismatch surfaces as a stated fact.
	var issues int
	if err := store.DB().SQL().QueryRowContext(ctx,
		`SELECT count(*) FROM v_biz_quality_issues
          WHERE entity_id = ? AND issue = 'contract_amount_plan_mismatch'`, ctrID).Scan(&issues); err != nil {
		t.Fatalf("query quality issues: %v", err)
	}
	if issues != 1 {
		t.Fatalf("the amount/plan disagreement went unreported (%d rows)", issues)
	}
}

// A contract may only descend from a won opportunity. Anything else means the
// pipeline and the revenue record disagree about whether the deal happened.
func TestContractRequiresAWonOpportunity(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	acct, err := store.CreateAccount(ctx, writeCtx("req-a"), ops.CreateAccountInput{Name: "某客户"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	acctID := acct.Data.(*ops.Account).ID

	opp, err := store.CreateOpportunity(ctx, writeCtx("req-o"), ops.CreateOpportunityInput{
		AccountID: acctID, Name: "三期扩展", Stage: "negotiation",
	})
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}
	oppID := opp.Data.(*ops.Opportunity).ID

	_, err = store.CreateContract(ctx, writeCtx("req-c"), ops.CreateContractInput{
		AccountID: acctID, OpportunityID: oppID, Name: "抢跑的合同", Amount: 1000,
	})
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("a contract under a still-negotiating opportunity should be refused, got %s", code)
	}
}

// Search has to answer two shapes of query against one Chinese corpus, and the
// split is forced by the tokenizer, not chosen for convenience: trigram finds
// 三字及以上 substrings and structurally cannot match a two-character one.
// 杨总 is exactly the shape used to refer to a key contact, so if only the
// index path worked, searching for a person by name would silently return
// nothing. Both paths are exercised here against the same document.
func TestSearchFindsChineseByBothPaths(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	added, err := store.AddDocument(ctx, writeCtx("req-doc"), ops.AddDocumentInput{
		Kind: "meeting_note", Title: "云深处 8/18 沟通纪要",
		RelPath: "2026/08/18/cap_x/original/source.md", FileRole: "original",
	})
	if err != nil {
		t.Fatalf("add document: %v", err)
	}
	docID := added.Data.(*ops.Document).ID

	// Until the body is indexed it is a maintenance item, not an error.
	missing, err := store.DocumentsNeedingIndex(ctx)
	if err != nil {
		t.Fatalf("index queue: %v", err)
	}
	if len(missing) != 1 || missing[0].DocID != docID {
		t.Fatalf("a document with an original file but no body should be queued, got %+v", missing)
	}
	if missing[0].Reason != ops.IndexReasonNotIndexed {
		t.Fatalf("reason = %q, want %q", missing[0].Reason, ops.IndexReasonNotIndexed)
	}

	body := "本次会议由杨总主持，确认了云深处 AI 转型的下一步，" +
		"并把数据标注的结算口径定在 9 月 5 日。"
	if _, err := store.IndexDocument(ctx, writeCtx("req-idx"), ops.IndexDocumentInput{
		DocumentID: docID, Body: body,
	}); err != nil {
		t.Fatalf("index document: %v", err)
	}

	// Three characters or more: the trigram index answers.
	for _, q := range []string{"云深处", "数据标注", "AI 转型"} {
		res, err := store.SearchDocuments(ctx, q, 0)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if res.Mode != ops.SearchModeIndex {
			t.Fatalf("search %q should use the index, used %q", q, res.Mode)
		}
		if len(res.Hits) != 1 {
			t.Fatalf("search %q found %d documents, want 1", q, len(res.Hits))
		}
	}

	// Two characters: the index cannot match, so the scan must.
	res, err := store.SearchDocuments(ctx, "杨总", 0)
	if err != nil {
		t.Fatalf("search 杨总: %v", err)
	}
	if res.Mode != ops.SearchModeScan {
		t.Fatalf("a two-character query must fall back to a scan, used %q", res.Mode)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("searching for 杨总 found %d documents, want 1; the short-query "+
			"fallback is how a key contact is found by name", len(res.Hits))
	}

	// A term that is genuinely absent stays absent on both paths.
	for _, q := range []string{"根本没提到的词", "阿里"} {
		res, err := store.SearchDocuments(ctx, q, 0)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if len(res.Hits) != 0 {
			t.Fatalf("search %q should find nothing, found %d", q, len(res.Hits))
		}
	}

	// Re-indexing replaces rather than accumulates.
	if _, err := store.IndexDocument(ctx, writeCtx("req-idx2"), ops.IndexDocumentInput{
		DocumentID: docID, Body: body,
	}); err != nil {
		t.Fatalf("re-index: %v", err)
	}
	res, err = store.SearchDocuments(ctx, "云深处", 0)
	if err != nil {
		t.Fatalf("search after re-index: %v", err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("re-indexing duplicated the document: %d hits", len(res.Hits))
	}
}

// The Library's crash recovery is only as good as the journal underneath it.
// internal/library proves the six-row matrix against a fake; this proves the
// real ops.db implementation honours the same contract, including the two
// properties recovery depends on: re-staging and re-sealing are safe to
// repeat, and a request id maps back to the package it already produced.
func TestLibraryJournalSurvivesRepeatedCalls(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	j := store.NewLibraryJournal()

	rec := library.JournalRecord{
		PackageID:    "cap_01ABC",
		RequestID:    "req-capture-1",
		StorageDate:  "2026-08-26",
		ManifestHash: "d4735e3a265e16eee03f59718b9b5d03019c07d8b6c51f90da3a666eec13ab35",
		StagedAt:     fixedNow,
	}
	if err := j.MarkStaging(ctx, rec); err != nil {
		t.Fatalf("mark staging: %v", err)
	}
	// A crash between staging and rename means this runs twice. It must not
	// collide on the primary key.
	if err := j.MarkStaging(ctx, rec); err != nil {
		t.Fatalf("re-staging the same package must be safe: %v", err)
	}

	got, found, err := j.Lookup(ctx, rec.PackageID)
	if err != nil || !found {
		t.Fatalf("lookup after staging: found=%v err=%v", found, err)
	}
	if got.State != library.StateStaging {
		t.Fatalf("expected staging, got %q", got.State)
	}
	if got.StorageDate != "2026-08-26" {
		t.Fatalf("storage date did not round trip: %q", got.StorageDate)
	}

	// Idempotent capture: the same request must resolve to the same package
	// rather than producing a second copy of the same bytes.
	id, found, err := j.FindByRequestID(ctx, "req-capture-1")
	if err != nil || !found || id != rec.PackageID {
		t.Fatalf("request id should map back to its package, got %q found=%v err=%v", id, found, err)
	}
	if _, found, err := j.FindByRequestID(ctx, "req-never-used"); err != nil || found {
		t.Fatalf("an unused request id must report not-found, got found=%v err=%v", found, err)
	}

	if err := j.MarkSealed(ctx, rec.PackageID, fixedNow); err != nil {
		t.Fatalf("mark sealed: %v", err)
	}
	if err := j.MarkSealed(ctx, rec.PackageID, fixedNow); err != nil {
		t.Fatalf("re-sealing must be a no-op, not an error: %v", err)
	}
	got, _, err = j.Lookup(ctx, rec.PackageID)
	if err != nil {
		t.Fatalf("lookup after sealing: %v", err)
	}
	if got.State != library.StateSealed || got.SealedAt.IsZero() {
		t.Fatalf("expected a sealed record with a sealing time, got %q / %v", got.State, got.SealedAt)
	}

	// An unknown package is "no record", not an error - that distinction is a
	// whole row of the recovery matrix.
	if _, found, err := j.Lookup(ctx, "cap_nope"); err != nil || found {
		t.Fatalf("unknown package: found=%v err=%v", found, err)
	}

	ids, err := j.ListPackageIDs(ctx)
	if err != nil || len(ids) != 1 || ids[0] != rec.PackageID {
		t.Fatalf("ListPackageIDs should report the one package, got %v err=%v", ids, err)
	}
}

// receivable.list is what the UI renders on the money screen, and its job is
// to hand the frontend BOTH numbers plus the gap between them - never one
// reconciled figure. The display rule depends entirely on this view getting
// the arithmetic right, so the arithmetic is pinned here rather than left to
// a manual check.
func TestReceivableViewReportsTheGapWithoutClosingIt(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	acct, err := store.CreateAccount(ctx, writeCtx("r-acct"), ops.CreateAccountInput{
		Name: "杭州云深处科技股份有限公司", AccountType: "customer",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	acctID := acct.Data.(*ops.Account).ID

	contract, err := store.CreateContract(ctx, writeCtx("r-ctr"), ops.CreateContractInput{
		AccountID: acctID, Kind: "sales", ContractNo: "YS-2026-001",
		Name: "云深处二期服务合同", SignDate: "2026-08-01", Amount: 680000, Status: "signed",
	})
	if err != nil {
		t.Fatalf("create contract: %v", err)
	}
	ctrID := contract.Data.(*ops.Contract).ID

	// Three instalments of 200k against a declared 680k: 80k unaccounted for.
	for seq, due := range map[int]string{1: "2026-06-30", 2: "2026-08-20", 3: "2026-11-30"} {
		if _, err := store.SetReceivablePlan(ctx, writeCtx(fmt.Sprintf("r-plan-%d", seq)),
			ops.SetReceivablePlanInput{ContractID: ctrID, Seq: seq, DueDate: due, Amount: 200000}); err != nil {
			t.Fatalf("set plan %d: %v", seq, err)
		}
	}
	if _, err := store.RecordReceipt(ctx, writeCtx("r-rcpt"), ops.RecordReceiptInput{
		ContractID: ctrID, Amount: 200000, ReceivedAt: "2026-06-28T10:00:00+08:00",
	}); err != nil {
		t.Fatalf("record receipt: %v", err)
	}

	rows, err := store.ListReceivables(ctx, ops.ReceivableFilter{})
	if err != nil {
		t.Fatalf("list receivables: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one contract, got %d", len(rows))
	}
	r := rows[0]

	// Both numbers must survive to the frontend, unreconciled.
	if r.DeclaredAmount != 680000 {
		t.Fatalf("declared amount must be reported as written, got %v", r.DeclaredAmount)
	}
	if r.PlannedAmount != 600000 {
		t.Fatalf("planned total should be 600000, got %v", r.PlannedAmount)
	}
	if !r.PlanMismatch {
		t.Fatal("a 680000 contract with 600000 of instalments must be flagged as mismatched")
	}
	if r.PlanGap != -80000 {
		t.Fatalf("the gap should be reported as -80000, got %v", r.PlanGap)
	}
	if r.ReceivedAmount != 200000 || r.OutstandingAmount != 480000 {
		t.Fatalf("received/outstanding wrong: %v / %v", r.ReceivedAmount, r.OutstandingAmount)
	}
	if r.OverReceived {
		t.Fatal("receipts are well under the contract; over_received must be false")
	}

	// And the stored contract is still exactly what the user wrote.
	after, err := store.GetContract(ctx, ctrID)
	if err != nil {
		t.Fatalf("get contract: %v", err)
	}
	if after.Amount != 680000 {
		t.Fatalf("reading the view rewrote the contract to %v", after.Amount)
	}
}
