package ops_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mycontext "github.com/scottzx/mycontext"
	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/system"
)

// The V1a acceptance seed (design §10 "ToB 种子"): one pasted piece of
// evidence becomes an account, a contact, an opportunity, a logged meeting, a
// pre-sales project, a milestone and a task - and every one of them can be
// traced back to the bytes it came from.

// newIntakeStore builds a store with a real instance layout, because capture
// writes files into the Library and confirm reads them back to re-verify quotes.
func newIntakeStore(t *testing.T) (*ops.Store, system.Layout) {
	t.Helper()
	root := t.TempDir()
	layout := system.NewLayout(root)
	for _, dir := range layout.Dirs() {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("layout: %v", err)
		}
	}
	db, err := sqlite.Open(filepath.Join(layout.Data(), "ops.db"), sqlite.Options{})
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
	return ops.NewStore(db, system.FixedClock{At: fixedNow}), layout
}

// The evidence. Offsets below are computed from this string, so editing it
// cannot silently invalidate the locators - the helper recomputes them.
const tobEvidence = `某科技公司的张老师在群里问能不能做企业内训。
他们想要一套面向业务团队的 AI 培训方案，预算大约两万。
2026-08-23 10:00 第一次线上会议，确认了课程方向和交付节奏。
下一步是在 2026-09-02 之前把方案定稿。`

