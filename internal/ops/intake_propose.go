package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// The proposal contract (design §6 "Proposal DTO 与有效样例").
//
// An agent may say what it thinks it found; it may not say that anything is
// true. Every path in this file ends in a *_candidates row, and nothing here
// can reach a business table - that door is ConfirmInbox's alone, and it needs
// a human-issued grant to open.
//
// Candidate and group ids come from the CALLER. That looks odd until you
// replay a request: a proposal referring to `entitygroup_account` from three
// different rows has to mean the same group on the retry as on the first
// attempt, and server-generated ids would make the same JSON produce a
// different graph each time.

// CandidateRef is one of the three reference kinds a proposal may use.
type CandidateRef struct {
	Ref     string `json:"ref"` // existing|entity_group|action_group
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	GroupID string `json:"group_id,omitempty"`
}

// CandidateSource is where a claim came from.
type CandidateSource struct {
	DocumentID string        `json:"document_id"`
	Locator    SourceLocator `json:"locator"`
}

type ProposeEntity struct {
	CandidateID   string          `json:"candidate_id"`
	GroupID       string          `json:"group_id"`
	EntityType    string          `json:"entity_type"`
	Intent        string          `json:"intent"`
	TargetID      string          `json:"target_id,omitempty"`
	TargetVersion int64           `json:"target_version,omitempty"`
	MatchBasis    json.RawMessage `json:"match_basis,omitempty"`
}

type ProposeFact struct {
	CandidateID   string          `json:"candidate_id"`
	EntityGroupID string          `json:"entity_group_id"`
	FieldName     string          `json:"field_name"`
	Value         CandidateValue  `json:"value"`
	Confidence    *float64        `json:"confidence,omitempty"`
	Source        CandidateSource `json:"source"`
}

type ProposeRelation struct {
	CandidateID  string          `json:"candidate_id"`
	From         CandidateRef    `json:"from"`
	RelationType string          `json:"relation_type"`
	To           CandidateRef    `json:"to"`
	Attributes   map[string]any  `json:"attributes,omitempty"`
	Confidence   *float64        `json:"confidence,omitempty"`
	Source       CandidateSource `json:"source"`
}

type ProposeAction struct {
	CandidateID string          `json:"candidate_id"`
	GroupID     string          `json:"group_id"`
	ActionType  string          `json:"action_type"`
	Parent      *CandidateRef   `json:"parent,omitempty"`
	Subject     *CandidateRef   `json:"subject,omitempty"`
	Draft       map[string]any  `json:"draft"`
	Source      CandidateSource `json:"source"`
}

// ProposeInput is the payload of `inbox.propose`.
type ProposeInput struct {
	SchemaVersion   int               `json:"schema_version"`
	InboxID         string            `json:"inbox_id"`
	ExpectedVersion int64             `json:"expected_version"`
	LogicalRunKey   string            `json:"logical_run_key"`
	DocumentID      string            `json:"document_id,omitempty"`
	Extractor       string            `json:"extractor,omitempty"`
	Model           string            `json:"model,omitempty"`
	PromptVersion   string            `json:"prompt_version,omitempty"`
	RawResult       json.RawMessage   `json:"raw_result,omitempty"`
	Entities        []ProposeEntity   `json:"entities"`
	Facts           []ProposeFact     `json:"facts"`
	Relations       []ProposeRelation `json:"relations"`
	Actions         []ProposeAction   `json:"actions"`
}

// ProposeResult tells the caller which run its candidates belong to.
type ProposeResult struct {
	InboxID   string `json:"inbox_id"`
	RunID     string `json:"run_id"`
	AttemptNo int64  `json:"attempt_no"`
	Version   int64  `json:"inbox_version"`
	Entities  int    `json:"entity_count"`
	Facts     int    `json:"fact_count"`
	Relations int    `json:"relation_count"`
	Actions   int    `json:"action_count"`
}

