package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// candidate.revise: replace a proposal with a better one (design §3).
//
// This exists so that "the model got the customer name slightly wrong" does not
// force the user to reject everything and re-extract. It is carefully NOT an
// approval: revise inserts a new candidate, marks the old one superseded and
// appends a candidate_revisions row - it never touches candidate_decisions and
// never materialises anything. An agent may call it; a person still has to
// confirm afterwards.
//
// The new row inherits the old one's group_id (for entities and actions) or its
// logical key (for facts and relations), so everything that pointed at the old
// proposal keeps pointing at the same thing. That is the whole reason groups
// exist as separate tables: without them, revising an account would mean
// rewriting every fact, relation and action that mentioned it.

// ReviseInput is the payload of `candidate.revise`. Replacement is the FULL DTO
// of the matching proposal array element - not a merge patch. A patch would let
// a caller change a value without restating the source it came from, and this
// layer's entire premise is that every claim carries its evidence.
type ReviseInput struct {
	SchemaVersion int             `json:"schema_version"`
	InboxID       string          `json:"inbox_id"`
	CandidateType string          `json:"candidate_type"`
	CandidateID   string          `json:"candidate_id"`
	Reason        string          `json:"reason,omitempty"`
	Replacement   json.RawMessage `json:"replacement"`
}

// ReviseResult names the row the caller should now review.
type ReviseResult struct {
	CandidateType  string `json:"candidate_type"`
	OldCandidateID string `json:"old_candidate_id"`
	NewCandidateID string `json:"new_candidate_id"`
	RevisionID     string `json:"revision_id"`
}

var candidateTables = map[string]string{
	"entity": "entity_candidates", "fact": "fact_candidates",
	"relation": "relation_candidates", "action": "action_candidates",
}

