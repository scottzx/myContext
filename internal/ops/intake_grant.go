package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
)

// Confirmation grants: proof that a human, in a live UI session, clicked
// confirm on exactly the decisions being submitted (design §3).
//
// The problem this solves: `actor: "user:scott"` is a string. Anything that can
// reach the invoke endpoint can send it, which makes it an audit label, not an
// authorisation. Since the entire premise of this layer is "an agent may
// propose but only a person may materialise", the person's act needs a token
// the agent cannot mint.
//
// So the grant is bound to four things at once, and confirm re-checks all four:
// the session that issued it, the inbox item, the extraction run, and a hash of
// the decision list as displayed. Change any of them - review a different run,
// flip one accept to reject after the fact - and the grant no longer matches.

// GrantTTL is how long a confirmation stays valid. Long enough to survive a
// slow network round trip; short enough that a nonce left in a log is useless.
const GrantTTL = 5 * time.Minute

// CandidateDecision is one accept/reject from the review screen.
type CandidateDecision struct {
	CandidateType string `json:"candidate_type"`
	CandidateID   string `json:"candidate_id"`
	Decision      string `json:"decision"`
	Reason        string `json:"reason,omitempty"`
}

// IssueGrantInput is what the session-bound endpoint receives. It is
// deliberately NOT an invoke operation: putting it on the generic whitelist
// would hand every CLI and agent caller the capability the grant exists to
// withhold.
type IssueGrantInput struct {
	SessionID       string
	InboxID         string
	ActiveRunID     string
	ExpectedVersion int64
	Decisions       []CandidateDecision
}

// IssueConfirmationGrant mints a single-use nonce and stores only its hash, so
// a copy of the database never lets anyone confirm anything.
func (s *Store) IssueConfirmationGrant(ctx context.Context, in IssueGrantInput) (string, error) {
	if in.SessionID == "" {
		return "", protocol.BadInput("a confirmation grant requires a session")
	}
	if in.InboxID == "" || in.ActiveRunID == "" {
		return "", protocol.BadInput("inbox_id and active_run_id are required")
	}
	if in.ExpectedVersion <= 0 {
		return "", protocol.BadInput("expected_version is required")
	}
	if len(in.Decisions) == 0 {
		return "", protocol.BadInput("a confirmation grant requires the decisions it covers")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", protocol.Wrap(err, protocol.CodeIntegrity, "cannot generate a confirmation nonce")
	}
	nonce := hex.EncodeToString(raw)
	now := s.clock.Now()
	_, err := s.db.SQL().ExecContext(ctx, `
        INSERT INTO confirmation_grants (nonce_hash, session_id_hash, inbox_id, active_run_id,
                                         decisions_hash, expected_version, expires_at, created_at)
        VALUES (?,?,?,?,?,?,?,?)`,
		hashString(nonce), hashString(in.SessionID), in.InboxID, in.ActiveRunID,
		DecisionsHash(in.Decisions), in.ExpectedVersion,
		now.Add(GrantTTL).UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	if err != nil {
		return "", sqlite.Classify(err)
	}
	return nonce, nil
}

// DecisionsHash canonicalises a decision list so the same set always hashes the
// same regardless of the order the UI happened to send it in. Only type, id and
// verdict are hashed - the free-text reason is audit, not authorisation, and
// including it would invalidate a grant over a typo.
func DecisionsHash(decisions []CandidateDecision) string {
	parts := make([]string, 0, len(decisions))
	for _, d := range decisions {
		parts = append(parts, d.CandidateType+":"+d.CandidateID+"="+d.Decision)
	}
	sort.Strings(parts)
	return hashString(strings.Join(parts, "\n"))
}

// consumeGrant validates and burns a nonce inside the confirm transaction.
//
// The UPDATE ... WHERE consumed_at IS NULL is the atomic part: two confirms
// racing on the same nonce, the second one changes no rows and is refused. A
// replay of the same request_id never gets here at all - execute() returns the
// stored result first - so GRANT_USED means a genuinely different request tried
// to reuse a spent confirmation.
func consumeGrant(ctx context.Context, tx dbExecQuerier, nonce, sessionID, inboxID,
	runID string, expectedVersion int64, decisions []CandidateDecision,
	correlationID string, now time.Time) error {

	if nonce == "" {
		return protocol.Review(protocol.CodeGrantInvalid,
			"confirmation requires a nonce issued by the review UI", nil)
	}
	var (
		storedSession, storedInbox, storedRun, storedDecisions, expiresAt string
		storedVersion                                                     int64
		consumedAt                                                        *string
	)
	err := tx.QueryRowContext(ctx, `
        SELECT session_id_hash, inbox_id, active_run_id, decisions_hash,
               expected_version, expires_at, consumed_at
          FROM confirmation_grants WHERE nonce_hash = ?`, hashString(nonce)).
		Scan(&storedSession, &storedInbox, &storedRun, &storedDecisions,
			&storedVersion, &expiresAt, &consumedAt)
	if err != nil {
		return protocol.Review(protocol.CodeGrantInvalid, "this confirmation is not recognised", nil)
	}
	if consumedAt != nil {
		return protocol.Review(protocol.CodeGrantUsed, "this confirmation has already been used", nil)
	}
	if expiry, perr := time.Parse(time.RFC3339, expiresAt); perr == nil && now.After(expiry) {
		return protocol.Review(protocol.CodeGrantInvalid,
			"this confirmation has expired; review again and re-confirm", nil)
	}
	if storedSession != hashString(sessionID) {
		return protocol.Review(protocol.CodeGrantInvalid,
			"this confirmation was issued to a different session", nil)
	}
	if storedInbox != inboxID || storedRun != runID || storedVersion != expectedVersion {
		return protocol.Review(protocol.CodeGrantInvalid,
			"this confirmation does not match the item being confirmed", nil)
	}
	if storedDecisions != DecisionsHash(decisions) {
		return protocol.Review(protocol.CodeGrantInvalid,
			"the decisions changed after this confirmation was issued", nil)
	}
	res, err := tx.ExecContext(ctx, `
        UPDATE confirmation_grants SET consumed_at = ?, correlation_id = ?
         WHERE nonce_hash = ? AND consumed_at IS NULL`,
		now.UTC().Format(time.RFC3339), correlationID, hashString(nonce))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return protocol.Review(protocol.CodeGrantUsed, "this confirmation has already been used", nil)
	}
	return nil
}