// Propose records one extraction run and its candidates.
//
// The whole proposal lands in one transaction, and every reference is resolved
// before the first insert. A half-written proposal would be worse than none:
// the review screen would show facts about an object whose own candidate row
// never arrived, and the user would have no way to tell.
func (s *Store) Propose(ctx context.Context, wc WriteContext, layout system.Layout,
	in ProposeInput) (*Result, error) {

	if err := in.validateShape(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "inbox.propose", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		inbox, err := loadInboxItem(ctx, tx, in.InboxID)
		if err != nil {
			return nil, err
		}
		if inbox.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("inbox item", in.ExpectedVersion, inbox.Version)
		}
		if inbox.Status == "confirmed" || inbox.Status == "archived" {
			return nil, protocol.BadInput("inbox item %s is already %s", inbox.ID, inbox.Status)
		}
		documentID := in.DocumentID
		if documentID == "" {
			documentID = inbox.DocumentID
		}
		if documentID == "" {
			return nil, protocol.BadInput("this inbox item has no document to cite")
		}

		// Locators are checked against the sealed bytes here, at proposal time,
		// so a bad offset is reported while the extractor can still fix it -
		// and again at confirm, because the two are separated in time.
		original, err := loadOriginalBytes(ctx, tx, layout, documentID)
		if err != nil {
			return nil, err
		}
		if err := in.verifyLocators(original); err != nil {
			return nil, err
		}
		if err := in.validateGraph(); err != nil {
			return nil, err
		}

		runID, attempt, err := openExtractionRun(ctx, tx, wc, now, in, original)
		if err != nil {
			return nil, err
		}
		if err := in.insertCandidates(ctx, tx, now, runID, documentID); err != nil {
			return nil, err
		}

		version, err := setInboxStatus(ctx, tx, in.InboxID, inbox.Version, "reviewing", now)
		if err != nil {
			return nil, err
		}
		result := ProposeResult{
			InboxID: in.InboxID, RunID: runID, AttemptNo: attempt, Version: version,
			Entities: len(in.Entities), Facts: len(in.Facts),
			Relations: len(in.Relations), Actions: len(in.Actions),
		}
		return &Result{
			Data: result,
			Changes: []protocol.Change{{EntityType: "inbox", EntityID: in.InboxID,
				EventType: "updated", Version: version, ProjectionKeys: []string{"inbox"}}},
		}, nil
	})
}

// ---------------------------------------------------------------------------
// shape validation: everything checkable without touching the database
// ---------------------------------------------------------------------------

