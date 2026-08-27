package ops

import (
	"fmt"
	"sort"

	"github.com/scottzx/mycontext/internal/protocol"
)

// The frozen V1a contract, as executable code (design §4).
//
// Three registries, and nothing outside them can be written by confirm:
//
//   fieldRegistry     which (entity, field) pairs exist, what type each takes
//   actionRegistry    which draft keys each action type allows
//   relationMatrix    which (from, relation, to) triples have a real storage
//
// Anything absent is refused by name. That is the point: an extractor that
// invents `opportunity.budget_confidence` gets UNSUPPORTED_FIELD instead of
// having its guess quietly land in a JSON column nobody audits.
//
// The registry's unit of work is the OBJECT, not the field. Confirm groups
// every accepted fact for one candidate, builds one typed DTO, and calls one Tx
// primitive - so an object gets one version bump and one event, not one per
// field it happened to mention.

type fieldDef struct {
	valueType string
	// requiredOnCreate fields must be present when intent=create; without them
	// the typed table would reject the row anyway, but with a far worse message.
	requiredOnCreate bool
	// enum, when set, restricts a text value to the domain's existing vocabulary.
	// Reused from the validators the CLI already enforces, so `opportunity
	// create --stage` and confirm cannot disagree about what a stage is.
	enum map[string]bool
}

var fieldRegistry = map[string]map[string]fieldDef{
	"account": {
		"name":       {valueType: "text", requiredOnCreate: true},
		"short_name": {valueType: "text"},
		"industry":   {valueType: "text"},
		"region":     {valueType: "text"},
		"owner":      {valueType: "text"},
		"note":       {valueType: "text"},
	},
	"contact": {
		"name":      {valueType: "text", requiredOnCreate: true},
		"title":     {valueType: "text"},
		"phone":     {valueType: "text"},
		"email":     {valueType: "text"},
		"wechat":    {valueType: "text"},
		"note":      {valueType: "text"},
		"deal_role": {valueType: "text", enum: validDealRole},
	},
	"opportunity": {
		"name":               {valueType: "text", requiredOnCreate: true},
		"source":             {valueType: "text"},
		"owner":              {valueType: "text"},
		"next_step":          {valueType: "text"},
		"stage":              {valueType: "text", enum: validOpportunityStage},
		"est_amount":         {valueType: "money"},
		"win_probability":    {valueType: "number"},
		"expected_sign_date": {valueType: "date"},
	},
	"interaction": {
		"occurred_at":  {valueType: "timestamp", requiredOnCreate: true},
		"channel":      {valueType: "text", enum: validInteractionChannel},
		"summary":      {valueType: "text"},
		"participants": {valueType: "text"},
		"owner":        {valueType: "text"},
	},
}

// entityCreateRelations names the relations an entity type MUST have accepted
// alongside it. They are injected into the same create DTO rather than applied
// afterwards, because `contacts.account_id` is NOT NULL - there is no moment at
// which a contact legitimately exists without its account.
var entityCreateRelations = map[string][]struct {
	relationType string
	toType       string
}{
	"contact":     {{"belongs_to", "account"}},
	"opportunity": {{"belongs_to", "account"}},
	"interaction": {{"about", "opportunity"}},
}

func lookupField(entityType, fieldName string, path string) (fieldDef, error) {
	fields, ok := fieldRegistry[entityType]
	if !ok {
		return fieldDef{}, protocol.Review(protocol.CodeUnsupportedField,
			fmt.Sprintf("entity type %q is not writable in this version", entityType),
			map[string]any{"path": path})
	}
	def, ok := fields[fieldName]
	if !ok {
		return fieldDef{}, protocol.Review(protocol.CodeUnsupportedField,
			fmt.Sprintf("%s has no writable field %q", entityType, fieldName),
			map[string]any{"path": path, "known_fields": fieldNames(fields)})
	}
	return def, nil
}

// checkValue enforces the registry's declared type and vocabulary. A value that
// parses but names a stage that does not exist is still wrong, and saying which
// values are allowed is more useful to a review UI than saying it failed.
func (d fieldDef) checkValue(entityType, fieldName string, v CandidateValue, path string) error {
	if v.Type != d.valueType {
		return protocol.Review(protocol.CodeUnsupportedValue,
			fmt.Sprintf("%s.%s takes a %s value, got %s", entityType, fieldName, d.valueType, v.Type),
			map[string]any{"path": path})
	}
	if d.enum != nil && !d.enum[v.Text] {
		return protocol.Review(protocol.CodeUnsupportedValue,
			fmt.Sprintf("%s.%s does not accept %q", entityType, fieldName, v.Text),
			map[string]any{"path": path, "allowed": enumNames(d.enum)})
	}
	return nil
}

