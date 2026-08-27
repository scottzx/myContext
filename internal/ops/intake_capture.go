package ops

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/library"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// CaptureTextMaxBytes caps one pasted capture. 256 KiB is far more than any
// transcript this is meant for, and the limit exists so a runaway paste fails
// immediately instead of after copying itself into the Library.
const CaptureTextMaxBytes = 256 << 10

// CaptureTextInput is the payload of `inbox.capture-text`.
type CaptureTextInput struct {
	SchemaVersion int    `json:"schema_version"`
	Title         string `json:"title,omitempty"`
	SourceRef     string `json:"source_ref,omitempty"`
	Text          string `json:"text"`
}

// CaptureTextResult is what a caller needs to go straight to review.
type CaptureTextResult struct {
	IngestionID  string `json:"ingestion_id"`
	PackageID    string `json:"package_id"`
	DocumentID   string `json:"document_id"`
	InboxID      string `json:"inbox_id"`
	CanonicalSHA string `json:"canonical_text_sha"`
	Bytes        int    `json:"bytes"`
	Replayed     bool   `json:"replayed,omitempty"`
}

func (in *CaptureTextInput) normalize() (string, error) {
	if in.SchemaVersion != 0 && in.SchemaVersion != 1 {
		return "", protocol.BadInput("schema_version must be 1")
	}
	if !utf8.ValidString(in.Text) {
		return "", protocol.BadInput("text must be valid UTF-8")
	}
	// Line endings are normalised ONCE, here, before the bytes are sealed.
	// Every locator offset afterwards is into these bytes, so a CRLF original
	// and its LF form must never both exist inside the Library.
	text := strings.ReplaceAll(in.Text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if strings.TrimSpace(text) == "" {
		return "", protocol.BadInput("text is required")
	}
	if len(text) > CaptureTextMaxBytes {
		return "", protocol.BadInput("text is %d bytes; the limit is %d", len(text), CaptureTextMaxBytes)
	}
	if in.Title == "" {
		in.Title = firstLine(text, 60)
	}
	return text, nil
}

func firstLine(text string, max int) string {
	line := text
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if len([]rune(line)) > max {
		line = string([]rune(line)[:max])
	}
	if line == "" {
		return "captured text"
	}
	return line
}

// capturePlan is what capture_ingestions.planned_json carries: exactly enough
// to rebuild the document and inbox registration after a crash, and nothing
// more. It holds no business facts, because a reconciler resuming a capture
// must not be able to invent any.
type capturePlan struct {
	SchemaVersion int    `json:"schema_version"`
	Title         string `json:"title"`
	SourceRef     string `json:"source_ref,omitempty"`
	Bytes         int    `json:"bytes"`
}

// CaptureText is the V1a entry point for evidence: normalise, seal the bytes in
// the Library, then register a document and an inbox item.
//
// This deliberately does NOT go through Store.execute. A SQLite transaction
// cannot span the file copy, so pretending the whole thing is one atomic
// command would be a lie that recovery then has to work around. Instead the
// authority for idempotency is capture_ingestions.request_id, and the same
// request replayed resumes from whatever stage it actually reached (design §5):
//
//	planned    -> re-seal/verify, then register
//	sealed     -> register
//	registered -> return the stored result
//
// Only the final registration is a real transaction, and only it touches
// command_requests - so execute()'s "a started row means recovery is required"
// contract keeps meaning what it says.
func (s *Store) CaptureText(ctx context.Context, wc WriteContext, layout system.Layout,
	in CaptureTextInput) (*Result, error) {

	text, err := in.normalize()
	if err != nil {
		return nil, err
	}
	if err := wc.validate(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(text))
	canonicalSHA := hex.EncodeToString(sum[:])

	existing, err := s.loadIngestionByRequest(ctx, wc.RequestID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// A retried request must be the same capture. Different bytes under the
		// same request id is a caller bug, and continuing would attach the old
		// package to the new text.
		if existing.CanonicalSHA != canonicalSHA {
			return nil, protocol.IdempotencyConflict(wc.RequestID)
		}
		if existing.State == "registered" {
			return &Result{
				Data: CaptureTextResult{
					IngestionID: existing.ID, PackageID: existing.PackageID,
					DocumentID: existing.DocumentID, InboxID: existing.InboxID,
					CanonicalSHA: canonicalSHA, Bytes: len(text), Replayed: true,
				},
				Warnings: []string{"replayed a previously completed capture"},
			}, nil
		}
	}

	now := s.clock.Now()
	ingestionID := ""
	if existing != nil {
		ingestionID = existing.ID
	} else {
		ingestionID = system.NewID("ingest")
		plan, _ := json.Marshal(capturePlan{
			SchemaVersion: 1, Title: in.Title, SourceRef: in.SourceRef, Bytes: len(text),
		})
		ts := system.FormatTimestamp(now)
		if _, err := s.db.SQL().ExecContext(ctx, `
            INSERT INTO capture_ingestions (id, request_id, capture_kind, source_ref, title,
                                            canonical_text_sha, planned_json, state, created_at, updated_at)
            VALUES (?,?,'text',?,?,?,?, 'planned', ?, ?)`,
			ingestionID, wc.RequestID, nullString(in.SourceRef), nullString(in.Title),
			canonicalSHA, string(plan), ts, ts); err != nil {
			return nil, sqlite.Classify(err)
		}
	}

	// Stage the normalised bytes and let library.Commit run its own journalled
	// file transaction. It is idempotent on the same request id, so a replay
	// after a crash between seal and registration finds the package it already
	// made instead of writing a second copy.
	stagingDir := filepath.Join(layout.System(), "capture", ingestionID)
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, s.failIngestion(ctx, ingestionID, "STAGING_FAILED",
			protocol.Wrap(err, protocol.CodeIntegrity, "cannot prepare capture staging"))
	}
	textPath := filepath.Join(stagingDir, "original.txt")
	if err := os.WriteFile(textPath, []byte(text), 0o600); err != nil {
		return nil, s.failIngestion(ctx, ingestionID, "STAGING_FAILED",
			protocol.Wrap(err, protocol.CodeIntegrity, "cannot write capture text"))
	}
	defer os.RemoveAll(stagingDir)

	commit, err := library.Commit(ctx, layout, s.NewLibraryJournal(), s.clock, library.CaptureInput{
		RequestID:     wc.RequestID,
		CapturedAt:    now,
		SourceRef:     in.SourceRef,
		CaptureMethod: "inbox.capture-text",
		Files: []library.InputFile{
			{SourcePath: textPath, Role: library.RoleOriginal, Name: "original.txt"},
		},
	})
	if err != nil {
		return nil, s.failIngestion(ctx, ingestionID, "LIBRARY_COMMIT_FAILED", err)
	}
	if _, err := s.db.SQL().ExecContext(ctx, `
        UPDATE capture_ingestions
           SET state = 'sealed', package_id = ?, error_code = NULL, updated_at = ?
         WHERE id = ? AND state IN ('planned','failed')`,
		commit.PackageID, system.FormatTimestamp(now), ingestionID); err != nil {
		return nil, sqlite.Classify(err)
	}

	return s.registerCapture(ctx, wc, layout, now, ingestionID, commit, in, text, canonicalSHA)
}

