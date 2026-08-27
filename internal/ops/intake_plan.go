package ops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// The confirm plan: work out everything that could refuse, BEFORE writing.
//
// Every check in this file runs against the candidate graph in memory, so a
// review that cannot succeed is reported as a structured error with nothing
// written - not discovered halfway through by a constraint violation. That
// ordering is the difference between "your project has no opportunity, here is
// which candidate is missing" and "FOREIGN KEY constraint failed".

type confirmPlan struct {
	entities  []entityCand
	actions   []actionCand
	relations []relationCand

	factsByGroup map[string][]factCand
	entityByGrp  map[string]entityCand
	actionByGrp  map[string]actionCand

	// filled during execute
	entityIDs map[string]string
	actionIDs map[string]string
}

// idPrefixFor maps a business type to the id prefix its rows already use, so
// pre-allocated ids are indistinguishable from ones the create path would have
// generated itself.
var idPrefixFor = map[string]string{
	"account": "acct", "contact": "ctc", "opportunity": "opp", "interaction": "itx",
}

func (c *candidateSet) plan(verdicts verdictMap) (*confirmPlan, error) {
	p := &confirmPlan{
		factsByGroup: map[string][]factCand{},
		entityByGrp:  map[string]entityCand{},
		actionByGrp:  map[string]actionCand{},
		entityIDs:    map[string]string{},
		actionIDs:    map[string]string{},
	}

	for _, e := range c.entities {
		if verdicts[verdictKey("entity", e.ID)] {
			p.entities = append(p.entities, e)
			p.entityByGrp[e.GroupID] = e
		}
	}
	for _, a := range c.actions {
		if verdicts[verdictKey("action", a.ID)] {
			p.actions = append(p.actions, a)
			p.actionByGrp[a.GroupID] = a
		}
	}
	for _, r := range c.relations {
		if verdicts[verdictKey("relation", r.ID)] {
			p.relations = append(p.relations, r)
		}
	}

	// A fact accepted while the object it describes was rejected has nowhere to
	// go. Naming both sides is what lets the UI highlight the actual conflict.
	for _, f := range c.facts {
		if !verdicts[verdictKey("fact", f.ID)] {
			continue
		}
		if _, ok := p.entityByGrp[f.EntityGroupID]; !ok {
			return nil, protocol.Review(protocol.CodeDependencyConflict,
				fmt.Sprintf("field %s was accepted but the object it belongs to was not", f.FieldName),
				map[string]any{"fact_candidate_id": f.ID, "entity_group_id": f.EntityGroupID})
		}
		p.factsByGroup[f.EntityGroupID] = append(p.factsByGroup[f.EntityGroupID], f)
	}

	for _, r := range p.relations {
		if err := p.requireEnd(r.FromRef, r.FromGroup, r.ID, "from"); err != nil {
			return nil, err
		}
		if err := p.requireEnd(r.ToRef, r.ToGroup, r.ID, "to"); err != nil {
			return nil, err
		}
	}
	for _, a := range p.actions {
		if a.ParentGroup != "" {
			if _, ok := p.actionByGrp[a.ParentGroup]; !ok {
				return nil, protocol.Review(protocol.CodeDependencyConflict,
					"this action was accepted but its parent was not",
					map[string]any{"action_candidate_id": a.ID, "parent_action_group_id": a.ParentGroup})
			}
		}
		if a.SubjectEntityGrp != "" {
			if _, ok := p.entityByGrp[a.SubjectEntityGrp]; !ok {
				return nil, protocol.Review(protocol.CodeDependencyConflict,
					"this action was accepted but the object it is about was not",
					map[string]any{"action_candidate_id": a.ID, "subject_entity_group_id": a.SubjectEntityGrp})
			}
		}
	}

	if err := p.checkRequiredRelations(); err != nil {
		return nil, err
	}
	if err := p.checkProjectCardinality(); err != nil {
		return nil, err
	}
	if err := p.checkRequiredFields(); err != nil {
		return nil, err
	}
	p.order()
	return p, nil
}

func (p *confirmPlan) requireEnd(refType, groupID, relationID, side string) error {
	switch refType {
	case "entity_group":
		if _, ok := p.entityByGrp[groupID]; !ok {
			return protocol.Review(protocol.CodeDependencyConflict,
				"this relation was accepted but one of its ends was not",
				map[string]any{"relation_candidate_id": relationID, "side": side, "group_id": groupID})
		}
	case "action_group":
		if _, ok := p.actionByGrp[groupID]; !ok {
			return protocol.Review(protocol.CodeDependencyConflict,
				"this relation was accepted but one of its ends was not",
				map[string]any{"relation_candidate_id": relationID, "side": side, "group_id": groupID})
		}
	}
	return nil
}

