package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// The review screen's read model.
//
// One call has to be enough to render the whole three-column review: the
// original on the left, the candidate facts and relations in the middle, the
// candidate actions and the confirm summary on the right. Splitting it across
// several requests would let the columns disagree about which run they are
// showing, and confirm would then be checking a version the user never saw.

// CandidateQuote is a candidate plus the exact text it came from, so the review
// UI can show the sentence next to the claim without re-reading the file.
type CandidateQuote struct {
	DocumentID string        `json:"document_id"`
	Locator    SourceLocator `json:"locator"`
	Quote      string        `json:"quote"`
}

type EntityCandidateView struct {
	CandidateID   string          `json:"candidate_id"`
	GroupID       string          `json:"group_id"`
	EntityType    string          `json:"entity_type"`
	Intent        string          `json:"intent"`
	TargetID      string          `json:"target_id,omitempty"`
	TargetVersion int64           `json:"target_version,omitempty"`
	TargetLabel   string          `json:"target_label,omitempty"`
	MatchBasis    json.RawMessage `json:"match_basis,omitempty"`
	Status        string          `json:"status"`
}

type FactCandidateView struct {
	CandidateID   string         `json:"candidate_id"`
	EntityGroupID string         `json:"entity_group_id"`
	FieldName     string         `json:"field_name"`
	Value         CandidateValue `json:"value"`
	Confidence    *float64       `json:"confidence,omitempty"`
	Status        string         `json:"status"`
	Source        CandidateQuote `json:"source"`
}

type RelationCandidateView struct {
	CandidateID  string         `json:"candidate_id"`
	FromRef      string         `json:"from_ref"`
	FromType     string         `json:"from_type"`
	FromKey      string         `json:"from_key"`
	RelationType string         `json:"relation_type"`
	ToRef        string         `json:"to_ref"`
	ToType       string         `json:"to_type"`
	ToKey        string         `json:"to_key"`
	Attributes   map[string]any `json:"attributes,omitempty"`
	Status       string         `json:"status"`
	Source       CandidateQuote `json:"source"`
}

type ActionCandidateView struct {
	CandidateID string         `json:"candidate_id"`
	GroupID     string         `json:"group_id"`
	ActionType  string         `json:"action_type"`
	ParentGroup string         `json:"parent_action_group_id,omitempty"`
	SubjectType string         `json:"subject_type,omitempty"`
	SubjectKey  string         `json:"subject_key,omitempty"`
	Draft       map[string]any `json:"draft"`
	Status      string         `json:"status"`
	Source      CandidateQuote `json:"source"`
}

// InboxDetail is everything `inbox.get` returns.
type InboxDetail struct {
	Item         InboxItem               `json:"item"`
	OriginalText string                  `json:"original_text"`
	ActiveRunID  string                  `json:"active_run_id,omitempty"`
	Entities     []EntityCandidateView   `json:"entities"`
	Facts        []FactCandidateView     `json:"facts"`
	Relations    []RelationCandidateView `json:"relations"`
	Actions      []ActionCandidateView   `json:"actions"`
}