// ---------------------------------------------------------------------------
// action registry
// ---------------------------------------------------------------------------

type actionFieldDef struct {
	kind     string // text|date|timestamp|number|enum
	required bool
	enum     map[string]bool
}

// actionRegistry mirrors fieldRegistry for drafts. Draft values are plain JSON
// scalars rather than typed value objects: an action draft is a form the user
// is about to fill in, not a claim about the world that needs provenance per
// field. The provenance that matters - which evidence suggested this action at
// all - is on the candidate row itself.
var actionRegistry = map[string]map[string]actionFieldDef{
	"project": {
		"name":                {kind: "text", required: true},
		"description":         {kind: "text"},
		"stage":               {kind: "enum", enum: validStage},
		"outcome":             {kind: "text"},
		"completion_criteria": {kind: "text"},
		"target_date":         {kind: "date"},
		"start_date":          {kind: "date"},
		"end_date":            {kind: "date"},
		"next_review_at":      {kind: "date"},
		"hard_due_at":         {kind: "timestamp"},
		"importance":          {kind: "enum", enum: validImportance},
	},
	"milestone": {
		"name":        {kind: "text", required: true},
		"target_date": {kind: "date", required: true},
		"description": {kind: "text"},
		"note":        {kind: "text"},
		"status":      {kind: "enum", enum: validMilestoneStatus},
		"importance":  {kind: "enum", enum: validImportance},
	},
	"task": {
		"title":               {kind: "text", required: true},
		"detail":              {kind: "text"},
		"completion_criteria": {kind: "text"},
		"waiting_for":         {kind: "text"},
		"hard_due_at":         {kind: "timestamp"},
		"earliest_start_at":   {kind: "timestamp"},
		"next_review_at":      {kind: "date"},
		"planned_date":        {kind: "date"},
		"estimate_minutes":    {kind: "number"},
		"planned_minutes":     {kind: "number"},
		"status":              {kind: "enum", enum: validTaskStatus},
		"importance":          {kind: "enum", enum: validImportance},
		"time_slot":           {kind: "enum", enum: validTimeSlot},
	},
}

// validateActionDraft checks a draft against the registry for its action type.
// Unknown keys are rejected rather than dropped: silently ignoring a key the
// extractor set means the user reviews a task that does something other than
// what the review screen showed them.
func validateActionDraft(actionType string, draft map[string]any, path string) error {
	spec, ok := actionRegistry[actionType]
	if !ok {
		return protocol.Review(protocol.CodeUnsupportedAction,
			fmt.Sprintf("action type %q is not supported", actionType),
			map[string]any{"path": path})
	}
	for key, raw := range draft {
		def, known := spec[key]
		if !known {
			return protocol.Review(protocol.CodeUnsupportedAction,
				fmt.Sprintf("%s drafts have no field %q", actionType, key),
				map[string]any{"path": path + "." + key, "known_fields": actionFieldNames(spec)})
		}
		if err := checkActionValue(def, raw, path+"."+key); err != nil {
			return err
		}
	}
	for key, def := range spec {
		if !def.required {
			continue
		}
		if v, present := draft[key]; !present || v == nil || v == "" {
			return protocol.Review(protocol.CodeMissingField,
				fmt.Sprintf("%s requires %s", actionType, key),
				map[string]any{"path": path + "." + key})
		}
	}
	return nil
}