// checkRequiredRelations enforces the relations that a typed create cannot do
// without: contacts.account_id and opportunities.account_id are NOT NULL, and
// an interaction with no subject is a note about nothing.
func (p *confirmPlan) checkRequiredRelations() error {
	for _, e := range p.entities {
		if e.Intent != "create" {
			continue
		}
		for _, req := range entityCreateRelations[e.EntityType] {
			if p.findRelationFrom(e.GroupID, req.relationType, req.toType) == nil {
				return protocol.Review(protocol.CodeMissingField,
					fmt.Sprintf("a new %s needs an accepted %s %s relation",
						e.EntityType, req.relationType, req.toType),
					map[string]any{"entity_candidate_id": e.ID, "entity_group_id": e.GroupID})
			}
		}
	}
	return nil
}

// checkProjectCardinality is the V1a business rule that keeps the pipeline
// honest: exactly one opportunity per confirmed project, and no opportunity
// getting two primaries in the same confirm. Relying on the partial unique
// index instead would surface a normal review mistake as an integrity error.
func (p *confirmPlan) checkProjectCardinality() error {
	primaryOf := map[string]string{} // opportunity group -> project group
	for _, a := range p.actions {
		if a.ActionType != "project" {
			continue
		}
		var advances []relationCand
		for _, r := range p.relations {
			if r.RelationType == "advances" && r.FromRef == "action_group" && r.FromGroup == a.GroupID {
				advances = append(advances, r)
			}
		}
		if len(advances) == 0 {
			return protocol.Review(protocol.CodeDependencyConflict,
				"a confirmed project must advance exactly one opportunity",
				map[string]any{"action_candidate_id": a.ID, "action_group_id": a.GroupID})
		}
		if len(advances) > 1 {
			ids := make([]string, 0, len(advances))
			for _, r := range advances {
				ids = append(ids, r.ID)
			}
			return protocol.Review(protocol.CodeRelationCardinal,
				"a project cannot advance more than one opportunity",
				map[string]any{"action_group_id": a.GroupID, "candidate_paths": ids})
		}
		target := advances[0].ToGroup
		if target == "" {
			target = advances[0].ToID
		}
		if prev, dup := primaryOf[target]; dup {
			return protocol.Review(protocol.CodeRelationCardinal,
				"two projects were accepted as the primary project of the same opportunity",
				map[string]any{"opportunity": target, "projects": []string{prev, a.GroupID}})
		}
		primaryOf[target] = a.GroupID
	}
	return nil
}

func (p *confirmPlan) checkRequiredFields() error {
	for _, e := range p.entities {
		if e.Intent != "create" {
			continue
		}
		present := map[string]bool{}
		for _, f := range p.factsByGroup[e.GroupID] {
			present[f.FieldName] = true
		}
		for name, def := range fieldRegistry[e.EntityType] {
			if def.requiredOnCreate && !present[name] {
				return protocol.Review(protocol.CodeMissingField,
					fmt.Sprintf("a new %s needs %s", e.EntityType, name),
					map[string]any{"entity_candidate_id": e.ID, "field_name": name})
			}
		}
	}
	return nil
}

// order sorts the plan into the sequence the foreign keys demand:
// Account -> Contact/Opportunity -> Interaction, then Project -> Milestone ->
// Task. Within a rank the order is by candidate id, so a confirm of the same
// proposal produces the same ordering every time.
func (p *confirmPlan) order() {
	entityRank := map[string]int{"account": 0, "contact": 1, "opportunity": 2, "interaction": 3}
	sort.SliceStable(p.entities, func(i, j int) bool {
		ri, rj := entityRank[p.entities[i].EntityType], entityRank[p.entities[j].EntityType]
		if ri != rj {
			return ri < rj
		}
		return p.entities[i].ID < p.entities[j].ID
	})
	actionRank := map[string]int{"project": 0, "milestone": 1, "task": 2}
	sort.SliceStable(p.actions, func(i, j int) bool {
		ri, rj := actionRank[p.actions[i].ActionType], actionRank[p.actions[j].ActionType]
		if ri != rj {
			return ri < rj
		}
		return p.actions[i].ID < p.actions[j].ID
	})
}

