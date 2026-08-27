package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// ConfirmInbox: the single moment a candidate becomes a fact (design §5).
//
// Everything before this file is reversible by deleting rows nobody depends on.
// This is the transaction that writes real business objects, real projects and
// real tasks - so it either does all of it or none of it. A partial confirm
// would leave an opportunity with no project, or tasks pointing at an object
// that was never created, and no amount of later repair could tell which half
// the user actually approved.
//
// Confirmation is all-or-nothing in a second sense too: every active candidate
// must carry an accept or a reject. "I'll deal with the rest later" is how a
// review queue silently becomes a pile of half-reviewed extractions, so the
// only way to change a proposal is candidate.revise, which produces a new
// version to decide on rather than leaving the old one undecided.

// ConfirmInboxInput is the payload of `inbox.confirm`.
type ConfirmInboxInput struct {
	SchemaVersion     int                 `json:"schema_version"`
	InboxID           string              `json:"inbox_id"`
	ExpectedVersion   int64               `json:"expected_version"`
	ActiveRunID       string              `json:"active_run_id"`
	ConfirmationNonce string              `json:"confirmation_nonce"`
	Decisions         []CandidateDecision `json:"decisions"`

	// SessionID is supplied by the adapter that owns the session, never by the
	// caller's JSON: a request that could name its own session could name the
	// one that was granted the confirmation.
	SessionID string `json:"-"`
}

// Materialization maps one candidate to the row it became.
type Materialization struct {
	CandidateType string `json:"candidate_type"`
	CandidateID   string `json:"candidate_id"`
	EntityType    string `json:"entity_type"`
	EntityID      string `json:"entity_id"`
	Action        string `json:"action"` // created|updated|linked
}

// ConfirmResult is what the workspace navigates with after a confirm.
type ConfirmResult struct {
	CorrelationID    string            `json:"correlation_id"`
	RootType         string            `json:"root_type"`
	RootID           string            `json:"root_id"`
	Materializations []Materialization `json:"materializations"`
	InboxVersion     int64             `json:"inbox_version"`
}

// ConfirmInbox materialises an entire reviewed extraction in one transaction.
func (s *Store) ConfirmInbox(ctx context.Context, wc WriteContext, layout system.Layout,
	in ConfirmInboxInput) (*Result, error) {

	if in.SchemaVersion != 0 && in.SchemaVersion != 1 {
		return nil, protocol.BadInput("schema_version must be 1")
	}
	if in.InboxID == "" || in.ActiveRunID == "" {
		return nil, protocol.BadInput("inbox_id and active_run_id are required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the inbox item first")
	}
	if len(in.Decisions) == 0 {
		return nil, protocol.Review(protocol.CodeIncompleteReview,
			"a confirmation must carry a decision for every candidate", nil)
	}
	if wc.CorrelationID == "" {
		wc.CorrelationID = system.NewID("corr")
	}
	wc.Confirmed = true

	return s.execute(ctx, "inbox.confirm", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		return confirmInboxTx(ctx, tx, wc, layout, now, in)
	})
}

func confirmInboxTx(ctx context.Context, tx *sql.Tx, wc WriteContext, layout system.Layout,
	now time.Time, in ConfirmInboxInput) (*Result, error) {

	inbox, err := loadInboxItem(ctx, tx, in.InboxID)
	if err != nil {
		return nil, err
	}
	if inbox.Version != in.ExpectedVersion {
		return nil, protocol.VersionConflict("inbox item", in.ExpectedVersion, inbox.Version)
	}
	if inbox.Status != "reviewing" {
		return nil, protocol.BadInput("inbox item %s is %s, not reviewing", inbox.ID, inbox.Status)
	}
	if err := requireActiveRun(ctx, tx, in.InboxID, in.ActiveRunID); err != nil {
		return nil, err
	}
	if err := consumeGrant(ctx, tx, in.ConfirmationNonce, in.SessionID, in.InboxID,
		in.ActiveRunID, in.ExpectedVersion, in.Decisions, wc.CorrelationID, now); err != nil {
		return nil, err
	}

	cands, err := loadActiveCandidates(ctx, tx, in.ActiveRunID)
	if err != nil {
		return nil, err
	}
	verdicts, err := cands.applyDecisions(in.Decisions)
	if err != nil {
		return nil, err
	}
	plan, err := cands.plan(verdicts)
	if err != nil {
		return nil, err
	}
	// The bytes are re-hashed here, not only at proposal time: a review can sit
	// open for a while, and the guarantee being made is about the moment the
	// fact is written, not the moment it was suggested.
	if err := cands.verifySources(ctx, tx, layout, verdicts); err != nil {
		return nil, err
	}

	decisionIDs, err := appendDecisions(ctx, tx, wc, now, in.Decisions)
	if err != nil {
		return nil, err
	}
	if err := cands.applyStatuses(ctx, tx, verdicts); err != nil {
		return nil, err
	}

	mats, rootType, rootID, err := plan.execute(ctx, tx, wc, now, cands, decisionIDs)
	if err != nil {
		return nil, err
	}

	version, err := confirmInboxItem(ctx, tx, in.InboxID, inbox.Version, rootType, rootID, now)
	if err != nil {
		return nil, err
	}

	result := ConfirmResult{
		CorrelationID: wc.CorrelationID, RootType: rootType, RootID: rootID,
		Materializations: mats, InboxVersion: version,
	}
	changes := []protocol.Change{{EntityType: "inbox", EntityID: in.InboxID,
		EventType: "completed", Version: version, ProjectionKeys: []string{"inbox", "cases"}}}
	for _, m := range mats {
		changes = append(changes, protocol.Change{
			EntityType: m.EntityType, EntityID: m.EntityID, EventType: m.Action,
			ProjectionKeys: []string{"cases"},
		})
	}
	return &Result{Data: result, Changes: changes}, nil
}