func TestV1aToBSeed_EvidenceToWorkspace(t *testing.T) {
	ctx := context.Background()
	store, layout := newIntakeStore(t)

	// --- capture ---------------------------------------------------------
	captured, err := store.CaptureText(ctx, writeCtx("req_capture"), layout, ops.CaptureTextInput{
		SchemaVersion: 1,
		Title:         "群聊线索",
		SourceRef:     "https://example.invalid/thread/1",
		Text:          tobEvidence,
	})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	cap := captured.Data.(ops.CaptureTextResult)
	if cap.InboxID == "" || cap.DocumentID == "" || cap.PackageID == "" {
		t.Fatalf("capture produced an incomplete registration: %+v", cap)
	}

	// Replaying the same request must not produce a second package.
	replayed, err := store.CaptureText(ctx, writeCtx("req_capture"), layout, ops.CaptureTextInput{
		SchemaVersion: 1, Title: "群聊线索",
		SourceRef: "https://example.invalid/thread/1", Text: tobEvidence,
	})
	if err != nil {
		t.Fatalf("capture replay: %v", err)
	}
	if got := replayed.Data.(ops.CaptureTextResult); got.PackageID != cap.PackageID {
		t.Fatalf("replay created a second package: %s vs %s", got.PackageID, cap.PackageID)
	}

	// --- propose ---------------------------------------------------------
	proposal := tobProposal(t, cap.InboxID, cap.DocumentID)
	proposed, err := store.Propose(ctx, writeCtx("req_propose"), layout, proposal)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	pr := proposed.Data.(ops.ProposeResult)

	// Nothing may exist in the business tables yet. This is the headline
	// guarantee of the whole design, so it is asserted before confirm, not after.
	assertCount(t, store, "SELECT COUNT(*) FROM accounts", 0)
	assertCount(t, store, "SELECT COUNT(*) FROM opportunities", 0)
	assertCount(t, store, "SELECT COUNT(*) FROM projects", 0)
	assertCount(t, store, "SELECT COUNT(*) FROM tasks", 0)

	// --- review ----------------------------------------------------------
	detail, err := store.GetInbox(ctx, layout, cap.InboxID)
	if err != nil {
		t.Fatalf("inbox get: %v", err)
	}
	if detail.ActiveRunID != pr.RunID {
		t.Fatalf("review shows run %s, propose created %s", detail.ActiveRunID, pr.RunID)
	}
	for _, f := range detail.Facts {
		if f.Source.Quote == "" {
			t.Fatalf("fact %s has no quotable source", f.CandidateID)
		}
	}

	decisions := acceptAll(detail)
	nonce, err := store.IssueConfirmationGrant(ctx, ops.IssueGrantInput{
		SessionID: "session-1", InboxID: cap.InboxID, ActiveRunID: pr.RunID,
		ExpectedVersion: pr.Version, Decisions: decisions,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	// --- confirm ---------------------------------------------------------
	confirmed, err := store.ConfirmInbox(ctx, writeCtx("req_confirm"), layout, ops.ConfirmInboxInput{
		SchemaVersion: 1, InboxID: cap.InboxID, ExpectedVersion: pr.Version,
		ActiveRunID: pr.RunID, ConfirmationNonce: nonce, SessionID: "session-1",
		Decisions: decisions,
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	res := confirmed.Data.(ops.ConfirmResult)
	if res.RootType != "opportunity" || res.RootID == "" {
		t.Fatalf("confirm did not route to an opportunity root: %+v", res)
	}
	if len(res.Materializations) != 7 {
		t.Fatalf("expected 7 materialisations, got %d: %+v", len(res.Materializations), res.Materializations)
	}

	assertCount(t, store, "SELECT COUNT(*) FROM accounts", 1)
	assertCount(t, store, "SELECT COUNT(*) FROM contacts", 1)
	assertCount(t, store, "SELECT COUNT(*) FROM opportunities", 1)
	assertCount(t, store, "SELECT COUNT(*) FROM interactions", 1)
	assertCount(t, store, "SELECT COUNT(*) FROM projects", 1)
	assertCount(t, store, "SELECT COUNT(*) FROM milestones", 1)
	assertCount(t, store, "SELECT COUNT(*) FROM tasks", 1)
	assertCount(t, store, "SELECT COUNT(*) FROM opportunity_projects WHERE role='primary'", 1)

	// Every accepted field carries provenance, and the decision that approved it.
	assertCount(t, store,
		"SELECT COUNT(*) FROM source_attributions WHERE origin_type='evidence' AND decision_id IS NOT NULL",
		len(detail.Facts))
	// Statuses and decisions agree - the drift view is the integrity check.
	assertCount(t, store, "SELECT COUNT(*) FROM v_candidate_decision_drift", 0)
	if issues := intakeIssues(t, store); len(issues) > 0 {
		t.Fatalf("a clean confirm left quality issues: %v", issues)
	}

	// One correlation id ties the whole confirm together.
	assertCount(t, store,
		"SELECT COUNT(DISTINCT correlation_id) FROM events WHERE correlation_id = '"+res.CorrelationID+"'", 1)

	// --- workspace -------------------------------------------------------
	cases, err := store.ListCases(ctx, ops.CaseFilter{})
	if err != nil {
		t.Fatalf("case list: %v", err)
	}
	if len(cases) != 1 || cases[0].RootID != res.RootID {
		t.Fatalf("case list did not surface the new case: %+v", cases)
	}
	if cases[0].PrimaryProjectID == nil {
		t.Fatal("the case has no primary project")
	}

	detailCase, err := store.GetCase(ctx, "opportunity", res.RootID)
	if err != nil {
		t.Fatalf("case get: %v", err)
	}
	if detailCase.Facts["name"] != "AI 培训解决方案" {
		t.Fatalf("case facts lost the opportunity name: %+v", detailCase.Facts)
	}
	current := 0
	for _, e := range detailCase.Evidence {
		if e.IsCurrent {
			current++
		}
	}
	if current == 0 {
		t.Fatal("no attribution is current immediately after confirm")
	}

	timeline, err := store.GetCaseTimeline(ctx, "opportunity", res.RootID, "", 50)
	if err != nil {
		t.Fatalf("case timeline: %v", err)
	}
	if len(timeline.Items) == 0 {
		t.Fatal("the timeline is empty right after a confirm")
	}
	seen := map[string]bool{}
	for _, it := range timeline.Items {
		key := it.ItemType + ":" + it.ItemID
		if seen[key] {
			t.Fatalf("timeline repeats %s", key)
		}
		seen[key] = true
	}
	if !seen["interaction:"+interactionID(t, store)] {
		t.Fatal("the logged meeting is missing from the timeline")
	}

	next, err := store.GetCaseNextActions(ctx, "opportunity", res.RootID)
	if err != nil {
		t.Fatalf("case next actions: %v", err)
	}
	if next.OpenTaskCount != 1 || len(next.Milestones) != 1 {
		t.Fatalf("next actions did not pick up the confirmed plan: %+v", next)
	}
}

func TestConfirm_RefusesIncompleteReview(t *testing.T) {
	ctx := context.Background()
	store, layout, inboxID, runID, version, _ := seedProposal(t)

	// One decision short of the full set.
	detail, _ := store.GetInbox(ctx, layout, inboxID)
	decisions := acceptAll(detail)[1:]

	nonce, err := store.IssueConfirmationGrant(ctx, ops.IssueGrantInput{
		SessionID: "s", InboxID: inboxID, ActiveRunID: runID,
		ExpectedVersion: version, Decisions: decisions,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	_, err = store.ConfirmInbox(ctx, writeCtx("req_partial"), layout, ops.ConfirmInboxInput{
		InboxID: inboxID, ExpectedVersion: version, ActiveRunID: runID,
		ConfirmationNonce: nonce, SessionID: "s", Decisions: decisions,
	})
	if got := appErrCode(t, err); got != "INCOMPLETE_REVIEW" {
		t.Fatalf("expected INCOMPLETE_REVIEW, got %s", got)
	}
	assertCount(t, store, "SELECT COUNT(*) FROM accounts", 0)
}

func TestConfirm_RefusesWithoutGrant(t *testing.T) {
	ctx := context.Background()
	store, layout, inboxID, runID, version, _ := seedProposal(t)
	detail, _ := store.GetInbox(ctx, layout, inboxID)

	_, err := store.ConfirmInbox(ctx, writeCtx("req_nogrant"), layout, ops.ConfirmInboxInput{
		InboxID: inboxID, ExpectedVersion: version, ActiveRunID: runID,
		SessionID: "s", Decisions: acceptAll(detail),
	})
	if got := appErrCode(t, err); got != "CONFIRMATION_GRANT_INVALID" {
		t.Fatalf("expected CONFIRMATION_GRANT_INVALID, got %s", got)
	}
	assertCount(t, store, "SELECT COUNT(*) FROM opportunities", 0)
}

func TestConfirm_RefusesGrantFromAnotherSession(t *testing.T) {
	ctx := context.Background()
	store, layout, inboxID, runID, version, _ := seedProposal(t)
	detail, _ := store.GetInbox(ctx, layout, inboxID)
	decisions := acceptAll(detail)

	nonce, err := store.IssueConfirmationGrant(ctx, ops.IssueGrantInput{
		SessionID: "session-a", InboxID: inboxID, ActiveRunID: runID,
		ExpectedVersion: version, Decisions: decisions,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	_, err = store.ConfirmInbox(ctx, writeCtx("req_wrongsession"), layout, ops.ConfirmInboxInput{
		InboxID: inboxID, ExpectedVersion: version, ActiveRunID: runID,
		ConfirmationNonce: nonce, SessionID: "session-b", Decisions: decisions,
	})
	if got := appErrCode(t, err); got != "CONFIRMATION_GRANT_INVALID" {
		t.Fatalf("expected CONFIRMATION_GRANT_INVALID, got %s", got)
	}
}

func TestConfirm_RefusesWhenDecisionsChangedAfterTheGrant(t *testing.T) {
	ctx := context.Background()
	store, layout, inboxID, runID, version, _ := seedProposal(t)
	detail, _ := store.GetInbox(ctx, layout, inboxID)
	shown := acceptAll(detail)

	nonce, err := store.IssueConfirmationGrant(ctx, ops.IssueGrantInput{
		SessionID: "s", InboxID: inboxID, ActiveRunID: runID,
		ExpectedVersion: version, Decisions: shown,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	tampered := append([]ops.CandidateDecision(nil), shown...)
	tampered[0].Decision = "reject"

	_, err = store.ConfirmInbox(ctx, writeCtx("req_tampered"), layout, ops.ConfirmInboxInput{
		InboxID: inboxID, ExpectedVersion: version, ActiveRunID: runID,
		ConfirmationNonce: nonce, SessionID: "s", Decisions: tampered,
	})
	if got := appErrCode(t, err); got != "CONFIRMATION_GRANT_INVALID" {
		t.Fatalf("expected CONFIRMATION_GRANT_INVALID, got %s", got)
	}
}

func TestConfirm_RefusesProjectWithoutOpportunity(t *testing.T) {
	ctx := context.Background()
	store, layout, inboxID, runID, version, _ := seedProposal(t)
	detail, _ := store.GetInbox(ctx, layout, inboxID)

	// Keep the project, reject the relation that gives it a reason to exist.
	decisions := acceptAll(detail)
	for i := range decisions {
		if decisions[i].CandidateID == "rel_project_opp" {
			decisions[i].Decision = "reject"
		}
	}
	nonce, err := store.IssueConfirmationGrant(ctx, ops.IssueGrantInput{
		SessionID: "s", InboxID: inboxID, ActiveRunID: runID,
		ExpectedVersion: version, Decisions: decisions,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	_, err = store.ConfirmInbox(ctx, writeCtx("req_orphanproject"), layout, ops.ConfirmInboxInput{
		InboxID: inboxID, ExpectedVersion: version, ActiveRunID: runID,
		ConfirmationNonce: nonce, SessionID: "s", Decisions: decisions,
	})
	if got := appErrCode(t, err); got != "DEPENDENCY_CONFLICT" {
		t.Fatalf("expected DEPENDENCY_CONFLICT, got %s", got)
	}
	assertCount(t, store, "SELECT COUNT(*) FROM projects", 0)
	assertCount(t, store, "SELECT COUNT(*) FROM accounts", 0)
}

func TestPropose_RefusesUnknownField(t *testing.T) {
	ctx := context.Background()
	store, layout := newIntakeStore(t)
	cap := mustCapture(t, store, layout)

	in := tobProposal(t, cap.InboxID, cap.DocumentID)
	in.Facts[0].FieldName = "budget_confidence"
	_, err := store.Propose(ctx, writeCtx("req_badfield"), layout, in)
	if got := appErrCode(t, err); got != "UNSUPPORTED_FIELD" {
		t.Fatalf("expected UNSUPPORTED_FIELD, got %s", got)
	}
}

func TestPropose_RefusesUnknownRelation(t *testing.T) {
	ctx := context.Background()
	store, layout := newIntakeStore(t)
	cap := mustCapture(t, store, layout)

	in := tobProposal(t, cap.InboxID, cap.DocumentID)
	in.Relations[0].RelationType = "advances"
	_, err := store.Propose(ctx, writeCtx("req_badrel"), layout, in)
	if got := appErrCode(t, err); got != "UNSUPPORTED_RELATION" {
		t.Fatalf("expected UNSUPPORTED_RELATION, got %s", got)
	}
}

func TestPropose_RefusesTamperedLocator(t *testing.T) {
	ctx := context.Background()
	store, layout := newIntakeStore(t)
	cap := mustCapture(t, store, layout)

	in := tobProposal(t, cap.InboxID, cap.DocumentID)
	in.Facts[0].Source.Locator.EndByte += 3
	_, err := store.Propose(ctx, writeCtx("req_badloc"), layout, in)
	if got := appErrCode(t, err); got != "SOURCE_CHANGED" {
		t.Fatalf("expected SOURCE_CHANGED, got %s", got)
	}
}

func TestPropose_RefusesMilestoneWithoutTargetDate(t *testing.T) {
	ctx := context.Background()
	store, layout := newIntakeStore(t)
	cap := mustCapture(t, store, layout)

	in := tobProposal(t, cap.InboxID, cap.DocumentID)
	for i := range in.Actions {
		if in.Actions[i].ActionType == "milestone" {
			delete(in.Actions[i].Draft, "target_date")
		}
	}
	_, err := store.Propose(ctx, writeCtx("req_nodate"), layout, in)
	if got := appErrCode(t, err); got != "MISSING_REQUIRED_FIELD" {
		t.Fatalf("expected MISSING_REQUIRED_FIELD, got %s", got)
	}
}

func TestRevise_SupersedesWithoutApproving(t *testing.T) {
	ctx := context.Background()
	store, layout, inboxID, _, _, docID := seedProposal(t)

	replacement, _ := json.Marshal(map[string]any{
		"candidate_id":    "fact_opp_name_v2",
		"entity_group_id": "entitygroup_opp",
		"field_name":      "name",
		"value":           map[string]any{"type": "text", "text": "AI 培训解决方案（企业内训）"},
		"source":          candidateSource(t, docID, "AI 培训方案"),
	})
	revised, err := store.Revise(ctx, writeCtx("req_revise"), layout, ops.ReviseInput{
		SchemaVersion: 1, InboxID: inboxID, CandidateType: "fact",
		CandidateID: "fact_opp_name", Reason: "客户口径", Replacement: replacement,
	})
	if err != nil {
		t.Fatalf("revise: %v", err)
	}
	out := revised.Data.(ops.ReviseResult)
	if out.NewCandidateID != "fact_opp_name_v2" {
		t.Fatalf("revise returned %+v", out)
	}
	// The revision is recorded as a revision, NOT as a human decision.
	assertCount(t, store, "SELECT COUNT(*) FROM candidate_revisions", 1)
	assertCount(t, store, "SELECT COUNT(*) FROM candidate_decisions", 0)
	assertCount(t, store,
		"SELECT COUNT(*) FROM fact_candidates WHERE id='fact_opp_name' AND status='superseded'", 1)

	// The replacement inherits the group, so nothing that referenced the old
	// proposal needs rewriting.
	detail, err := store.GetInbox(ctx, layout, inboxID)
	if err != nil {
		t.Fatalf("inbox get: %v", err)
	}
	for _, f := range detail.Facts {
		if f.CandidateID == "fact_opp_name" {
			t.Fatal("the superseded candidate is still in the review set")
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustCapture(t *testing.T, store *ops.Store, layout system.Layout) ops.CaptureTextResult {
	t.Helper()
	out, err := store.CaptureText(context.Background(), writeCtx(system.NewID("req")), layout,
		ops.CaptureTextInput{SchemaVersion: 1, Title: "群聊线索", Text: tobEvidence})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	return out.Data.(ops.CaptureTextResult)
}

func seedProposal(t *testing.T) (*ops.Store, system.Layout, string, string, int64, string) {
	t.Helper()
	store, layout := newIntakeStore(t)
	cap := mustCapture(t, store, layout)
	out, err := store.Propose(context.Background(), writeCtx(system.NewID("req")), layout,
		tobProposal(t, cap.InboxID, cap.DocumentID))
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	pr := out.Data.(ops.ProposeResult)
	return store, layout, cap.InboxID, pr.RunID, pr.Version, cap.DocumentID
}

func acceptAll(detail *ops.InboxDetail) []ops.CandidateDecision {
	var out []ops.CandidateDecision
	for _, e := range detail.Entities {
		out = append(out, ops.CandidateDecision{CandidateType: "entity", CandidateID: e.CandidateID, Decision: "accept"})
	}
	for _, f := range detail.Facts {
		out = append(out, ops.CandidateDecision{CandidateType: "fact", CandidateID: f.CandidateID, Decision: "accept"})
	}
	for _, r := range detail.Relations {
		out = append(out, ops.CandidateDecision{CandidateType: "relation", CandidateID: r.CandidateID, Decision: "accept"})
	}
	for _, a := range detail.Actions {
		out = append(out, ops.CandidateDecision{CandidateType: "action", CandidateID: a.CandidateID, Decision: "accept"})
	}
	return out
}

func assertCount(t *testing.T, store *ops.Store, query string, want int) {
	t.Helper()
	var got int
	if err := store.DB().SQL().QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", query, got, want)
	}
}

func intakeIssues(t *testing.T, store *ops.Store) []string {
	t.Helper()
	rows, err := store.DB().SQL().Query(
		"SELECT entity_type, entity_id, issue FROM v_intake_quality_issues")
	if err != nil {
		t.Fatalf("intake issues: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, a+" "+b+" "+c)
	}
	return out
}

func interactionID(t *testing.T, store *ops.Store) string {
	t.Helper()
	var id string
	if err := store.DB().SQL().QueryRow("SELECT id FROM interactions LIMIT 1").Scan(&id); err != nil {
		t.Fatalf("interaction id: %v", err)
	}
	return id
}

// tobProposal is the design's worked example (§6), pointed at a real captured
// document. It is the fixture every intake test starts from, so a change to the
// proposal contract shows up here first.
func tobProposal(t *testing.T, inboxID, docID string) ops.ProposeInput {
	t.Helper()
	f64 := func(v float64) *float64 { return &v }
	_ = f64
	return ops.ProposeInput{
		SchemaVersion: 1, InboxID: inboxID, ExpectedVersion: 1,
		LogicalRunKey: "proposal-v1", DocumentID: docID,
		Extractor: "test", PromptVersion: "v1",
		Entities: []ops.ProposeEntity{
			{CandidateID: "entitycand_account", GroupID: "entitygroup_account", EntityType: "account", Intent: "create"},
			{CandidateID: "entitycand_contact", GroupID: "entitygroup_contact", EntityType: "contact", Intent: "create"},
			{CandidateID: "entitycand_opp", GroupID: "entitygroup_opp", EntityType: "opportunity", Intent: "create"},
			{CandidateID: "entitycand_itx", GroupID: "entitygroup_itx", EntityType: "interaction", Intent: "create"},
		},
		Facts: []ops.ProposeFact{
			fact(t, "fact_account_name", "entitygroup_account", "name",
				textValue("某科技公司"), docID, "某科技公司"),
			fact(t, "fact_contact_name", "entitygroup_contact", "name",
				textValue("张老师"), docID, "张老师"),
			fact(t, "fact_opp_name", "entitygroup_opp", "name",
				textValue("AI 培训解决方案"), docID, "AI 培训方案"),
			fact(t, "fact_opp_stage", "entitygroup_opp", "stage",
				textValue("proposal"), docID, "确认了课程方向和交付节奏"),
			fact(t, "fact_opp_amount", "entitygroup_opp", "est_amount",
				ops.CandidateValue{Type: "money", Amount: ptrFloat(20000), Currency: "CNY", Qualifier: "approx"},
				docID, "预算大约两万"),
			fact(t, "fact_itx_time", "entitygroup_itx", "occurred_at",
				ops.CandidateValue{Type: "timestamp", RFC3339: "2026-08-23T10:00:00+08:00"},
				docID, "2026-08-23 10:00"),
		},
		Relations: []ops.ProposeRelation{
			relation(t, "rel_contact_account",
				ops.CandidateRef{Ref: "entity_group", Type: "contact", GroupID: "entitygroup_contact"},
				"belongs_to",
				ops.CandidateRef{Ref: "entity_group", Type: "account", GroupID: "entitygroup_account"},
				docID, "张老师"),
			relation(t, "rel_opp_account",
				ops.CandidateRef{Ref: "entity_group", Type: "opportunity", GroupID: "entitygroup_opp"},
				"belongs_to",
				ops.CandidateRef{Ref: "entity_group", Type: "account", GroupID: "entitygroup_account"},
				docID, "某科技公司"),
			relation(t, "rel_itx_opp",
				ops.CandidateRef{Ref: "entity_group", Type: "interaction", GroupID: "entitygroup_itx"},
				"about",
				ops.CandidateRef{Ref: "entity_group", Type: "opportunity", GroupID: "entitygroup_opp"},
				docID, "第一次线上会议"),
			withAttrs(relation(t, "rel_itx_doc",
				ops.CandidateRef{Ref: "entity_group", Type: "interaction", GroupID: "entitygroup_itx"},
				"documented_by",
				ops.CandidateRef{Ref: "existing", Type: "document", ID: docID},
				docID, "第一次线上会议"), map[string]any{"role": "evidence"}),
			relation(t, "rel_project_opp",
				ops.CandidateRef{Ref: "action_group", Type: "project", GroupID: "actiongroup_project"},
				"advances",
				ops.CandidateRef{Ref: "entity_group", Type: "opportunity", GroupID: "entitygroup_opp"},
				docID, "把方案定稿"),
		},
		Actions: []ops.ProposeAction{
			action(t, "action_project", "actiongroup_project", "project", nil,
				map[string]any{"name": "AI 培训售前推进", "importance": "P1"}, docID, "把方案定稿"),
			action(t, "action_milestone", "actiongroup_milestone", "milestone",
				&ops.CandidateRef{Ref: "action_group", Type: "project", GroupID: "actiongroup_project"},
				map[string]any{"name": "方案确认", "target_date": "2026-09-02"}, docID, "2026-09-02"),
			taskAction(t, "action_task", "actiongroup_task",
				&ops.CandidateRef{Ref: "action_group", Type: "milestone", GroupID: "actiongroup_milestone"},
				&ops.CandidateRef{Ref: "entity_group", Type: "opportunity", GroupID: "entitygroup_opp"},
				map[string]any{"title": "修改 AI 培训方案", "planned_date": "2026-08-27"}, docID, "把方案定稿"),
		},
	}
}

func ptrFloat(v float64) *float64 { return &v }

func textValue(s string) ops.CandidateValue { return ops.CandidateValue{Type: "text", Text: s} }

func fact(t *testing.T, id, group, field string, value ops.CandidateValue, docID, needle string) ops.ProposeFact {
	t.Helper()
	return ops.ProposeFact{CandidateID: id, EntityGroupID: group, FieldName: field,
		Value: value, Source: candidateSource(t, docID, needle)}
}

func relation(t *testing.T, id string, from ops.CandidateRef, relType string,
	to ops.CandidateRef, docID, needle string) ops.ProposeRelation {
	t.Helper()
	return ops.ProposeRelation{CandidateID: id, From: from, RelationType: relType, To: to,
		Source: candidateSource(t, docID, needle)}
}

func withAttrs(r ops.ProposeRelation, attrs map[string]any) ops.ProposeRelation {
	r.Attributes = attrs
	return r
}

func action(t *testing.T, id, group, actionType string, parent *ops.CandidateRef,
	draft map[string]any, docID, needle string) ops.ProposeAction {
	t.Helper()
	return ops.ProposeAction{CandidateID: id, GroupID: group, ActionType: actionType,
		Parent: parent, Draft: draft, Source: candidateSource(t, docID, needle)}
}

func taskAction(t *testing.T, id, group string, parent, subject *ops.CandidateRef,
	draft map[string]any, docID, needle string) ops.ProposeAction {
	t.Helper()
	a := action(t, id, group, "task", parent, draft, docID, needle)
	a.Subject = subject
	return a
}

func candidateSource(t *testing.T, docID, needle string) ops.CandidateSource {
	t.Helper()
	start := strings.Index(tobEvidence, needle)
	if start < 0 {
		t.Fatalf("evidence does not contain %q", needle)
	}
	end := start + len(needle)
	sum := sha256.Sum256([]byte(tobEvidence[start:end]))
	return ops.CandidateSource{DocumentID: docID, Locator: ops.SourceLocator{
		Schema: 1, Type: "text", StartByte: int64(start), EndByte: int64(end),
		QuoteSHA256: hex.EncodeToString(sum[:]),
	}}
}