// findRelationFrom locates one accepted relation leaving a group.
func (p *confirmPlan) findRelationFrom(groupID, relationType, toType string) *relationCand {
	for i := range p.relations {
		r := &p.relations[i]
		if r.RelationType == relationType && r.ToType == toType &&
			(r.FromGroup == groupID || r.FromID == groupID) {
			return r
		}
	}
	return nil
}

// resolveRef turns a candidate reference into the real row id. By the time it
// is called for a given relation, whatever it points at has already been
// created - that is what order() guarantees.
func (p *confirmPlan) resolveRef(refType, id, groupID string) (string, error) {
	switch refType {
	case "existing":
		return id, nil
	case "entity_group":
		resolved, ok := p.entityIDs[groupID]
		if !ok {
			return "", protocol.Internal("entity group %s was not materialised before it was referenced", groupID)
		}
		return resolved, nil
	case "action_group":
		resolved, ok := p.actionIDs[groupID]
		if !ok {
			return "", protocol.Internal("action group %s was not materialised before it was referenced", groupID)
		}
		return resolved, nil
	}
	return "", protocol.Internal("unknown reference kind %q", refType)
}

// ---------------------------------------------------------------------------
// execution
// ---------------------------------------------------------------------------

func (p *confirmPlan) execute(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	cands *candidateSet, decisionIDs map[string]string) ([]Materialization, string, string, error) {

	var mats []Materialization
	rootType, rootID := "", ""

	// Pre-allocate every create id up front, so a relation can be written into
	// the same DTO as the object it constrains rather than as a later UPDATE.
	for _, e := range p.entities {
		switch e.Intent {
		case "create":
			p.entityIDs[e.GroupID] = system.NewID(idPrefixFor[e.EntityType])
		default:
			p.entityIDs[e.GroupID] = e.TargetID
		}
	}
	for _, a := range p.actions {
		switch a.ActionType {
		case "project":
			p.actionIDs[a.GroupID] = system.NewID("proj")
		case "milestone":
			p.actionIDs[a.GroupID] = system.NewID("ms")
		case "task":
			p.actionIDs[a.GroupID] = system.NewID("task")
		}
	}

	for _, e := range p.entities {
		mat, err := p.materializeEntity(ctx, tx, wc, now, e)
		if err != nil {
			return nil, "", "", err
		}
		mats = append(mats, mat)
		if e.EntityType == "opportunity" && rootID == "" {
			rootType, rootID = "opportunity", p.entityIDs[e.GroupID]
		}
		for _, f := range p.factsByGroup[e.GroupID] {
			if err := p.recordFieldSource(ctx, tx, now, e, f, decisionIDs); err != nil {
				return nil, "", "", err
			}
		}
	}

	for _, a := range p.actions {
		mat, err := p.materializeAction(ctx, tx, wc, now, a)
		if err != nil {
			return nil, "", "", err
		}
		mats = append(mats, mat)
	}

	// Relations last: their endpoints all exist now, and the ones that are
	// stored inside a create DTO were already applied there. Writing them again
	// here would be a second authority for the same link.
	for _, r := range p.relations {
		if err := p.materializeRelation(ctx, tx, wc, now, r, decisionIDs); err != nil {
			return nil, "", "", err
		}
	}
	return mats, rootType, rootID, nil
}

func (p *confirmPlan) values(groupID string) map[string]CandidateValue {
	out := map[string]CandidateValue{}
	for _, f := range p.factsByGroup[groupID] {
		out[f.FieldName] = f.Value
	}
	return out
}

func (p *confirmPlan) materializeEntity(ctx context.Context, tx *sql.Tx, wc WriteContext,
	now time.Time, e entityCand) (Materialization, error) {

	id := p.entityIDs[e.GroupID]
	mat := Materialization{CandidateType: "entity", CandidateID: e.ID,
		EntityType: e.EntityType, EntityID: id}

	// link_existing changes nothing about the object; it only says "the thing
	// this evidence talks about is that row". Writing a version bump for it
	// would claim an edit that never happened.
	if e.Intent == "link_existing" {
		mat.Action = "linked"
		if err := markCandidateMaterialized(ctx, tx, "entity_candidates", e.ID, e.EntityType, id); err != nil {
			return mat, err
		}
		return mat, nil
	}

	vals := p.values(e.GroupID)
	var err error
	if e.Intent == "create" {
		mat.Action = "created"
		err = p.createEntity(ctx, tx, wc, now, e, id, vals)
	} else {
		mat.Action = "updated"
		err = p.updateEntity(ctx, tx, wc, now, e, vals)
	}
	if err != nil {
		return mat, err
	}
	return mat, markCandidateMaterialized(ctx, tx, "entity_candidates", e.ID, e.EntityType, id)
}