// GetInbox loads one item with its active extraction and every candidate the
// user has to decide on.
func (s *Store) GetInbox(ctx context.Context, layout system.Layout, id string) (*InboxDetail, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+inboxColumns+` FROM inbox_items WHERE id = ?`, id)
	item, err := scanInboxItem(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("inbox item %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	detail := &InboxDetail{Item: *item,
		Entities: []EntityCandidateView{}, Facts: []FactCandidateView{},
		Relations: []RelationCandidateView{}, Actions: []ActionCandidateView{}}

	var original []byte
	if item.DocumentID != "" {
		if original, err = s.readOriginal(ctx, layout, item.DocumentID); err != nil {
			return nil, err
		}
		detail.OriginalText = string(original)
	}

	var runID sql.NullString
	if err := s.db.SQL().QueryRowContext(ctx, `
        SELECT id FROM extraction_runs
         WHERE inbox_id = ? AND status = 'completed'
         ORDER BY completed_at DESC LIMIT 1`, id).Scan(&runID); err != nil && err != sql.ErrNoRows {
		return nil, sqlite.Classify(err)
	}
	if !runID.Valid {
		return detail, nil
	}
	detail.ActiveRunID = runID.String

	quote := func(loc SourceLocator) string {
		if loc.EndByte > int64(len(original)) || loc.StartByte < 0 {
			return ""
		}
		return string(original[loc.StartByte:loc.EndByte])
	}

	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT c.id, c.group_id, c.entity_type, c.intent, COALESCE(c.target_id, ''),
               COALESCE(c.target_version, 0), c.match_basis_json, c.status
          FROM entity_candidates c
         WHERE c.run_id = ? AND c.status <> 'superseded' ORDER BY c.id`, detail.ActiveRunID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	for rows.Next() {
		var v EntityCandidateView
		var basis sql.NullString
		if err := rows.Scan(&v.CandidateID, &v.GroupID, &v.EntityType, &v.Intent,
			&v.TargetID, &v.TargetVersion, &basis, &v.Status); err != nil {
			rows.Close()
			return nil, sqlite.Classify(err)
		}
		if basis.Valid {
			v.MatchBasis = json.RawMessage(basis.String)
		}
		// A create/update decision is meaningless without seeing WHICH existing
		// row would be touched, so the label is resolved here rather than
		// leaving the UI to make a second round trip per candidate.
		if v.TargetID != "" {
			v.TargetLabel = s.entityLabel(ctx, v.EntityType, v.TargetID)
		}
		detail.Entities = append(detail.Entities, v)
	}
	rows.Close()

	rows, err = s.db.SQL().QueryContext(ctx, `
        SELECT id, entity_group_id, field_name, value_json, confidence, status,
               source_document_id, source_locator_json
          FROM fact_candidates
         WHERE run_id = ? AND status <> 'superseded' ORDER BY entity_group_id, field_name`,
		detail.ActiveRunID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	for rows.Next() {
		var v FactCandidateView
		var valueJSON, locatorJSON string
		if err := rows.Scan(&v.CandidateID, &v.EntityGroupID, &v.FieldName, &valueJSON,
			&v.Confidence, &v.Status, &v.Source.DocumentID, &locatorJSON); err != nil {
			rows.Close()
			return nil, sqlite.Classify(err)
		}
		_ = json.Unmarshal([]byte(valueJSON), &v.Value)
		_ = json.Unmarshal([]byte(locatorJSON), &v.Source.Locator)
		v.Source.Quote = quote(v.Source.Locator)
		detail.Facts = append(detail.Facts, v)
	}
	rows.Close()

	rows, err = s.db.SQL().QueryContext(ctx, `
        SELECT id, from_ref_type, from_type,
               COALESCE(from_id, from_entity_group_id, from_action_group_id, ''),
               relation_type, to_ref_type, to_type,
               COALESCE(to_id, to_entity_group_id, to_action_group_id, ''),
               attributes_json, status, source_document_id, source_locator_json
          FROM relation_candidates
         WHERE run_id = ? AND status <> 'superseded' ORDER BY id`, detail.ActiveRunID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	for rows.Next() {
		var v RelationCandidateView
		var attrJSON, locatorJSON string
		if err := rows.Scan(&v.CandidateID, &v.FromRef, &v.FromType, &v.FromKey,
			&v.RelationType, &v.ToRef, &v.ToType, &v.ToKey,
			&attrJSON, &v.Status, &v.Source.DocumentID, &locatorJSON); err != nil {
			rows.Close()
			return nil, sqlite.Classify(err)
		}
		_ = json.Unmarshal([]byte(attrJSON), &v.Attributes)
		_ = json.Unmarshal([]byte(locatorJSON), &v.Source.Locator)
		v.Source.Quote = quote(v.Source.Locator)
		detail.Relations = append(detail.Relations, v)
	}
	rows.Close()

	rows, err = s.db.SQL().QueryContext(ctx, `
        SELECT id, group_id, action_type, COALESCE(parent_action_group_id, ''),
               COALESCE(subject_type, ''),
               COALESCE(subject_id, subject_entity_group_id, ''),
               draft_json, status, source_document_id, source_locator_json
          FROM action_candidates
         WHERE run_id = ? AND status <> 'superseded' ORDER BY action_type, id`, detail.ActiveRunID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	for rows.Next() {
		var v ActionCandidateView
		var draftJSON, locatorJSON string
		if err := rows.Scan(&v.CandidateID, &v.GroupID, &v.ActionType, &v.ParentGroup,
			&v.SubjectType, &v.SubjectKey, &draftJSON, &v.Status,
			&v.Source.DocumentID, &locatorJSON); err != nil {
			rows.Close()
			return nil, sqlite.Classify(err)
		}
		_ = json.Unmarshal([]byte(draftJSON), &v.Draft)
		_ = json.Unmarshal([]byte(locatorJSON), &v.Source.Locator)
		v.Source.Quote = quote(v.Source.Locator)
		detail.Actions = append(detail.Actions, v)
	}
	rows.Close()

	return detail, sqlite.Classify(rows.Err())
}

func (s *Store) readOriginal(ctx context.Context, layout system.Layout, documentID string) ([]byte, error) {
	tx, err := s.db.SQL().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer tx.Rollback()
	return loadOriginalBytes(ctx, tx, layout, documentID)
}

// entityLabel resolves a human-readable name for a match target. A missing row
// is not an error here: it only means the review screen shows the id.
func (s *Store) entityLabel(ctx context.Context, entityType, id string) string {
	table, ok := entityTables[entityType]
	if !ok {
		return ""
	}
	column := "name"
	if entityType == "interaction" {
		return ""
	}
	var label sql.NullString
	if err := s.db.SQL().QueryRowContext(ctx,
		"SELECT "+column+" FROM "+table+" WHERE id = ?", id).Scan(&label); err != nil {
		return ""
	}
	return label.String
}

// InboxPending is one row of v_inbox_pending: what is waiting, and for what.
type InboxPending struct {
	InboxID            string  `json:"inbox_id"`
	Title              *string `json:"title,omitempty"`
	SourceRef          *string `json:"source_ref,omitempty"`
	Status             string  `json:"status"`
	CaptureKind        string  `json:"capture_kind"`
	DocumentID         *string `json:"document_id,omitempty"`
	PackageID          string  `json:"package_id"`
	AssignedRootType   *string `json:"assigned_root_type,omitempty"`
	AssignedRootID     *string `json:"assigned_root_id,omitempty"`
	ErrorCode          *string `json:"error_code,omitempty"`
	ErrorMessage       *string `json:"error_message,omitempty"`
	Version            int64   `json:"version"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	ActiveRunID        *string `json:"active_run_id,omitempty"`
	RunningCount       int     `json:"running_count"`
	UndecidedEntities  int     `json:"undecided_entities"`
	UndecidedFacts     int     `json:"undecided_facts"`
	UndecidedRelations int     `json:"undecided_relations"`
	UndecidedActions   int     `json:"undecided_actions"`
}