func checkActionValue(def actionFieldDef, raw any, path string) error {
	bad := func(msg string) error {
		return protocol.Review(protocol.CodeUnsupportedValue, msg, map[string]any{"path": path})
	}
	switch def.kind {
	case "text":
		if _, ok := raw.(string); !ok {
			return bad("expected a string")
		}
	case "date":
		s, ok := raw.(string)
		if !ok {
			return bad("expected a YYYY-MM-DD string")
		}
		return ValidateDate(path, s)
	case "timestamp":
		s, ok := raw.(string)
		if !ok {
			return bad("expected an RFC3339 string")
		}
		return ValidateTimestamp(path, s)
	case "number":
		if _, ok := raw.(float64); !ok {
			return bad("expected a number")
		}
	case "enum":
		s, ok := raw.(string)
		if !ok || !def.enum[s] {
			return protocol.Review(protocol.CodeUnsupportedValue,
				fmt.Sprintf("value %v is not allowed here", raw),
				map[string]any{"path": path, "allowed": enumNames(def.enum)})
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// relation materialization matrix
// ---------------------------------------------------------------------------

// relationSpec says where a relation actually lives. storageType is the real
// table or column, not a label: relation_attributions records it verbatim so
// provenance can be checked against the row rather than against a description
// of it.
type relationSpec struct {
	fromType    string
	toType      string
	storageType string
	// attrKey/attrValues declare the only attributes this relation may carry.
	// Every other relation must send an empty object, so relation semantics
	// cannot start leaking into free-form JSON one exception at a time.
	attrKey    string
	attrValues map[string]bool
	attrLegacy string
}

// V1a only. External Program and Application relations arrive with 011; listing
// them early would mean confirm accepts triples the schema cannot store.
var relationMatrix = []relationSpec{
	{fromType: "contact", toType: "account", storageType: "contacts.account_id"},
	{fromType: "opportunity", toType: "account", storageType: "opportunities.account_id"},
	{fromType: "opportunity", toType: "contact", storageType: "opportunities.primary_contact_id"},
	{fromType: "project", toType: "opportunity", storageType: "opportunity_projects"},
	{fromType: "document", toType: "account", storageType: "doc_links"},
	{fromType: "document", toType: "contact", storageType: "doc_links"},
	{fromType: "document", toType: "opportunity", storageType: "doc_links"},
	{fromType: "interaction", toType: "opportunity", storageType: "interactions.subject_type/id"},
	{fromType: "interaction", toType: "document", storageType: "interaction_documents",
		attrKey:    "role",
		attrValues: map[string]bool{"transcript": true, "minutes": true, "attachment": true, "evidence": true},
		attrLegacy: "evidence"},
}

var relationOfType = map[string]string{
	"contacts.account_id":              "belongs_to",
	"opportunities.account_id":         "belongs_to",
	"opportunities.primary_contact_id": "primary_contact",
	"opportunity_projects":             "advances",
	"doc_links":                        "evidence_for",
	"interactions.subject_type/id":     "about",
	"interaction_documents":            "documented_by",
}

// lookupRelation resolves one triple. An unlisted triple is UNSUPPORTED_RELATION
// and is never downgraded into a soft context_edge: a soft edge that looks like
// a hard one is worse than a refusal, because nothing downstream can tell.
func lookupRelation(fromType, relationType, toType, path string) (relationSpec, error) {
	for _, spec := range relationMatrix {
		if spec.fromType == fromType && spec.toType == toType &&
			relationOfType[spec.storageType] == relationType {
			return spec, nil
		}
	}
	return relationSpec{}, protocol.Review(protocol.CodeUnsupportedRel,
		fmt.Sprintf("%s %s %s is not a supported relation", fromType, relationType, toType),
		map[string]any{"path": path})
}

// checkAttributes enforces the "empty object unless declared" rule.
func (spec relationSpec) checkAttributes(attrs map[string]any, path string) (string, error) {
	if spec.attrKey == "" {
		if len(attrs) > 0 {
			return "", protocol.Review(protocol.CodeUnsupportedRel,
				"this relation does not accept attributes", map[string]any{"path": path})
		}
		return "", nil
	}
	if len(attrs) == 0 {
		return spec.attrLegacy, nil
	}
	if len(attrs) > 1 {
		return "", protocol.Review(protocol.CodeUnsupportedRel,
			fmt.Sprintf("this relation only accepts %q", spec.attrKey),
			map[string]any{"path": path})
	}
	raw, ok := attrs[spec.attrKey]
	if !ok {
		return "", protocol.Review(protocol.CodeUnsupportedRel,
			fmt.Sprintf("this relation only accepts %q", spec.attrKey),
			map[string]any{"path": path})
	}
	value, ok := raw.(string)
	if !ok || !spec.attrValues[value] {
		return "", protocol.Review(protocol.CodeUnsupportedValue,
			fmt.Sprintf("%s %v is not allowed", spec.attrKey, raw),
			map[string]any{"path": path, "allowed": enumNames(spec.attrValues)})
	}
	return value, nil
}

// enumNames, fieldNames and actionFieldNames exist only to make a refusal
// actionable: a UI that is told "stage is not allowed" cannot offer the user a
// fix, and one that is told which stages exist can.
func enumNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fieldNames(m map[string]fieldDef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func actionFieldNames(m map[string]actionFieldDef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