func requireActiveRun(ctx context.Context, tx *sql.Tx, inboxID, runID string) error {
	var status string
	err := tx.QueryRowContext(ctx, `
        SELECT status FROM extraction_runs WHERE id = ? AND inbox_id = ?`,
		runID, inboxID).Scan(&status)
	if err == sql.ErrNoRows {
		return protocol.NotFound("extraction run %s does not belong to inbox item %s", runID, inboxID)
	}
	if err != nil {
		return sqlite.Classify(err)
	}
	if status != "completed" {
		// A superseded run's candidates are a previous reading. Confirming them
		// would materialise facts the user is no longer looking at.
		return protocol.BadInput("extraction run %s is %s and can no longer be confirmed", runID, status)
	}
	return nil
}

// ---------------------------------------------------------------------------
// loading the active candidate set
// ---------------------------------------------------------------------------

type entityCand struct {
	ID, GroupID, EntityType, Intent, TargetID string
	TargetVersion                             int64
}

type factCand struct {
	ID, EntityGroupID, FieldName, DocumentID string
	Value                                    CandidateValue
	Locator                                  SourceLocator
}

type relationCand struct {
	ID                                   string
	FromRef, FromType, FromID, FromGroup string
	RelationType                         string
	ToRef, ToType, ToID, ToGroup         string
	Attributes                           map[string]any
	DocumentID                           string
	Locator                              SourceLocator
}

type actionCand struct {
	ID, GroupID, ActionType, ParentGroup     string
	SubjectType, SubjectID, SubjectEntityGrp string
	Draft                                    map[string]any
	DocumentID                               string
	Locator                                  SourceLocator
}

type candidateSet struct {
	entities  []entityCand
	facts     []factCand
	relations []relationCand
	actions   []actionCand
}