// ListInboxPending answers `inbox.list`: everything still waiting on a person.
func (s *Store) ListInboxPending(ctx context.Context, limit int) ([]InboxPending, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT inbox_id, title, source_ref, status, capture_kind, document_id, package_id,
               assigned_root_type, assigned_root_id, error_code, error_message, version,
               created_at, updated_at, active_run_id, running_count,
               undecided_entities, undecided_facts, undecided_relations, undecided_actions
          FROM v_inbox_pending ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()
	out := []InboxPending{}
	for rows.Next() {
		var p InboxPending
		if err := rows.Scan(&p.InboxID, &p.Title, &p.SourceRef, &p.Status, &p.CaptureKind,
			&p.DocumentID, &p.PackageID, &p.AssignedRootType, &p.AssignedRootID,
			&p.ErrorCode, &p.ErrorMessage, &p.Version, &p.CreatedAt, &p.UpdatedAt,
			&p.ActiveRunID, &p.RunningCount, &p.UndecidedEntities, &p.UndecidedFacts,
			&p.UndecidedRelations, &p.UndecidedActions); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, p)
	}
	return out, sqlite.Classify(rows.Err())
}

// ArchiveInboxInput is the payload of `inbox.archive`.
type ArchiveInboxInput struct {
	SchemaVersion   int    `json:"schema_version"`
	InboxID         string `json:"inbox_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason,omitempty"`
}

// ArchiveInbox drops an item out of the queue without materialising anything.
// It archives the ITEM, never the Library package: the bytes stay sealed, so
// "I decided this was not worth filing" never becomes "the evidence is gone".
func (s *Store) ArchiveInbox(ctx context.Context, wc WriteContext, in ArchiveInboxInput) (*Result, error) {
	if in.InboxID == "" {
		return nil, protocol.BadInput("inbox_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the inbox item first")
	}
	return s.execute(ctx, "inbox.archive", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		item, err := loadInboxItem(ctx, tx, in.InboxID)
		if err != nil {
			return nil, err
		}
		if item.Status == "confirmed" {
			return nil, protocol.BadInput("inbox item %s is already confirmed", in.InboxID)
		}
		version, err := setInboxStatus(ctx, tx, in.InboxID, in.ExpectedVersion, "archived", now)
		if err != nil {
			return nil, err
		}
		if item.DocumentID != "" {
			if err := recordEvent(ctx, tx, wc, now, "document", item.DocumentID, "note", nil,
				map[string]any{"inbox_archived": in.InboxID, "reason": in.Reason}); err != nil {
				return nil, err
			}
		}
		return &Result{
			Data: map[string]any{"inbox_id": in.InboxID, "status": "archived", "version": version},
			Changes: []protocol.Change{{EntityType: "inbox", EntityID: in.InboxID,
				EventType: "updated", Version: version, ProjectionKeys: []string{"inbox"}}},
		}, nil
	})
}