func (in *ProposeInput) validateShape() error {
	if in.SchemaVersion != 0 && in.SchemaVersion != 1 {
		return protocol.BadInput("schema_version must be 1")
	}
	if in.InboxID == "" {
		return protocol.BadInput("inbox_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return protocol.BadInput("expected_version is required; read the inbox item first")
	}
	if in.LogicalRunKey == "" {
		in.LogicalRunKey = "proposal-v1"
	}
	if in.PromptVersion == "" {
		in.PromptVersion = "v1"
	}
	if in.Extractor == "" {
		in.Extractor = "external"
	}
	if len(in.Entities) == 0 && len(in.Actions) == 0 {
		return protocol.BadInput("a proposal must contain at least one entity or action")
	}

	seen := map[string]string{}
	claim := func(id, prefix, path string) error {
		if !strings.HasPrefix(id, prefix) {
			return protocol.Review(protocol.CodeBadInput,
				fmt.Sprintf("id %q must start with %q", id, prefix), map[string]any{"path": path})
		}
		if prev, dup := seen[id]; dup {
			return protocol.Review(protocol.CodeBadInput,
				fmt.Sprintf("id %q is used twice in this request", id),
				map[string]any{"path": path, "first_seen": prev})
		}
		seen[id] = path
		return nil
	}

	for i, e := range in.Entities {
		path := fmt.Sprintf("entities[%d]", i)
		if err := claim(e.CandidateID, "entitycand_", path+".candidate_id"); err != nil {
			return err
		}
		if err := validateEntityShape(e, path); err != nil {
			return err
		}
	}

	for i, f := range in.Facts {
		path := fmt.Sprintf("facts[%d]", i)
		if err := claim(f.CandidateID, "fact_", path+".candidate_id"); err != nil {
			return err
		}
		if err := validateFactShape(&in.Facts[i], path); err != nil {
			return err
		}
	}

	for i, r := range in.Relations {
		path := fmt.Sprintf("relations[%d]", i)
		if err := claim(r.CandidateID, "rel_", path+".candidate_id"); err != nil {
			return err
		}
		if err := validateRelationShape(r, path); err != nil {
			return err
		}
	}

	for i, a := range in.Actions {
		path := fmt.Sprintf("actions[%d]", i)
		if err := claim(a.CandidateID, "action_", path+".candidate_id"); err != nil {
			return err
		}
		if err := validateActionShape(a, path); err != nil {
			return err
		}
	}
	return nil
}

// validateReplacement runs exactly the element checks a proposal would, on the
// single element a revision carries. Sharing them is the point: a revision must
// not be a second, laxer door into the same tables.
func (in *ProposeInput) validateReplacement() error {
	for i := range in.Entities {
		if err := validateEntityShape(in.Entities[i], "replacement"); err != nil {
			return err
		}
	}
	for i := range in.Facts {
		if err := validateFactShape(&in.Facts[i], "replacement"); err != nil {
			return err
		}
	}
	for i := range in.Relations {
		if err := validateRelationShape(in.Relations[i], "replacement"); err != nil {
			return err
		}
	}
	for i := range in.Actions {
		if err := validateActionShape(in.Actions[i], "replacement"); err != nil {
			return err
		}
	}
	return nil
}

func validateEntityShape(e ProposeEntity, path string) error {
	if _, known := fieldRegistry[e.EntityType]; !known {
		return protocol.Review(protocol.CodeUnsupportedField,
			fmt.Sprintf("entity type %q is not writable in this version", e.EntityType),
			map[string]any{"path": path + ".entity_type"})
	}
	switch e.Intent {
	case "create":
		if e.TargetID != "" || e.TargetVersion != 0 {
			return protocol.Review(protocol.CodeBadInput,
				"a create candidate must not carry a target", map[string]any{"path": path + ".intent"})
		}
	case "update":
		if e.TargetID == "" || e.TargetVersion <= 0 {
			return protocol.Review(protocol.CodeBadInput,
				"an update candidate needs target_id and target_version",
				map[string]any{"path": path + ".target_id"})
		}
	case "link_existing":
		if e.TargetID == "" {
			return protocol.Review(protocol.CodeBadInput,
				"a link_existing candidate needs target_id",
				map[string]any{"path": path + ".target_id"})
		}
	default:
		return protocol.Review(protocol.CodeBadInput,
			"intent must be create|update|link_existing", map[string]any{"path": path + ".intent"})
	}
	if !strings.HasPrefix(e.GroupID, "entitygroup_") {
		return protocol.Review(protocol.CodeBadInput,
			"group_id must start with entitygroup_", map[string]any{"path": path + ".group_id"})
	}
	return nil
}

func validateFactShape(f *ProposeFact, path string) error {
	if err := f.Value.parse(path + ".value"); err != nil {
		return err
	}
	if err := f.Source.Locator.validate(path + ".source.locator"); err != nil {
		return err
	}
	if f.Confidence != nil && (*f.Confidence < 0 || *f.Confidence > 1) {
		return protocol.Review(protocol.CodeBadInput,
			"confidence must be between 0 and 1", map[string]any{"path": path + ".confidence"})
	}
	return nil
}

func validateRelationShape(r ProposeRelation, path string) error {
	if err := r.From.validate(path + ".from"); err != nil {
		return err
	}
	if err := r.To.validate(path + ".to"); err != nil {
		return err
	}
	if err := r.Source.Locator.validate(path + ".source.locator"); err != nil {
		return err
	}
	return nil
}

func validateActionShape(a ProposeAction, path string) error {
	if !strings.HasPrefix(a.GroupID, "actiongroup_") {
		return protocol.Review(protocol.CodeBadInput,
			"group_id must start with actiongroup_", map[string]any{"path": path + ".group_id"})
	}
	if err := validateActionDraft(a.ActionType, a.Draft, path+".draft"); err != nil {
		return err
	}
	if err := a.Source.Locator.validate(path + ".source.locator"); err != nil {
		return err
	}
	switch a.ActionType {
	case "project":
		if a.Parent != nil {
			return protocol.Review(protocol.CodeBadInput,
				"a project action has no parent; its business membership is a relation",
				map[string]any{"path": path + ".parent"})
		}
		if a.Subject != nil {
			return protocol.Review(protocol.CodeBadInput,
				"a project action must not carry a subject; use an advances relation",
				map[string]any{"path": path + ".subject"})
		}
	case "milestone":
		if a.Parent == nil || a.Parent.Type != "project" {
			return protocol.Review(protocol.CodeMissingField,
				"a milestone must have a project action as its parent",
				map[string]any{"path": path + ".parent"})
		}
	case "task":
		if a.Parent == nil || (a.Parent.Type != "project" && a.Parent.Type != "milestone") {
			return protocol.Review(protocol.CodeMissingField,
				"a task must have a project or milestone action as its parent",
				map[string]any{"path": path + ".parent"})
		}
	default:
		return protocol.Review(protocol.CodeUnsupportedAction,
			"action_type must be project|milestone|task", map[string]any{"path": path + ".action_type"})
	}
	if a.Parent != nil {
		if err := a.Parent.validate(path + ".parent"); err != nil {
			return err
		}
		if a.Parent.Ref != "action_group" {
			return protocol.Review(protocol.CodeBadInput,
				"an action parent must reference an action_group",
				map[string]any{"path": path + ".parent.ref"})
		}
		if a.Parent.GroupID == a.GroupID {
			return protocol.Review(protocol.CodeCandidateCycle,
				"an action cannot be its own parent", map[string]any{"path": path + ".parent"})
		}
	}
	if a.Subject != nil {
		if err := a.Subject.validate(path + ".subject"); err != nil {
			return err
		}
		if a.Subject.Ref == "action_group" {
			return protocol.Review(protocol.CodeBadInput,
				"an action subject must be a business object, not another action",
				map[string]any{"path": path + ".subject.ref"})
		}
	}
	return nil
}

func (r CandidateRef) validate(path string) error {
	switch r.Ref {
	case "existing":
		if r.ID == "" || r.GroupID != "" {
			return protocol.Review(protocol.CodeBadInput,
				"an existing reference needs id and no group_id", map[string]any{"path": path})
		}
	case "entity_group":
		if r.GroupID == "" || r.ID != "" {
			return protocol.Review(protocol.CodeBadInput,
				"an entity_group reference needs group_id and no id", map[string]any{"path": path})
		}
	case "action_group":
		if r.GroupID == "" || r.ID != "" {
			return protocol.Review(protocol.CodeBadInput,
				"an action_group reference needs group_id and no id", map[string]any{"path": path})
		}
	default:
		return protocol.Review(protocol.CodeBadInput,
			"ref must be existing|entity_group|action_group", map[string]any{"path": path})
	}
	if r.Type == "" {
		return protocol.Review(protocol.CodeBadInput, "a reference needs a type",
			map[string]any{"path": path})
	}
	return nil
}

func (in ProposeInput) verifyLocators(original []byte) error {
	for i, f := range in.Facts {
		if err := f.Source.Locator.verifyAgainst(original, fmt.Sprintf("facts[%d].source.locator", i)); err != nil {
			return err
		}
	}
	for i, r := range in.Relations {
		if err := r.Source.Locator.verifyAgainst(original, fmt.Sprintf("relations[%d].source.locator", i)); err != nil {
			return err
		}
	}
	for i, a := range in.Actions {
		if err := a.Source.Locator.verifyAgainst(original, fmt.Sprintf("actions[%d].source.locator", i)); err != nil {
			return err
		}
	}
	return nil
}

// validateGraph resolves every intra-proposal reference and checks the parts
// the registries own: which field belongs to which entity type, which triples
// have a storage, which action parents exist. It also detects a parent cycle -
// the DAG confirm executes has to be acyclic, and finding out at write time
// would mean discovering it with rows already inserted.
func (in ProposeInput) validateGraph() error {
	entityTypeOf := map[string]string{}
	for _, e := range in.Entities {
		entityTypeOf[e.GroupID] = e.EntityType
	}
	actionTypeOf := map[string]string{}
	parentOf := map[string]string{}
	for _, a := range in.Actions {
		actionTypeOf[a.GroupID] = a.ActionType
		if a.Parent != nil {
			parentOf[a.GroupID] = a.Parent.GroupID
		}
	}

	intentOf := map[string]string{}
	for _, e := range in.Entities {
		intentOf[e.GroupID] = e.Intent
	}

	for i, f := range in.Facts {
		path := fmt.Sprintf("facts[%d]", i)
		entityType, ok := entityTypeOf[f.EntityGroupID]
		if !ok {
			return protocol.Review(protocol.CodeDependencyConflict,
				fmt.Sprintf("no entity candidate declares group %s", f.EntityGroupID),
				map[string]any{"path": path + ".entity_group_id"})
		}
		if intentOf[f.EntityGroupID] == "link_existing" {
			return protocol.Review(protocol.CodeDependencyConflict,
				"a link_existing candidate proposes no field changes",
				map[string]any{"path": path + ".entity_group_id"})
		}
		def, err := lookupField(entityType, f.FieldName, path+".field_name")
		if err != nil {
			return err
		}
		if err := def.checkValue(entityType, f.FieldName, f.Value, path+".value"); err != nil {
			return err
		}
	}

	resolveType := func(ref CandidateRef, path string) (string, error) {
		switch ref.Ref {
		case "entity_group":
			declared, ok := entityTypeOf[ref.GroupID]
			if !ok {
				return "", protocol.Review(protocol.CodeDependencyConflict,
					fmt.Sprintf("no entity candidate declares group %s", ref.GroupID),
					map[string]any{"path": path})
			}
			if declared != ref.Type {
				return "", protocol.Review(protocol.CodeDependencyConflict,
					fmt.Sprintf("group %s is a %s, not a %s", ref.GroupID, declared, ref.Type),
					map[string]any{"path": path})
			}
			return declared, nil
		case "action_group":
			declared, ok := actionTypeOf[ref.GroupID]
			if !ok {
				return "", protocol.Review(protocol.CodeDependencyConflict,
					fmt.Sprintf("no action candidate declares group %s", ref.GroupID),
					map[string]any{"path": path})
			}
			// Only a project action is a stable relation endpoint: milestones
			// and tasks belong to a project, and letting them carry business
			// relations would give the same link two authorities.
			if declared != "project" {
				return "", protocol.Review(protocol.CodeUnsupportedRel,
					"only a project action can be a relation endpoint",
					map[string]any{"path": path})
			}
			if declared != ref.Type {
				return "", protocol.Review(protocol.CodeDependencyConflict,
					fmt.Sprintf("group %s is a %s, not a %s", ref.GroupID, declared, ref.Type),
					map[string]any{"path": path})
			}
			return declared, nil
		default:
			return ref.Type, nil
		}
	}

	for i, r := range in.Relations {
		path := fmt.Sprintf("relations[%d]", i)
		fromType, err := resolveType(r.From, path+".from")
		if err != nil {
			return err
		}
		toType, err := resolveType(r.To, path+".to")
		if err != nil {
			return err
		}
		spec, err := lookupRelation(fromType, r.RelationType, toType, path+".relation_type")
		if err != nil {
			return err
		}
		if _, err := spec.checkAttributes(r.Attributes, path+".attributes"); err != nil {
			return err
		}
	}

	for i, a := range in.Actions {
		path := fmt.Sprintf("actions[%d]", i)
		if a.Parent != nil {
			parentType, ok := actionTypeOf[a.Parent.GroupID]
			if !ok {
				return protocol.Review(protocol.CodeDependencyConflict,
					fmt.Sprintf("no action candidate declares group %s", a.Parent.GroupID),
					map[string]any{"path": path + ".parent.group_id"})
			}
			if parentType != a.Parent.Type {
				return protocol.Review(protocol.CodeDependencyConflict,
					fmt.Sprintf("group %s is a %s, not a %s", a.Parent.GroupID, parentType, a.Parent.Type),
					map[string]any{"path": path + ".parent.type"})
			}
		}
		if a.Subject != nil && a.Subject.Ref == "entity_group" {
			if _, ok := entityTypeOf[a.Subject.GroupID]; !ok {
				return protocol.Review(protocol.CodeDependencyConflict,
					fmt.Sprintf("no entity candidate declares group %s", a.Subject.GroupID),
					map[string]any{"path": path + ".subject.group_id"})
			}
		}
		if err := detectActionCycle(a.GroupID, parentOf, path+".parent"); err != nil {
			return err
		}
	}
	return nil
}

func detectActionCycle(start string, parentOf map[string]string, path string) error {
	seen := map[string]bool{start: true}
	for node := parentOf[start]; node != ""; node = parentOf[node] {
		if seen[node] {
			return protocol.Review(protocol.CodeCandidateCycle,
				"action parents form a cycle", map[string]any{"path": path, "group_id": node})
		}
		seen[node] = true
	}
	return nil
}

// ---------------------------------------------------------------------------
// persistence
// ---------------------------------------------------------------------------

// openExtractionRun supersedes any previous completed run for the same logical
// key and opens the next attempt. The old run and its candidates stay: a second
// reading that disagrees with the first is information, and deleting the first
// would hide that the model changed its mind.
func openExtractionRun(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	in ProposeInput, original []byte) (string, int64, error) {

	if _, err := tx.ExecContext(ctx, `
        UPDATE extraction_runs SET status = 'superseded'
         WHERE inbox_id = ? AND logical_run_key = ? AND status = 'completed'`,
		in.InboxID, in.LogicalRunKey); err != nil {
		return "", 0, err
	}
	var attempt int64
	if err := tx.QueryRowContext(ctx, `
        SELECT COALESCE(MAX(attempt_no), 0) + 1 FROM extraction_runs
         WHERE inbox_id = ? AND logical_run_key = ?`,
		in.InboxID, in.LogicalRunKey).Scan(&attempt); err != nil {
		return "", 0, err
	}

	runID := system.NewID("ext")
	ts := system.FormatTimestamp(now)
	var raw any
	if len(in.RawResult) > 0 {
		raw = string(in.RawResult)
	}
	_, err := tx.ExecContext(ctx, `
        INSERT INTO extraction_runs (id, inbox_id, logical_run_key, attempt_no, extractor,
                                     model, prompt_version, schema_version, status,
                                     raw_result_json, input_hash, started_at, completed_at)
        VALUES (?,?,?,?,?,?,?,1,'completed',?,?,?,?)`,
		runID, in.InboxID, in.LogicalRunKey, attempt, in.Extractor,
		nullString(in.Model), in.PromptVersion, raw, hashString(string(original)), ts, ts)
	return runID, attempt, err
}

func (in ProposeInput) insertCandidates(ctx context.Context, tx *sql.Tx, now time.Time,
	runID, documentID string) error {

	ts := system.FormatTimestamp(now)

	for _, e := range in.Entities {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO entity_candidate_groups (id, run_id, entity_type, created_at)
            VALUES (?,?,?,?) ON CONFLICT(id) DO NOTHING`,
			e.GroupID, runID, e.EntityType, ts); err != nil {
			return err
		}
		var matchBasis any
		if len(e.MatchBasis) > 0 {
			matchBasis = string(e.MatchBasis)
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO entity_candidates (id, group_id, run_id, entity_type, intent,
                                           target_id, target_version, match_basis_json,
                                           status, created_at)
            VALUES (?,?,?,?,?,?,?,?, 'proposed', ?)`,
			e.CandidateID, e.GroupID, runID, e.EntityType, e.Intent,
			nullString(e.TargetID), zeroAsNull(e.TargetVersion), matchBasis, ts); err != nil {
			return err
		}
	}

	for _, f := range in.Facts {
		docID := f.Source.DocumentID
		if docID == "" {
			docID = documentID
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO fact_candidates (id, run_id, entity_group_id, field_name, value_type,
                                         value_json, confidence, source_document_id,
                                         source_locator_json, status, created_at)
            VALUES (?,?,?,?,?,?,?,?,?, 'proposed', ?)`,
			f.CandidateID, runID, f.EntityGroupID, f.FieldName, f.Value.Type,
			mustJSON(f.Value), nullFloat(f.Confidence), docID,
			mustJSON(f.Source.Locator), ts); err != nil {
			return err
		}
	}

	for _, r := range in.Relations {
		docID := r.Source.DocumentID
		if docID == "" {
			docID = documentID
		}
		attrs := "{}"
		if len(r.Attributes) > 0 {
			attrs = mustJSON(r.Attributes)
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO relation_candidates (
                id, run_id,
                from_ref_type, from_type, from_id, from_entity_group_id, from_action_group_id,
                relation_type,
                to_ref_type, to_type, to_id, to_entity_group_id, to_action_group_id,
                confidence, attributes_json, source_document_id, source_locator_json,
                status, created_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, 'proposed', ?)`,
			r.CandidateID, runID,
			r.From.Ref, r.From.Type, nullString(r.From.ID),
			nullString(refEntityGroup(r.From)), nullString(refActionGroup(r.From)),
			r.RelationType,
			r.To.Ref, r.To.Type, nullString(r.To.ID),
			nullString(refEntityGroup(r.To)), nullString(refActionGroup(r.To)),
			nullFloat(r.Confidence), attrs, docID, mustJSON(r.Source.Locator), ts); err != nil {
			return err
		}
	}

	for _, a := range in.Actions {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO action_candidate_groups (id, run_id, action_type, created_at)
            VALUES (?,?,?,?) ON CONFLICT(id) DO NOTHING`,
			a.GroupID, runID, a.ActionType, ts); err != nil {
			return err
		}
		docID := a.Source.DocumentID
		if docID == "" {
			docID = documentID
		}
		var parentGroup, subjectType, subjectID, subjectGroup any
		if a.Parent != nil {
			parentGroup = a.Parent.GroupID
		}
		if a.Subject != nil {
			if a.Subject.Ref == "existing" {
				subjectType, subjectID = a.Subject.Type, a.Subject.ID
			} else {
				subjectGroup = a.Subject.GroupID
			}
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO action_candidates (id, group_id, run_id, action_type,
                                           parent_action_group_id, subject_type, subject_id,
                                           subject_entity_group_id, draft_json,
                                           source_document_id, source_locator_json,
                                           status, created_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?, 'proposed', ?)`,
			a.CandidateID, a.GroupID, runID, a.ActionType, parentGroup,
			subjectType, subjectID, subjectGroup, mustJSON(a.Draft),
			docID, mustJSON(a.Source.Locator), ts); err != nil {
			return err
		}
	}
	return nil
}