// Revise supersedes one candidate and inserts its replacement.
func (s *Store) Revise(ctx context.Context, wc WriteContext, layout system.Layout,
	in ReviseInput) (*Result, error) {

	if in.SchemaVersion != 0 && in.SchemaVersion != 1 {
		return nil, protocol.BadInput("schema_version must be 1")
	}
	table, ok := candidateTables[in.CandidateType]
	if !ok {
		return nil, protocol.BadInput("candidate_type must be entity|fact|relation|action")
	}
	if in.CandidateID == "" || len(in.Replacement) == 0 {
		return nil, protocol.BadInput("candidate_id and replacement are required")
	}

	return s.execute(ctx, "candidate.revise", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		var runID, status, inboxID string
		err := tx.QueryRowContext(ctx, `
            SELECT c.run_id, c.status, r.inbox_id
              FROM `+table+` c JOIN extraction_runs r ON r.id = c.run_id
             WHERE c.id = ?`, in.CandidateID).Scan(&runID, &status, &inboxID)
		if err == sql.ErrNoRows {
			return nil, protocol.NotFound("candidate %s does not exist", in.CandidateID)
		}
		if err != nil {
			return nil, err
		}
		if in.InboxID != "" && in.InboxID != inboxID {
			return nil, protocol.BadInput("candidate %s belongs to a different inbox item", in.CandidateID)
		}
		// Once a candidate has been decided, revising it would let a second
		// proposal inherit a decision it was never shown for.
		if status != "proposed" {
			return nil, protocol.BadInput("candidate %s is %s and can no longer be revised",
				in.CandidateID, status)
		}

		inbox, err := loadInboxItem(ctx, tx, inboxID)
		if err != nil {
			return nil, err
		}
		original, err := loadOriginalBytes(ctx, tx, layout, inbox.DocumentID)
		if err != nil {
			return nil, err
		}

		// Supersede first: the "one live candidate per group / per field" partial
		// unique indexes exist precisely so two live proposals for the same
		// thing cannot coexist, and inserting before retiring would trip them.
		if _, err := tx.ExecContext(ctx,
			"UPDATE "+table+" SET status = 'superseded' WHERE id = ?", in.CandidateID); err != nil {
			return nil, err
		}
		newID, err := insertReplacement(ctx, tx, now, in, runID, inbox.DocumentID, original)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE "+table+" SET supersedes_id = ? WHERE id = ?", in.CandidateID, newID); err != nil {
			return nil, err
		}

		revisionID := system.NewID("rev")
		actorType := "agent"
		if wc.Actor.Type == "user" {
			actorType = "user"
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO candidate_revisions (id, candidate_type, old_candidate_id, new_candidate_id,
                                             actor_type, actor_id, reason, request_id, revised_at)
            VALUES (?,?,?,?,?,?,?,?,?)`,
			revisionID, in.CandidateType, in.CandidateID, newID, actorType,
			nullString(wc.Actor.ID), nullString(in.Reason), wc.RequestID,
			system.FormatTimestamp(now)); err != nil {
			return nil, err
		}

		return &Result{
			Data: ReviseResult{CandidateType: in.CandidateType, OldCandidateID: in.CandidateID,
				NewCandidateID: newID, RevisionID: revisionID},
			Changes: []protocol.Change{{EntityType: "inbox", EntityID: inboxID,
				EventType: "updated", ProjectionKeys: []string{"inbox"}}},
		}, nil
	})
}

// insertReplacement validates the replacement DTO exactly as propose would, so
// a revision cannot smuggle in a field, relation or draft key that the original
// proposal path would have refused.
func insertReplacement(ctx context.Context, tx *sql.Tx, now time.Time, in ReviseInput,
	runID, documentID string, original []byte) (string, error) {

	// A one-element proposal, run through the same validators.
	var single ProposeInput
	switch in.CandidateType {
	case "entity":
		var e ProposeEntity
		if err := json.Unmarshal(in.Replacement, &e); err != nil {
			return "", protocol.BadInput("invalid entity replacement: %v", err)
		}
		if err := carryGroup(ctx, tx, "entity_candidates", in.CandidateID, &e.GroupID); err != nil {
			return "", err
		}
		single.Entities = []ProposeEntity{e}
	case "fact":
		var f ProposeFact
		if err := json.Unmarshal(in.Replacement, &f); err != nil {
			return "", protocol.BadInput("invalid fact replacement: %v", err)
		}
		single.Facts = []ProposeFact{f}
	case "relation":
		var r ProposeRelation
		if err := json.Unmarshal(in.Replacement, &r); err != nil {
			return "", protocol.BadInput("invalid relation replacement: %v", err)
		}
		single.Relations = []ProposeRelation{r}
	case "action":
		var a ProposeAction
		if err := json.Unmarshal(in.Replacement, &a); err != nil {
			return "", protocol.BadInput("invalid action replacement: %v", err)
		}
		if err := carryGroup(ctx, tx, "action_candidates", in.CandidateID, &a.GroupID); err != nil {
			return "", err
		}
		single.Actions = []ProposeAction{a}
	}

	if err := single.validateReplacement(); err != nil {
		return "", err
	}
	// A revision is a NEW row. Reusing the id would make the supersession chain
	// point at itself and leave no record of what was originally proposed.
	if newID := replacementID(single); newID == in.CandidateID {
		return "", protocol.BadInput("a replacement needs its own candidate_id")
	}
	if err := single.verifyLocators(original); err != nil {
		return "", err
	}
	if err := single.insertCandidates(ctx, tx, now, runID, documentID); err != nil {
		return "", err
	}
	return replacementID(single), nil
}

func replacementID(in ProposeInput) string {
	switch {
	case len(in.Entities) == 1:
		return in.Entities[0].CandidateID
	case len(in.Facts) == 1:
		return in.Facts[0].CandidateID
	case len(in.Relations) == 1:
		return in.Relations[0].CandidateID
	case len(in.Actions) == 1:
		return in.Actions[0].CandidateID
	}
	return ""
}

// carryGroup forces the replacement onto the original's group, whatever the
// caller sent. A revision that lands in a new group would silently orphan every
// fact and relation attached to the old one.
func carryGroup(ctx context.Context, tx *sql.Tx, table, candidateID string, groupID *string) error {
	var existing string
	if err := tx.QueryRowContext(ctx,
		"SELECT group_id FROM "+table+" WHERE id = ?", candidateID).Scan(&existing); err != nil {
		return err
	}
	*groupID = existing
	return nil
}