func (p *confirmPlan) createEntity(ctx context.Context, tx *sql.Tx, wc WriteContext,
	now time.Time, e entityCand, id string, vals map[string]CandidateValue) error {

	switch e.EntityType {
	case "account":
		in := CreateAccountInput{
			Name: text(vals, "name"), ShortName: text(vals, "short_name"),
			Industry: text(vals, "industry"), Region: text(vals, "region"),
			Owner: text(vals, "owner"), Note: text(vals, "note"),
		}
		if err := in.normalize(); err != nil {
			return err
		}
		_, err := createAccountTx(ctx, tx, wc, now, id, in)
		return err

	case "contact":
		accountID, err := p.relatedID(e.GroupID, "belongs_to", "account")
		if err != nil {
			return err
		}
		in := CreateContactInput{
			AccountID: accountID, Name: text(vals, "name"), Title: text(vals, "title"),
			DealRole: text(vals, "deal_role"), Phone: text(vals, "phone"),
			Email: text(vals, "email"), Wechat: text(vals, "wechat"), Note: text(vals, "note"),
		}
		if err := in.normalize(); err != nil {
			return err
		}
		_, err = createContactTx(ctx, tx, wc, now, id, in)
		return err

	case "opportunity":
		accountID, err := p.relatedID(e.GroupID, "belongs_to", "account")
		if err != nil {
			return err
		}
		contactID, _ := p.optionalRelatedID(e.GroupID, "primary_contact", "contact")
		in := CreateOpportunityInput{
			AccountID: accountID, PrimaryContactID: contactID,
			Name: text(vals, "name"), Source: text(vals, "source"),
			Stage: text(vals, "stage"), Owner: text(vals, "owner"),
			NextStep: text(vals, "next_step"), ExpectedSignDate: dateOf(vals, "expected_sign_date"),
			EstAmount: money(vals, "est_amount"), WinProbability: number(vals, "win_probability"),
		}
		if err := in.normalize(); err != nil {
			return err
		}
		_, err = createOpportunityTx(ctx, tx, wc, now, id, in)
		return err

	case "interaction":
		subjectID, err := p.relatedID(e.GroupID, "about", "opportunity")
		if err != nil {
			return err
		}
		in := LogInteractionInput{
			SubjectType: "opportunity", SubjectID: subjectID,
			OccurredAt: timestampOf(vals, "occurred_at"), Channel: text(vals, "channel"),
			Summary: text(vals, "summary"), Participants: text(vals, "participants"),
			Owner: text(vals, "owner"),
		}
		if err := in.normalize(); err != nil {
			return err
		}
		_, err = logInteractionTx(ctx, tx, wc, now, id, in)
		return err
	}
	return protocol.Review(protocol.CodeUnsupportedField,
		fmt.Sprintf("entity type %q cannot be created in this version", e.EntityType), nil)
}