func loadActiveCandidates(ctx context.Context, tx *sql.Tx, runID string) (*candidateSet, error) {
	set := &candidateSet{}

	rows, err := tx.QueryContext(ctx, `
        SELECT id, group_id, entity_type, intent, COALESCE(target_id, ''),
               COALESCE(target_version, 0)
          FROM entity_candidates WHERE run_id = ? AND status <> 'superseded'
         ORDER BY id`, runID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	for rows.Next() {
		var e entityCand
		if err := rows.Scan(&e.ID, &e.GroupID, &e.EntityType, &e.Intent, &e.TargetID, &e.TargetVersion); err != nil {
			rows.Close()
			return nil, sqlite.Classify(err)
		}
		set.entities = append(set.entities, e)
	}
	rows.Close()

	rows, err = tx.QueryContext(ctx, `
        SELECT id, entity_group_id, field_name, value_json, source_document_id, source_locator_json
          FROM fact_candidates WHERE run_id = ? AND status <> 'superseded'
         ORDER BY id`, runID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	for rows.Next() {
		var f factCand
		var valueJSON, locatorJSON string
		if err := rows.Scan(&f.ID, &f.EntityGroupID, &f.FieldName, &valueJSON, &f.DocumentID, &locatorJSON); err != nil {
			rows.Close()
			return nil, sqlite.Classify(err)
		}
		if err := json.Unmarshal([]byte(valueJSON), &f.Value); err != nil {
			rows.Close()
			return nil, protocol.Integrity("candidate %s has an unreadable value", f.ID)
		}
		if err := json.Unmarshal([]byte(locatorJSON), &f.Locator); err != nil {
			rows.Close()
			return nil, protocol.Integrity("candidate %s has an unreadable locator", f.ID)
		}
		set.facts = append(set.facts, f)
	}
	rows.Close()

	rows, err = tx.QueryContext(ctx, `
        SELECT id, from_ref_type, from_type, COALESCE(from_id, ''),
               COALESCE(from_entity_group_id, from_action_group_id, ''),
               relation_type,
               to_ref_type, to_type, COALESCE(to_id, ''),
               COALESCE(to_entity_group_id, to_action_group_id, ''),
               attributes_json, source_document_id, source_locator_json
          FROM relation_candidates WHERE run_id = ? AND status <> 'superseded'
         ORDER BY id`, runID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	for rows.Next() {
		var r relationCand
		var attrJSON, locatorJSON string
		if err := rows.Scan(&r.ID, &r.FromRef, &r.FromType, &r.FromID, &r.FromGroup,
			&r.RelationType, &r.ToRef, &r.ToType, &r.ToID, &r.ToGroup,
			&attrJSON, &r.DocumentID, &locatorJSON); err != nil {
			rows.Close()
			return nil, sqlite.Classify(err)
		}
		_ = json.Unmarshal([]byte(attrJSON), &r.Attributes)
		if err := json.Unmarshal([]byte(locatorJSON), &r.Locator); err != nil {
			rows.Close()
			return nil, protocol.Integrity("candidate %s has an unreadable locator", r.ID)
		}
		set.relations = append(set.relations, r)
	}
	rows.Close()

	rows, err = tx.QueryContext(ctx, `
        SELECT id, group_id, action_type, COALESCE(parent_action_group_id, ''),
               COALESCE(subject_type, ''), COALESCE(subject_id, ''),
               COALESCE(subject_entity_group_id, ''), draft_json,
               source_document_id, source_locator_json
          FROM action_candidates WHERE run_id = ? AND status <> 'superseded'
         ORDER BY id`, runID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	for rows.Next() {
		var a actionCand
		var draftJSON, locatorJSON string
		if err := rows.Scan(&a.ID, &a.GroupID, &a.ActionType, &a.ParentGroup,
			&a.SubjectType, &a.SubjectID, &a.SubjectEntityGrp, &draftJSON,
			&a.DocumentID, &locatorJSON); err != nil {
			rows.Close()
			return nil, sqlite.Classify(err)
		}
		if err := json.Unmarshal([]byte(draftJSON), &a.Draft); err != nil {
			rows.Close()
			return nil, protocol.Integrity("candidate %s has an unreadable draft", a.ID)
		}
		if err := json.Unmarshal([]byte(locatorJSON), &a.Locator); err != nil {
			rows.Close()
			return nil, protocol.Integrity("candidate %s has an unreadable locator", a.ID)
		}
		set.actions = append(set.actions, a)
	}
	rows.Close()

	return set, nil
}

// ---------------------------------------------------------------------------
// decisions
// ---------------------------------------------------------------------------

type verdictMap map[string]bool // "type:id" -> accepted

func verdictKey(candidateType, id string) string { return candidateType + ":" + id }

// applyDecisions requires an explicit accept or reject for every live
// candidate, and refuses decisions about candidates that are not on this run.
// Both halves matter: the first stops half-reviews, the second stops a stale
// review screen from confirming a run the user is no longer looking at.
func (c *candidateSet) applyDecisions(decisions []CandidateDecision) (verdictMap, error) {
	expected := map[string]bool{}
	for _, e := range c.entities {
		expected[verdictKey("entity", e.ID)] = true
	}
	for _, f := range c.facts {
		expected[verdictKey("fact", f.ID)] = true
	}
	for _, r := range c.relations {
		expected[verdictKey("relation", r.ID)] = true
	}
	for _, a := range c.actions {
		expected[verdictKey("action", a.ID)] = true
	}

	out := verdictMap{}
	for i, d := range decisions {
		key := verdictKey(d.CandidateType, d.CandidateID)
		if !expected[key] {
			return nil, protocol.Review(protocol.CodeIncompleteReview,
				fmt.Sprintf("%s %s is not an active candidate of this run", d.CandidateType, d.CandidateID),
				map[string]any{"path": fmt.Sprintf("decisions[%d]", i)})
		}
		switch d.Decision {
		case "accept":
			out[key] = true
		case "reject":
			out[key] = false
		default:
			return nil, protocol.Review(protocol.CodeBadInput,
				"decision must be accept or reject",
				map[string]any{"path": fmt.Sprintf("decisions[%d].decision", i)})
		}
		delete(expected, key)
	}
	if len(expected) > 0 {
		missing := make([]string, 0, len(expected))
		for key := range expected {
			missing = append(missing, key)
		}
		sort.Strings(missing)
		return nil, protocol.Review(protocol.CodeIncompleteReview,
			fmt.Sprintf("%d candidates still have no decision", len(missing)),
			map[string]any{"undecided": missing})
	}
	return out, nil
}

func appendDecisions(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	decisions []CandidateDecision) (map[string]string, error) {

	ids := map[string]string{}
	ts := system.FormatTimestamp(now)
	for _, d := range decisions {
		id := system.NewID("dec")
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO candidate_decisions (id, candidate_type, candidate_id, decision,
                                             reviewer_type, reviewer_id, reason,
                                             correlation_id, decided_at)
            VALUES (?,?,?,?, 'user', ?,?,?,?)`,
			id, d.CandidateType, d.CandidateID, d.Decision,
			nullString(wc.Actor.ID), nullString(d.Reason), wc.CorrelationID, ts); err != nil {
			return nil, err
		}
		ids[verdictKey(d.CandidateType, d.CandidateID)] = id
	}
	return ids, nil
}

// applyStatuses keeps the cached status column in step with the decisions that
// were just appended. Both happen in this transaction, which is what
// v_candidate_decision_drift checks: if they ever disagree, something wrote a
// status outside this path.
func (c *candidateSet) applyStatuses(ctx context.Context, tx *sql.Tx, verdicts verdictMap) error {
	update := func(table, candidateType, id string) error {
		status := "rejected"
		if verdicts[verdictKey(candidateType, id)] {
			status = "accepted"
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE "+table+" SET status = ? WHERE id = ?", status, id)
		return err
	}
	for _, e := range c.entities {
		if err := update("entity_candidates", "entity", e.ID); err != nil {
			return err
		}
	}
	for _, f := range c.facts {
		if err := update("fact_candidates", "fact", f.ID); err != nil {
			return err
		}
	}
	for _, r := range c.relations {
		if err := update("relation_candidates", "relation", r.ID); err != nil {
			return err
		}
	}
	for _, a := range c.actions {
		if err := update("action_candidates", "action", a.ID); err != nil {
			return err
		}
	}
	return nil
}

func (c *candidateSet) verifySources(ctx context.Context, tx *sql.Tx, layout system.Layout,
	verdicts verdictMap) error {

	cache := map[string][]byte{}
	originals := func(docID string) ([]byte, error) {
		if data, ok := cache[docID]; ok {
			return data, nil
		}
		data, err := loadOriginalBytes(ctx, tx, layout, docID)
		if err != nil {
			return nil, err
		}
		cache[docID] = data
		return data, nil
	}
	check := func(accepted bool, docID string, loc SourceLocator, path string) error {
		if !accepted {
			return nil
		}
		data, err := originals(docID)
		if err != nil {
			return err
		}
		return loc.verifyAgainst(data, path)
	}
	for _, f := range c.facts {
		if err := check(verdicts[verdictKey("fact", f.ID)], f.DocumentID, f.Locator, "fact:"+f.ID); err != nil {
			return err
		}
	}
	for _, r := range c.relations {
		if err := check(verdicts[verdictKey("relation", r.ID)], r.DocumentID, r.Locator, "relation:"+r.ID); err != nil {
			return err
		}
	}
	for _, a := range c.actions {
		if err := check(verdicts[verdictKey("action", a.ID)], a.DocumentID, a.Locator, "action:"+a.ID); err != nil {
			return err
		}
	}
	return nil
}

func confirmInboxItem(ctx context.Context, tx *sql.Tx, id string, expected int64,
	rootType, rootID string, now time.Time) (int64, error) {

	ts := system.FormatTimestamp(now)
	res, err := tx.ExecContext(ctx, `
        UPDATE inbox_items
           SET status = 'confirmed', confirmed_at = ?, assigned_root_type = ?,
               assigned_root_id = ?, version = version + 1, updated_at = ?
         WHERE id = ? AND version = ?`,
		ts, nullString(rootType), nullString(rootID), ts, id, expected)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, protocol.VersionConflict("inbox item", expected, 0)
	}
	return expected + 1, nil
}

// dbExecQuerier is the subset of *sql.Tx the grant check needs, so that code
// stays testable without constructing a transaction.
type dbExecQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
