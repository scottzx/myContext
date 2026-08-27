package httpui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
)

// The write half of the localhost adapter.
//
// Two things separate this from the read handlers, and both are the point of
// the intake design:
//
//   - The session identity comes from the SERVER, never from the request body.
//     A caller that could name its own session could name the one a grant was
//     issued to, which would make the grant prove nothing.
//   - Confirming needs a nonce that only /api/v1/confirmation-grant issues, and
//     that endpoint is not on the invoke whitelist the CLI and agents share.

// errUnsupportedOperation lets handleInvoke fall through to the write router
// and still produce one "unknown operation" message listing everything the
// instance actually supports.
var errUnsupportedOperation = errors.New("unsupported operation")

// invokeWrite routes the mutating operations. A read-only instance refuses them
// here rather than failing later against a read-only database, so the error the
// user sees names the real reason.
func (s *Server) invokeWrite(ctx context.Context, req invocationRequest, start time.Time) (any, error) {
	switch req.Operation {
	case "inbox.capture-text", "inbox.propose", "inbox.archive", "candidate.revise", "inbox.confirm":
	default:
		return nil, errUnsupportedOperation
	}
	if !s.opts.Write {
		return nil, &protocol.AppError{
			Code:    protocol.CodeForbidden,
			Message: "this instance is serving read-only; restart `mycontext ui serve` without --read-only",
		}
	}
	if req.RequestID == "" {
		return nil, protocol.BadInput("request_id is required for write operations")
	}
	wc := ops.WriteContext{RequestID: req.RequestID, Actor: parseActor(req.Actor)}

	switch req.Operation {
	case "inbox.capture-text":
		var in ops.CaptureTextInput
		if err := decodeInput(req.Input, &in); err != nil {
			return nil, err
		}
		return unwrap(s.store.CaptureText(ctx, wc, s.opts.Layout, in))

	case "inbox.propose":
		var in ops.ProposeInput
		if err := decodeInput(req.Input, &in); err != nil {
			return nil, err
		}
		return unwrap(s.store.Propose(ctx, wc, s.opts.Layout, in))

	case "candidate.revise":
		var in ops.ReviseInput
		if err := decodeInput(req.Input, &in); err != nil {
			return nil, err
		}
		return unwrap(s.store.Revise(ctx, wc, s.opts.Layout, in))

	case "inbox.archive":
		var in ops.ArchiveInboxInput
		if err := decodeInput(req.Input, &in); err != nil {
			return nil, err
		}
		return unwrap(s.store.ArchiveInbox(ctx, wc, in))

	case "inbox.confirm":
		var in ops.ConfirmInboxInput
		if err := decodeInput(req.Input, &in); err != nil {
			return nil, err
		}
		// The session is this server's own token, not anything the body said.
		in.SessionID = s.token
		return unwrap(s.store.ConfirmInbox(ctx, wc, s.opts.Layout, in))
	}
	return nil, errUnsupportedOperation
}

// unwrap flattens an ops.Result into the envelope's data field. Changes and
// warnings are dropped on purpose: the frontend refetches the projections it
// cares about, and a partial change list invites it to patch state by hand.
func unwrap(result *ops.Result, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return result.Data, nil
}

// parseActor normalises the wire's `user:<id>` / `agent:<id>` string into the
// typed actor the audit trail records. It is metadata only - nothing here
// grants any capability, which is exactly why confirm needs a separate grant.
func parseActor(actor string) ops.Actor {
	out := ops.Actor{Type: "ui", EntryPoint: "http"}
	kind, id, found := strings.Cut(actor, ":")
	if !found {
		kind = actor
	}
	switch kind {
	case "user", "agent", "ui", "system":
		out.Type = kind
	}
	if found {
		out.ID = id
	}
	return out
}

// grantRequest is what the review screen posts when the user clicks confirm.
type grantRequest struct {
	InboxID         string                  `json:"inbox_id"`
	ActiveRunID     string                  `json:"active_run_id"`
	ExpectedVersion int64                   `json:"expected_version"`
	Decisions       []ops.CandidateDecision `json:"decisions"`
}

// handleConfirmationGrant mints the single-use confirmation nonce.
//
// It lives outside the invoke whitelist so that "may propose" and "may
// materialise" stay genuinely different capabilities: an agent holding the
// session token can reach every invoke operation, but the review screen is the
// only thing that calls this, and it only calls it on a real click.
func (s *Server) handleConfirmationGrant(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if !s.opts.Write {
		s.writeEnvelope(w, "inbox.grant", nil, &protocol.AppError{
			Code:    protocol.CodeForbidden,
			Message: "this instance is serving read-only",
		}, start)
		return
	}
	var req grantRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.writeEnvelope(w, "inbox.grant", nil, protocol.BadInput("invalid grant request: %v", err), start)
		return
	}
	nonce, err := s.store.IssueConfirmationGrant(r.Context(), ops.IssueGrantInput{
		SessionID:       s.token,
		InboxID:         req.InboxID,
		ActiveRunID:     req.ActiveRunID,
		ExpectedVersion: req.ExpectedVersion,
		Decisions:       req.Decisions,
	})
	if err != nil {
		s.writeEnvelope(w, "inbox.grant", nil, err, start)
		return
	}
	s.writeEnvelope(w, "inbox.grant", map[string]any{
		"confirmation_nonce": nonce,
		"expires_in_seconds": int(ops.GrantTTL.Seconds()),
	}, nil, start)
}