// registerCapture is the one real transaction: document, its file row, the
// inbox item, the event and the idempotency ledger all commit together or not
// at all. Until it commits, the sealed bytes are safe but unreferenced, and
// v_intake_quality_issues reports them as sealed_without_registration.
func (s *Store) registerCapture(ctx context.Context, wc WriteContext, layout system.Layout,
	now time.Time, ingestionID string, commit *library.CommitResult,
	in CaptureTextInput, text, canonicalSHA string) (*Result, error) {

	payloadHash, err := system.PayloadHash(in)
	if err != nil {
		return nil, protocol.Wrap(err, protocol.CodeInternal, "cannot hash payload")
	}

	var out *Result
	err = s.db.InTx(ctx, func(tx *sql.Tx) error {
		if err := claimRequest(ctx, tx, wc, "inbox.capture-text", payloadHash, now); err != nil {
			return err
		}
		ts := system.FormatTimestamp(now)
		docID := system.NewID("doc")
		canonicalURL := ""
		if strings.HasPrefix(in.SourceRef, "http://") || strings.HasPrefix(in.SourceRef, "https://") {
			canonicalURL = in.SourceRef
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO documents (id, kind, title, captured_at, lineage_id, source,
                                   canonical_url, version, created_at, updated_at)
            VALUES (?, 'other', ?, ?, ?, ?, ?, 1, ?, ?)`,
			docID, in.Title, ts, docID, nullString(in.SourceRef),
			nullString(canonicalURL), ts, ts); err != nil {
			return err
		}

		asset := commit.Manifest.Assets[0]
		relPath, err := filepath.Rel(layout.Library(),
			filepath.Join(commit.FinalPath, asset.RelativePath))
		if err != nil {
			return protocol.Wrap(err, protocol.CodeIntegrity,
				"cannot compute a library-relative path for the captured text")
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO document_files (id, doc_id, rel_path, mime, size_bytes, sha256,
                                        role, sort_order, package_id, created_at)
            VALUES (?,?,?,?,?,?, 'original', 0, ?, ?)`,
			system.NewID("df"), docID, filepath.ToSlash(relPath), "text/plain; charset=utf-8",
			asset.SizeBytes, asset.SHA256, commit.PackageID, ts); err != nil {
			return err
		}

		inboxID := system.NewID("inbox")
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO inbox_items (id, package_id, document_id, capture_kind, source_ref,
                                     title, status, version, created_at, updated_at)
            VALUES (?,?,?, 'text', ?,?, 'captured', 1, ?, ?)`,
			inboxID, commit.PackageID, docID, nullString(in.SourceRef), in.Title,
			ts, ts); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
            UPDATE capture_ingestions
               SET state = 'registered', document_id = ?, inbox_id = ?, updated_at = ?
             WHERE id = ?`, docID, inboxID, ts, ingestionID); err != nil {
			return err
		}
		if err := recordEvent(ctx, tx, wc, now, "document", docID, "created", nil,
			map[string]any{"capture": "inbox.capture-text", "inbox_id": inboxID}); err != nil {
			return err
		}

		out = &Result{
			Data: CaptureTextResult{
				IngestionID: ingestionID, PackageID: commit.PackageID, DocumentID: docID,
				InboxID: inboxID, CanonicalSHA: canonicalSHA, Bytes: len(text),
			},
			Changes: []protocol.Change{
				{EntityType: "document", EntityID: docID, EventType: "created", Version: 1,
					ProjectionKeys: []string{"documents", "inbox"}},
			},
		}
		return completeRequest(ctx, tx, wc.RequestID, out, now)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type ingestionRecord struct {
	ID           string
	State        string
	CanonicalSHA string
	PackageID    string
	DocumentID   string
	InboxID      string
}

func (s *Store) loadIngestionByRequest(ctx context.Context, requestID string) (*ingestionRecord, error) {
	var rec ingestionRecord
	var pkg, doc, inbox sql.NullString
	err := s.db.SQL().QueryRowContext(ctx, `
        SELECT id, state, canonical_text_sha, package_id, document_id, inbox_id
          FROM capture_ingestions WHERE request_id = ?`, requestID).
		Scan(&rec.ID, &rec.State, &rec.CanonicalSHA, &pkg, &doc, &inbox)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	rec.PackageID, rec.DocumentID, rec.InboxID = pkg.String, doc.String, inbox.String
	return &rec, nil
}

// failIngestion records why a capture stopped and returns the original error.
// The row stays: a failed capture that left bytes somewhere is exactly what
// `library verify` and the quality view need to be able to see.
func (s *Store) failIngestion(ctx context.Context, ingestionID, code string, cause error) error {
	_, _ = s.db.SQL().ExecContext(ctx, `
        UPDATE capture_ingestions SET state = 'failed', error_code = ?, updated_at = ?
         WHERE id = ? AND state <> 'registered'`,
		code, system.FormatTimestamp(s.clock.Now()), ingestionID)
	return cause
}

// loadOriginalBytes reads back the sealed original a document points at. Every
// locator in a proposal is verified against these exact bytes before confirm
// writes anything, which is what makes "click a fact, see the sentence it came
// from" a guarantee rather than a hope.
func loadOriginalBytes(ctx context.Context, tx *sql.Tx, layout system.Layout, documentID string) ([]byte, error) {
	var relPath string
	err := tx.QueryRowContext(ctx, `
        SELECT rel_path FROM document_files
         WHERE doc_id = ? AND role = 'original'
         ORDER BY sort_order LIMIT 1`, documentID).Scan(&relPath)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("document %s has no original file", documentID)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	full := filepath.Join(layout.Library(), filepath.FromSlash(relPath))
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, protocol.Wrap(err, protocol.CodeIntegrity,
			"cannot read the sealed original this evidence points at")
	}
	return data, nil
}