func (p *confirmPlan) updateEntity(ctx context.Context, tx *sql.Tx, wc WriteContext,
	now time.Time, e entityCand, vals map[string]CandidateValue) error {

	switch e.EntityType {
	case "account":
		in := UpdateAccountInput{AccountID: e.TargetID, ExpectedVersion: e.TargetVersion,
			Name: textPtr(vals, "name"), ShortName: textPtr(vals, "short_name"),
			Industry: textPtr(vals, "industry"), Region: textPtr(vals, "region"),
			Owner: textPtr(vals, "owner"), Note: textPtr(vals, "note")}
		_, err := updateAccountTx(ctx, tx, wc, now, in)
		return err
	case "contact":
		in := UpdateContactInput{ContactID: e.TargetID, ExpectedVersion: e.TargetVersion,
			Name: textPtr(vals, "name"), Title: textPtr(vals, "title"),
			DealRole: textPtr(vals, "deal_role"), Phone: textPtr(vals, "phone"),
			Email: textPtr(vals, "email"), Wechat: textPtr(vals, "wechat"),
			Note: textPtr(vals, "note")}
		_, err := updateContactTx(ctx, tx, wc, now, in)
		return err
	case "opportunity":
		in := UpdateOpportunityInput{OpportunityID: e.TargetID, ExpectedVersion: e.TargetVersion,
			Name: textPtr(vals, "name"), Source: textPtr(vals, "source"),
			Stage: textPtr(vals, "stage"), Owner: textPtr(vals, "owner"),
			NextStep:         textPtr(vals, "next_step"),
			ExpectedSignDate: datePtr(vals, "expected_sign_date"),
			EstAmount:        money(vals, "est_amount"), WinProbability: number(vals, "win_probability")}
		if contactID, ok := p.optionalRelatedID(e.GroupID, "primary_contact", "contact"); ok {
			in.PrimaryContactID = &contactID
		}
		_, err := updateOpportunityTx(ctx, tx, wc, now, in)
		return err
	}
	// V1a deliberately does not open interaction updates: correcting a logged
	// conversation is a different act from recording one, and it has no
	// reviewed path yet.
	return protocol.Review(protocol.CodeUnsupportedField,
		fmt.Sprintf("entity type %q cannot be updated through confirm in this version", e.EntityType), nil)
}

func (p *confirmPlan) materializeAction(ctx context.Context, tx *sql.Tx, wc WriteContext,
	now time.Time, a actionCand) (Materialization, error) {

	id := p.actionIDs[a.GroupID]
	mat := Materialization{CandidateType: "action", CandidateID: a.ID,
		EntityType: a.ActionType, EntityID: id, Action: "created"}

	switch a.ActionType {
	case "project":
		in := CreateProjectInput{
			Name: draftText(a.Draft, "name"), Description: draftText(a.Draft, "description"),
			Stage: draftText(a.Draft, "stage"), Outcome: draftText(a.Draft, "outcome"),
			CompletionCriteria: draftText(a.Draft, "completion_criteria"),
			TargetDate:         draftText(a.Draft, "target_date"), StartDate: draftText(a.Draft, "start_date"),
			EndDate: draftText(a.Draft, "end_date"), NextReviewAt: draftText(a.Draft, "next_review_at"),
			HardDueAt: draftText(a.Draft, "hard_due_at"), Importance: draftText(a.Draft, "importance"),
		}
		if err := in.normalize(); err != nil {
			return mat, err
		}
		if _, err := createProjectTx(ctx, tx, wc, now, id, in); err != nil {
			return mat, err
		}

	case "milestone":
		projectID, err := p.projectOf(a)
		if err != nil {
			return mat, err
		}
		in := CreateMilestoneInput{
			ProjectID: projectID, Name: draftText(a.Draft, "name"),
			Description: draftText(a.Draft, "description"), TargetDate: draftText(a.Draft, "target_date"),
			Status: draftText(a.Draft, "status"), Importance: draftText(a.Draft, "importance"),
			Note: draftText(a.Draft, "note"),
		}
		if in.Status == "" {
			in.Status = "pending"
		}
		if in.Importance == "" {
			in.Importance = string(P2)
		}
		if _, err := createMilestoneTx(ctx, tx, wc, now, id, in); err != nil {
			return mat, err
		}

	case "task":
		projectID, err := p.projectOf(a)
		if err != nil {
			return mat, err
		}
		milestoneID := ""
		if parent, ok := p.actionByGrp[a.ParentGroup]; ok && parent.ActionType == "milestone" {
			milestoneID = p.actionIDs[parent.GroupID]
		}
		subjectType, subjectID := a.SubjectType, a.SubjectID
		if a.SubjectEntityGrp != "" {
			subjectType = p.entityByGrp[a.SubjectEntityGrp].EntityType
			subjectID = p.entityIDs[a.SubjectEntityGrp]
		}
		in := CreateTaskInput{
			ProjectID: projectID, MilestoneID: milestoneID,
			Title: draftText(a.Draft, "title"), Detail: draftText(a.Draft, "detail"),
			CompletionCriteria: draftText(a.Draft, "completion_criteria"),
			WaitingFor:         draftText(a.Draft, "waiting_for"),
			Status:             draftText(a.Draft, "status"), Importance: draftText(a.Draft, "importance"),
			HardDueAt: draftText(a.Draft, "hard_due_at"), EarliestStartAt: draftText(a.Draft, "earliest_start_at"),
			NextReviewAt: draftText(a.Draft, "next_review_at"), PlannedDate: draftText(a.Draft, "planned_date"),
			TimeSlot:        draftText(a.Draft, "time_slot"),
			EstimateMinutes: draftInt(a.Draft, "estimate_minutes"),
			PlannedMinutes:  draftInt(a.Draft, "planned_minutes"),
			SubjectType:     subjectType, SubjectID: subjectID,
		}
		if err := in.normalize(); err != nil {
			return mat, err
		}
		if _, err := createTaskTx(ctx, tx, wc, now, id, in); err != nil {
			return mat, err
		}
	}
	return mat, markCandidateMaterialized(ctx, tx, "action_candidates", a.ID, a.ActionType, id)
}

