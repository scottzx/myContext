package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// Store is the ops.db repository plus the write pipeline that every entry
// point (CLI, Bridge, local HTTP) shares. There is exactly one place where
// ops writes happen (§12.1).
type Store struct {
	db    *sqlite.DB
	clock system.Clock
}

func NewStore(db *sqlite.DB, clock system.Clock) *Store {
	if clock == nil {
		clock = system.NewClock()
	}
	return &Store{db: db, clock: clock}
}

func (s *Store) DB() *sqlite.DB { return s.db }

// WriteContext carries the cross-cutting concerns of one mutation: who, why,
// idempotency and whether the user explicitly confirmed a risky action.
type WriteContext struct {
	RequestID     string
	Actor         Actor
	Reason        string
	Confirmed     bool
	DryRun        bool
	CorrelationID string
}

func (w WriteContext) validate() error {
	if w.RequestID == "" {
		return protocol.BadInput("request_id is required for write commands")
	}
	return w.Actor.validate()
}

// Result is what a use case returns: the new state plus the changes to report.
type Result struct {
	Data     any
	Changes  []protocol.Change
	Warnings []string
	Replayed bool
}

// mutation is the body of a use case, executed inside one short transaction.
type mutation func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error)

// execute runs a mutation with idempotency, auditing and a single short
// transaction (§11.2). A replayed request returns its first result without
// re-executing.
func (s *Store) execute(ctx context.Context, command string, wc WriteContext, payload any, fn mutation) (*Result, error) {
	if err := wc.validate(); err != nil {
		return nil, err
	}
	hash, err := system.PayloadHash(payload)
	if err != nil {
		return nil, protocol.Wrap(err, protocol.CodeInternal, "cannot hash payload")
	}

	if replay, err := s.replay(ctx, command, wc.RequestID, hash); err != nil {
		return nil, err
	} else if replay != nil {
		return replay, nil
	}

	now := s.clock.Now()
	var out *Result

	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		if err := claimRequest(ctx, tx, wc, command, hash, now); err != nil {
			return err
		}
		result, err := fn(ctx, tx, now)
		if err != nil {
			return err
		}
		out = result

		// A dry run must not leave the request claimed, or the real attempt
		// would be rejected as a payload mismatch.
		if wc.DryRun {
			return errDryRun
		}
		return completeRequest(ctx, tx, wc.RequestID, result, now)
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return nil, err
	}
	if out == nil {
		return nil, protocol.Internal("use case %s produced no result", command)
	}
	if wc.DryRun {
		out.Warnings = append(out.Warnings, "dry run: no changes were committed")
	}
	return out, nil
}

// errDryRun aborts the transaction after the use case has computed its
// result, so --dry-run reports real effects without persisting them.
var errDryRun = errors.New("dry run rollback")

func (s *Store) replay(ctx context.Context, command, requestID, hash string) (*Result, error) {
	var storedCommand, storedHash, status string
	var resultJSON, errorCode sql.NullString
	err := s.db.SQL().QueryRowContext(ctx, `
        SELECT command_name, payload_hash, status, result_json, error_code
          FROM command_requests WHERE request_id = ?`, requestID).
		Scan(&storedCommand, &storedHash, &status, &resultJSON, &errorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	if storedHash != hash || storedCommand != command {
		return nil, protocol.IdempotencyConflict(requestID)
	}
	if status != "completed" {
		// A 'started' row means a previous attempt died mid-transaction; the
		// transaction rolled back, so it is safe to say so rather than guess.
		return nil, protocol.Wrap(nil, protocol.CodeNeedsRecovery,
			"a previous attempt with this request_id did not complete")
	}
	out := &Result{Replayed: true, Warnings: []string{"replayed a previously completed request"}}
	if resultJSON.Valid {
		var payload struct {
			Data    json.RawMessage   `json:"data"`
			Changes []protocol.Change `json:"changes"`
		}
		if err := json.Unmarshal([]byte(resultJSON.String), &payload); err == nil {
			out.Data = payload.Data
			out.Changes = payload.Changes
		}
	}
	return out, nil
}

func claimRequest(ctx context.Context, tx *sql.Tx, wc WriteContext, command, hash string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
        INSERT INTO command_requests (request_id, command_name, payload_hash, actor, status, started_at)
        VALUES (?, ?, ?, ?, 'started', ?)`,
		wc.RequestID, command, hash, wc.Actor.Type, system.FormatTimestamp(now))
	return err
}

func completeRequest(ctx context.Context, tx *sql.Tx, requestID string, result *Result, now time.Time) error {
	payload, err := json.Marshal(map[string]any{"data": result.Data, "changes": result.Changes})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
        UPDATE command_requests
           SET status = 'completed', result_json = ?, completed_at = ?
         WHERE request_id = ?`, string(payload), system.FormatTimestamp(now), requestID)
	return err
}

// recordEvent appends one audit row inside the caller's transaction. Every
// mutation writes at least one (§15).
func recordEvent(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	entityType, entityID, eventType string, before, after any) error {

	beforeJSON, err := marshalOptional(before)
	if err != nil {
		return err
	}
	afterJSON, err := marshalOptional(after)
	if err != nil {
		return err
	}
	confirmed := 0
	if wc.Confirmed {
		confirmed = 1
	}
	_, err = tx.ExecContext(ctx, `
        INSERT INTO events (id, entity_type, entity_id, event_type, before_json, after_json,
                            actor_type, actor_id, entry_point, reason, confirmed,
                            request_id, correlation_id, occurred_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		system.NewID("evt"), entityType, entityID, eventType, beforeJSON, afterJSON,
		wc.Actor.Type, nullString(wc.Actor.ID), wc.Actor.EntryPoint, nullString(wc.Reason),
		confirmed, nullString(wc.RequestID), nullString(wc.CorrelationID),
		system.FormatTimestamp(now))
	return err
}

func marshalOptional(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