func refEntityGroup(r CandidateRef) string {
	if r.Ref == "entity_group" {
		return r.GroupID
	}
	return ""
}

func refActionGroup(r CandidateRef) string {
	if r.Ref == "action_group" {
		return r.GroupID
	}
	return ""
}

// zeroAsNull keeps target_version NULL for create/link candidates, which is
// what the table's CHECK constraints require of them.
func zeroAsNull(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// ---------------------------------------------------------------------------
// inbox item helpers
// ---------------------------------------------------------------------------

// InboxItem is one captured thing waiting for a decision.
type InboxItem struct {
	ID               string  `json:"id"`
	PackageID        string  `json:"package_id"`
	DocumentID       string  `json:"document_id,omitempty"`
	CaptureKind      string  `json:"capture_kind"`
	SourceRef        *string `json:"source_ref,omitempty"`
	Title            *string `json:"title,omitempty"`
	Status           string  `json:"status"`
	AssignedRootType *string `json:"assigned_root_type,omitempty"`
	AssignedRootID   *string `json:"assigned_root_id,omitempty"`
	ErrorCode        *string `json:"error_code,omitempty"`
	ErrorMessage     *string `json:"error_message,omitempty"`
	Version          int64   `json:"version"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	ConfirmedAt      *string `json:"confirmed_at,omitempty"`
}

const inboxColumns = `
    id, package_id, COALESCE(document_id, ''), capture_kind, source_ref, title, status,
    assigned_root_type, assigned_root_id, error_code, error_message, version,
    created_at, updated_at, confirmed_at`

func scanInboxItem(row interface{ Scan(...any) error }) (*InboxItem, error) {
	var i InboxItem
	err := row.Scan(&i.ID, &i.PackageID, &i.DocumentID, &i.CaptureKind, &i.SourceRef, &i.Title,
		&i.Status, &i.AssignedRootType, &i.AssignedRootID, &i.ErrorCode, &i.ErrorMessage,
		&i.Version, &i.CreatedAt, &i.UpdatedAt, &i.ConfirmedAt)
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func loadInboxItem(ctx context.Context, tx *sql.Tx, id string) (*InboxItem, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+inboxColumns+` FROM inbox_items WHERE id = ?`, id)
	item, err := scanInboxItem(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("inbox item %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return item, nil
}

// setInboxStatus moves an item forward under optimistic concurrency and returns
// the new version, so the caller can hand the UI something it can send back.
func setInboxStatus(ctx context.Context, tx *sql.Tx, id string, expected int64,
	status string, now time.Time) (int64, error) {

	res, err := tx.ExecContext(ctx, `
        UPDATE inbox_items SET status = ?, version = version + 1, updated_at = ?
         WHERE id = ? AND version = ?`,
		status, system.FormatTimestamp(now), id, expected)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, protocol.VersionConflict("inbox item", expected, 0)
	}
	return expected + 1, nil
}