// projectOf walks up the parent chain to the project a milestone or task
// belongs to. Tasks under a milestone inherit both, which is why project_id is
// injected here rather than left for the draft to state twice.
func (p *confirmPlan) projectOf(a actionCand) (string, error) {
	for group := a.ParentGroup; group != ""; {
		parent, ok := p.actionByGrp[group]
		if !ok {
			break
		}
		if parent.ActionType == "project" {
			return p.actionIDs[parent.GroupID], nil
		}
		group = parent.ParentGroup
	}
	return "", protocol.Review(protocol.CodeDependencyConflict,
		"this action has no project above it", map[string]any{"action_candidate_id": a.ID})
}

// materializeRelation writes the relations that live in their own tables.
// belongs_to, primary_contact and about are NOT written here: they were already
// applied inside the create DTO, and re-applying them would mean two places can
// set the same column.
func (p *confirmPlan) materializeRelation(ctx context.Context, tx *sql.Tx, wc WriteContext,
	now time.Time, r relationCand, decisionIDs map[string]string) error {

	fromID, err := p.resolveRef(r.FromRef, r.FromID, r.FromGroup)
	if err != nil {
		return err
	}
	toID, err := p.resolveRef(r.ToRef, r.ToID, r.ToGroup)
	if err != nil {
		return err
	}
	spec, err := lookupRelation(r.FromType, r.RelationType, r.ToType, "relation:"+r.ID)
	if err != nil {
		return err
	}

	storageKey := fromID + ":" + toID
	switch r.RelationType {
	case "advances":
		if err := linkOpportunityProjectTx(ctx, tx, now, toID, fromID, "primary"); err != nil {
			return err
		}
		storageKey = toID + ":" + fromID
	case "documented_by":
		role, err := spec.checkAttributes(r.Attributes, "relation:"+r.ID)
		if err != nil {
			return err
		}
		if err := linkInteractionDocumentTx(ctx, tx, now, fromID, toID, role); err != nil {
			return err
		}
	case "evidence_for":
		if err := linkDocumentToEntityTx(ctx, tx, now, fromID, r.ToType, toID, "evidence"); err != nil {
			return err
		}
	case "belongs_to", "primary_contact", "about":
		// Already materialised as a column of the object's own create.
	default:
		return protocol.Review(protocol.CodeUnsupportedRel,
			fmt.Sprintf("relation %s has no materialisation", r.RelationType), nil)
	}

	return recordRelationSource(ctx, tx, now, r, spec, fromID, toID, storageKey,
		wc.CorrelationID, decisionIDs[verdictKey("relation", r.ID)])
}

// relatedID resolves a required relation into a real id at DTO-build time.
func (p *confirmPlan) relatedID(groupID, relationType, toType string) (string, error) {
	r := p.findRelationFrom(groupID, relationType, toType)
	if r == nil {
		return "", protocol.Review(protocol.CodeMissingField,
			fmt.Sprintf("an accepted %s %s relation is required here", relationType, toType),
			map[string]any{"entity_group_id": groupID})
	}
	return p.resolveRef(r.ToRef, r.ToID, r.ToGroup)
}

func (p *confirmPlan) optionalRelatedID(groupID, relationType, toType string) (string, bool) {
	r := p.findRelationFrom(groupID, relationType, toType)
	if r == nil {
		return "", false
	}
	id, err := p.resolveRef(r.ToRef, r.ToID, r.ToGroup)
	if err != nil {
		return "", false
	}
	return id, true
}

// ---------------------------------------------------------------------------
// provenance
// ---------------------------------------------------------------------------

func (p *confirmPlan) recordFieldSource(ctx context.Context, tx *sql.Tx, now time.Time,
	e entityCand, f factCand, decisionIDs map[string]string) error {

	entityID := p.entityIDs[e.GroupID]
	var version sql.NullInt64
	// interactions are append-only and carry no version; the column stays NULL
	// rather than inventing a 1 that nothing would ever compare against.
	if e.EntityType != "interaction" {
		if err := tx.QueryRowContext(ctx,
			"SELECT version FROM "+entityTables[e.EntityType]+" WHERE id = ?", entityID).
			Scan(&version); err != nil {
			return err
		}
	}
	var runID sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT run_id FROM fact_candidates WHERE id = ?`, f.ID).Scan(&runID)

	_, err := tx.ExecContext(ctx, `
        INSERT INTO source_attributions (id, entity_type, entity_id, field_name,
                                         materialized_version, normalized_value_hash,
                                         document_id, source_locator_json, extraction_run_id,
                                         decision_id, origin_type, created_at)
        VALUES (?,?,?,?,?,?,?,?,?,?, 'evidence', ?)`,
		system.NewID("src"), e.EntityType, entityID, f.FieldName,
		version, f.Value.hash(), f.DocumentID, mustJSON(f.Locator), runID,
		nullString(decisionIDs[verdictKey("fact", f.ID)]), system.FormatTimestamp(now))
	return err
}

func recordRelationSource(ctx context.Context, tx *sql.Tx, now time.Time, r relationCand,
	spec relationSpec, fromID, toID, storageKey, correlationID, decisionID string) error {

	var runID sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT run_id FROM relation_candidates WHERE id = ?`, r.ID).Scan(&runID)

	// Replacing a link closes the previous attribution rather than deleting it:
	// when a relation stopped being true is as much a fact as that it was.
	if _, err := tx.ExecContext(ctx, `
        UPDATE relation_attributions SET valid_to_correlation_id = ?
         WHERE storage_type = ? AND storage_key <> ? AND valid_to_correlation_id IS NULL
           AND relation_type = ? AND from_type = ? AND from_id = ?`,
		correlationID, spec.storageType, storageKey, r.RelationType, r.FromType, fromID); err != nil {
		return err
	}

	_, err := tx.ExecContext(ctx, `
        INSERT INTO relation_attributions (id, relation_type, from_type, from_id, to_type, to_id,
                                           storage_type, storage_key, valid_from_correlation_id,
                                           document_id, source_locator_json, extraction_run_id,
                                           decision_id, origin_type, created_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?, 'evidence', ?)`,
		system.NewID("relsrc"), r.RelationType, r.FromType, fromID, r.ToType, toID,
		spec.storageType, storageKey, correlationID, r.DocumentID, mustJSON(r.Locator),
		runID, nullString(decisionID), system.FormatTimestamp(now))
	return err
}

func markCandidateMaterialized(ctx context.Context, tx *sql.Tx, table, id, entityType, entityID string) error {
	_, err := tx.ExecContext(ctx,
		"UPDATE "+table+" SET materialized_type = ?, materialized_id = ? WHERE id = ?",
		entityType, entityID, id)
	return err
}

// ---------------------------------------------------------------------------
// small typed accessors, so DTO construction above stays readable
// ---------------------------------------------------------------------------

func text(vals map[string]CandidateValue, name string) string {
	return vals[name].Text
}

func textPtr(vals map[string]CandidateValue, name string) *string {
	v, ok := vals[name]
	if !ok {
		return nil
	}
	s := v.Text
	return &s
}

func dateOf(vals map[string]CandidateValue, name string) string { return vals[name].ISO }

func datePtr(vals map[string]CandidateValue, name string) *string {
	v, ok := vals[name]
	if !ok {
		return nil
	}
	s := v.ISO
	return &s
}

func timestampOf(vals map[string]CandidateValue, name string) string { return vals[name].RFC3339 }

func money(vals map[string]CandidateValue, name string) *float64 {
	v, ok := vals[name]
	if !ok {
		return nil
	}
	return v.Amount
}

func number(vals map[string]CandidateValue, name string) *float64 {
	v, ok := vals[name]
	if !ok {
		return nil
	}
	return v.Number
}

func draftText(draft map[string]any, key string) string {
	if s, ok := draft[key].(string); ok {
		return s
	}
	return ""
}

func draftInt(draft map[string]any, key string) int {
	if f, ok := draft[key].(float64); ok {
		return int(f)
	}
	return 0
}
