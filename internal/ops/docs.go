package ops

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/library"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// ---------------------------------------------------------------------------
// documents + document_files: one row per version of one artefact, plus the
// bytes on disk.
// ---------------------------------------------------------------------------

// AddDocumentInput is the payload of `document.add`. Supplying SupersedesID
// makes this a new version in an existing lineage rather than a new one;
// RelPath, if given, attaches the first file (normally the 'original') in
// the same call.
type AddDocumentInput struct {
	Kind         string `json:"kind,omitempty"`
	Title        string `json:"title"`
	OccurredAt   string `json:"occurred_at,omitempty"`
	CapturedAt   string `json:"captured_at,omitempty"`
	ReviewAt     string `json:"review_at,omitempty"`
	SupersedesID string `json:"supersedes_id,omitempty"`
	ChangeNote   string `json:"change_note,omitempty"`
	Source       string `json:"source,omitempty"`
	AuthorName   string `json:"author_name,omitempty"`
	CanonicalURL string `json:"canonical_url,omitempty"`
	UserNote     string `json:"user_note,omitempty"`
	LegacyRef    string `json:"legacy_ref,omitempty"`
	RelPath      string `json:"rel_path,omitempty"`
	Mime         string `json:"mime,omitempty"`
	SizeBytes    *int64 `json:"size_bytes,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	FileRole     string `json:"file_role,omitempty"`
}

func (in *AddDocumentInput) normalize() error {
	if in.Title == "" {
		return protocol.BadInput("title is required")
	}
	if in.Kind == "" {
		in.Kind = "other"
	}
	if !validDocumentKind[in.Kind] {
		return protocol.BadInput("kind %q is not valid", in.Kind)
	}
	if in.OccurredAt != "" {
		if err := ValidateTimestamp("occurred_at", in.OccurredAt); err != nil {
			return err
		}
	}
	if in.CapturedAt != "" {
		if err := ValidateTimestamp("captured_at", in.CapturedAt); err != nil {
			return err
		}
	}
	if in.ReviewAt != "" {
		if err := ValidateDate("review_at", in.ReviewAt); err != nil {
			return err
		}
	}
	if in.RelPath != "" {
		if strings.HasPrefix(in.RelPath, "/") {
			return protocol.BadInput("rel_path must be relative to the library root")
		}
		if in.FileRole == "" {
			in.FileRole = "original"
		}
		if !validDocumentFileRole[in.FileRole] {
			return protocol.BadInput("file_role must be original|rendition|attachment")
		}
		if in.SizeBytes != nil && *in.SizeBytes < 0 {
			return protocol.BadInput("size_bytes cannot be negative")
		}
		if in.SHA256 != "" && (len(in.SHA256) != 64 || strings.ToLower(in.SHA256) != in.SHA256) {
			return protocol.BadInput("sha256 must be 64 lowercase hex characters")
		}
	} else if in.FileRole != "" {
		return protocol.BadInput("file_role requires rel_path")
	}
	return nil
}

// AddDocumentAttachment is one extra file captured alongside a document's
// primary SourceFile - the .pdf rendition of an .html original, a screenshot
// kept as evidence. Role is a document_files.role value, never a library
// asset role: see CaptureDocument for how the two vocabularies map.
type AddDocumentAttachment struct {
	SourcePath string `json:"source_path"`
	Role       string `json:"role,omitempty"` // rendition|attachment; default rendition
}

// CaptureDocumentInput is the payload of `document.capture`: like
// AddDocumentInput, but SourceFile (and optionally Attachments) name real
// files to ingest through the Library's recoverable commit (technical design
// §15), rather than a RelPath the caller has already placed under the
// library root.
type CaptureDocumentInput struct {
	AddDocumentInput
	SourceFile  string                  `json:"source_file"`
	Attachments []AddDocumentAttachment `json:"attachments,omitempty"`
}

func (in *CaptureDocumentInput) normalize() error {
	if in.RelPath != "" || in.FileRole != "" {
		return protocol.BadInput("--path registers a file already inside the library; " +
			"use --file to capture a new file, not both")
	}
	if err := in.AddDocumentInput.normalize(); err != nil {
		return err
	}
	if strings.TrimSpace(in.SourceFile) == "" {
		return protocol.BadInput("source_file is required")
	}
	for i := range in.Attachments {
		if strings.TrimSpace(in.Attachments[i].SourcePath) == "" {
			return protocol.BadInput("attachment %d: source_path is required", i)
		}
		if in.Attachments[i].Role == "" {
			in.Attachments[i].Role = "rendition"
		}
		if in.Attachments[i].Role != "rendition" && in.Attachments[i].Role != "attachment" {
			return protocol.BadInput("attachment %d: role must be rendition or attachment", i)
		}
	}
	return nil
}

// libraryAssetRole maps a document_files.role (the business-level tag: which
// file is the authoritative text vs. an alternate rendition vs. supporting
// material) to the fixed physical folder a Capture Package files it under
// (library.Role*, B+ design §9.4). The two vocabularies are deliberately
// different: 'rendition' and 'original' are both primary, user-supplied
// content so both land under RoleOriginal; only 'attachment' - genuinely
// supporting material - goes under RoleAttachments.
func libraryAssetRole(documentFileRole string) string {
	if documentFileRole == "attachment" {
		return library.RoleAttachments
	}
	return library.RoleOriginal
}

// CaptureDocument ingests SourceFile (and any Attachments) into the Library
// via library.Commit - stage, hash, immutable manifest, atomic rename, seal
// (§15.1) - then records the document and one document_files row per
// captured asset in a single write. This is what makes `doc add --file`
// put the file INTO the library because the tool did it, instead of
// requiring the caller to have pre-copied it under library/YYYY/MM/DD first.
//
// The Library commit runs before, not inside, the ops.execute transaction
// below: it is its own journalled file transaction against library_packages,
// driven by the same wc.RequestID. A retried request_id replays both halves
// idempotently - library.Commit returns the original package without
// recopying bytes, and ops.execute's own idempotency ledger returns the
// original document without inserting a second row - so calling this twice
// with the same request_id is always safe.
func (s *Store) CaptureDocument(ctx context.Context, wc WriteContext, layout system.Layout, in CaptureDocumentInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	if err := wc.validate(); err != nil {
		return nil, err
	}

	files := make([]library.InputFile, 0, 1+len(in.Attachments))
	businessRoles := make([]string, 0, 1+len(in.Attachments))

	files = append(files, library.InputFile{
		SourcePath: in.SourceFile,
		Role:       library.RoleOriginal,
		Name:       filepath.Base(in.SourceFile),
	})
	businessRoles = append(businessRoles, "original")

	for _, a := range in.Attachments {
		files = append(files, library.InputFile{
			SourcePath: a.SourcePath,
			Role:       libraryAssetRole(a.Role),
			Name:       filepath.Base(a.SourcePath),
		})
		businessRoles = append(businessRoles, a.Role)
	}

	journal := s.NewLibraryJournal()
	commit, err := library.Commit(ctx, layout, journal, s.clock, library.CaptureInput{
		RequestID:     wc.RequestID,
		CaptureMethod: "cli:doc.capture",
		Files:         files,
	})
	if err != nil {
		return nil, err
	}

	return s.execute(ctx, "document.capture", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		lineageID := ""
		if in.SupersedesID != "" {
			prev, err := loadDocument(ctx, tx, in.SupersedesID)
			if err != nil {
				return nil, err
			}
			lineageID = prev.LineageID
		}
		id := system.NewID("doc")
		if lineageID == "" {
			lineageID = id
		}
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO documents (id, kind, title, occurred_at, captured_at, review_at,
                                   lineage_id, supersedes_id, change_note, source, author_name,
                                   canonical_url, user_note, legacy_ref, version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			id, in.Kind, in.Title, nullString(in.OccurredAt), nullString(in.CapturedAt),
			nullString(in.ReviewAt), lineageID, nullString(in.SupersedesID), nullString(in.ChangeNote),
			nullString(in.Source), nullString(in.AuthorName), nullString(in.CanonicalURL),
			nullString(in.UserNote), nullString(in.LegacyRef), ts, ts); err != nil {
			return nil, err
		}

		for i, asset := range commit.Manifest.Assets {
			relPath, err := filepath.Rel(layout.Library(), filepath.Join(commit.FinalPath, asset.RelativePath))
			if err != nil {
				return nil, protocol.Wrap(err, protocol.CodeIntegrity, "cannot compute a library-relative path for a captured asset")
			}
			relPath = filepath.ToSlash(relPath)
			fileID := system.NewID("df")
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO document_files (id, doc_id, rel_path, mime, size_bytes, sha256,
                                            role, sort_order, package_id, created_at)
                VALUES (?,?,?,?,?,?,?,?,?,?)`,
				fileID, id, relPath, nullString(asset.MimeType), asset.SizeBytes,
				nullString(asset.SHA256), businessRoles[i], i, commit.PackageID, ts); err != nil {
				return nil, err
			}
		}

		if err := recordEvent(ctx, tx, wc, now, "document", id, "created", nil, in); err != nil {
			return nil, err
		}
		doc, err := loadDocument(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: doc,
			Changes: []protocol.Change{{EntityType: "document", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"documents"}}},
		}, nil
	})
}

// AddDocument records one version of one artefact. It stores no path itself
// - when RelPath is given, the bytes are recorded as a document_files row in
// the same write.
func (s *Store) AddDocument(ctx context.Context, wc WriteContext, in AddDocumentInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "document.add", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		lineageID := ""
		if in.SupersedesID != "" {
			prev, err := loadDocument(ctx, tx, in.SupersedesID)
			if err != nil {
				return nil, err
			}
			lineageID = prev.LineageID
		}
		id := system.NewID("doc")
		if lineageID == "" {
			lineageID = id
		}
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO documents (id, kind, title, occurred_at, captured_at, review_at,
                                   lineage_id, supersedes_id, change_note, source, author_name,
                                   canonical_url, user_note, legacy_ref, version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			id, in.Kind, in.Title, nullString(in.OccurredAt), nullString(in.CapturedAt),
			nullString(in.ReviewAt), lineageID, nullString(in.SupersedesID), nullString(in.ChangeNote),
			nullString(in.Source), nullString(in.AuthorName), nullString(in.CanonicalURL),
			nullString(in.UserNote), nullString(in.LegacyRef), ts, ts); err != nil {
			return nil, err
		}
		if in.RelPath != "" {
			fileID := system.NewID("df")
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO document_files (id, doc_id, rel_path, mime, size_bytes, sha256,
                                            role, sort_order, created_at)
                VALUES (?,?,?,?,?,?,?,0,?)`,
				fileID, id, in.RelPath, nullString(in.Mime), nullInt64(in.SizeBytes),
				nullString(in.SHA256), in.FileRole, ts); err != nil {
				return nil, err
			}
		}
		if err := recordEvent(ctx, tx, wc, now, "document", id, "created", nil, in); err != nil {
			return nil, err
		}
		doc, err := loadDocument(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: doc,
			Changes: []protocol.Change{{EntityType: "document", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"documents"}}},
		}, nil
	})
}

const documentColumns = `
    id, kind, title, occurred_at, captured_at, review_at, lineage_id, supersedes_id,
    change_note, source, author_name, canonical_url, user_note, legacy_ref,
    version, created_at, updated_at`

func scanDocument(row interface{ Scan(...any) error }) (*Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.Kind, &d.Title, &d.OccurredAt, &d.CapturedAt, &d.ReviewAt,
		&d.LineageID, &d.SupersedesID, &d.ChangeNote, &d.Source, &d.AuthorName,
		&d.CanonicalURL, &d.UserNote, &d.LegacyRef, &d.Version, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func loadDocument(ctx context.Context, tx *sql.Tx, id string) (*Document, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+documentColumns+` FROM documents WHERE id = ?`, id)
	d, err := scanDocument(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("document %s does not exist", id)
	}
	return d, err
}

// GetDocument loads one document by id.
func (s *Store) GetDocument(ctx context.Context, id string) (*Document, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+documentColumns+` FROM documents WHERE id = ?`, id)
	d, err := scanDocument(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("document %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return d, nil
}

// DocumentFilter is the query surface of `mycontext document list`.
type DocumentFilter struct {
	Kind   string
	Search string
	Limit  int
}

// ListDocuments returns documents, newest first. It lists every version, not
// just the current one - use v_document_current at the read layer for that.
func (s *Store) ListDocuments(ctx context.Context, f DocumentFilter) ([]*Document, error) {
	query := `SELECT ` + documentColumns + ` FROM documents WHERE 1=1`
	var args []any
	if f.Kind != "" {
		query += " AND kind = ?"
		args = append(args, f.Kind)
	}
	if f.Search != "" {
		query += " AND title LIKE ? ESCAPE '\\'"
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, d)
	}
	return out, sqlite.Classify(rows.Err())
}

// ListDocumentFiles returns the files belonging to one document.
func (s *Store) ListDocumentFiles(ctx context.Context, docID string) ([]DocumentFile, error) {
	if docID == "" {
		return nil, protocol.BadInput("doc_id is required")
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT id, doc_id, rel_path, mime, size_bytes, sha256, role, sort_order, created_at
          FROM document_files WHERE doc_id = ? ORDER BY role, sort_order`, docID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []DocumentFile{}
	for rows.Next() {
		var f DocumentFile
		if err := rows.Scan(&f.ID, &f.DocID, &f.RelPath, &f.Mime, &f.SizeBytes, &f.SHA256,
			&f.Role, &f.SortOrder, &f.CreatedAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, f)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// doc_links: a document hangs off any business object.
// ---------------------------------------------------------------------------

// LinkDocumentInput is the payload of `document.link`.
type LinkDocumentInput struct {
	DocID      string `json:"doc_id"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	LinkType   string `json:"link_type,omitempty"`
}

// LinkDocument hangs a document off any business object - the Go equivalent
// of the legacy habit of putting `TASK 104 v0.1` in a filename.
func (s *Store) LinkDocument(ctx context.Context, wc WriteContext, in LinkDocumentInput) (*Result, error) {
	if in.DocID == "" || in.EntityID == "" {
		return nil, protocol.BadInput("doc_id and entity_id are required")
	}
	if !validEntityType[in.EntityType] {
		return nil, protocol.BadInput("entity_type must be one of %s", EntityTypeList())
	}
	if in.LinkType == "" {
		in.LinkType = "attachment"
	}
	if !validDocLinkType[in.LinkType] {
		return nil, protocol.BadInput("link_type must be dossier|minutes|evidence|attachment|deliverable")
	}
	return s.execute(ctx, "document.link", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, "documents", in.DocID, "document"); err != nil {
			return nil, err
		}
		if err := requireExists(ctx, tx, entityTables[in.EntityType], in.EntityID, in.EntityType); err != nil {
			return nil, err
		}
		id := system.NewID("dl")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO doc_links (id, doc_id, entity_type, entity_id, link_type, created_at)
            VALUES (?,?,?,?,?,?)
            ON CONFLICT(doc_id, entity_type, entity_id, link_type) DO NOTHING`,
			id, in.DocID, in.EntityType, in.EntityID, in.LinkType, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, in.EntityType, in.EntityID, "linked", nil, in); err != nil {
			return nil, err
		}
		return &Result{
			Data: DocLink{ID: id, DocID: in.DocID, EntityType: in.EntityType, EntityID: in.EntityID,
				LinkType: in.LinkType, CreatedAt: ts},
			Changes: []protocol.Change{{EntityType: in.EntityType, EntityID: in.EntityID,
				EventType: "linked", ProjectionKeys: []string{"doc_links"}}},
		}, nil
	})
}

// ListDocLinks returns the documents linked to one business object.
func (s *Store) ListDocLinks(ctx context.Context, entityType, entityID string) ([]DocLink, error) {
	if entityID == "" {
		return nil, protocol.BadInput("entity_id is required")
	}
	if !validEntityType[entityType] {
		return nil, protocol.BadInput("entity_type must be one of %s", EntityTypeList())
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT id, doc_id, entity_type, entity_id, link_type, created_at
          FROM doc_links WHERE entity_type = ? AND entity_id = ? ORDER BY created_at`,
		entityType, entityID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []DocLink{}
	for rows.Next() {
		var l DocLink
		if err := rows.Scan(&l.ID, &l.DocID, &l.EntityType, &l.EntityID, &l.LinkType, &l.CreatedAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, l)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// metric_samples: any number, about anything, over time.
// ---------------------------------------------------------------------------

// RecordMetricSampleInput is the payload of `metric.record`.
type RecordMetricSampleInput struct {
	SubjectType string  `json:"subject_type"`
	SubjectID   string  `json:"subject_id"`
	MetricName  string  `json:"metric_name"`
	SampledAt   string  `json:"sampled_at"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit,omitempty"`
	Source      string  `json:"source,omitempty"`
	Note        string  `json:"note,omitempty"`
}

func (in *RecordMetricSampleInput) normalize() error {
	if in.SubjectType == "" || in.SubjectID == "" {
		return protocol.BadInput("subject_type and subject_id are required")
	}
	if !validEntityType[in.SubjectType] {
		return protocol.BadInput("subject_type must be one of %s", EntityTypeList())
	}
	if strings.TrimSpace(in.MetricName) == "" || strings.TrimSpace(in.MetricName) != in.MetricName {
		return protocol.BadInput("metric_name must be non-empty and trimmed")
	}
	if in.SampledAt == "" {
		return protocol.BadInput("sampled_at is required")
	}
	return nil
}

// RecordMetricSample writes one observation. Recording the same subject,
// metric and instant again is a CORRECTION - the unique index on those three
// columns makes this an upsert rather than a second row.
func (s *Store) RecordMetricSample(ctx context.Context, wc WriteContext, in RecordMetricSampleInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "metric.record", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, entityTables[in.SubjectType], in.SubjectID, in.SubjectType); err != nil {
			return nil, err
		}
		id := system.NewID("msp")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO metric_samples (id, subject_type, subject_id, metric_name, sampled_at,
                                        value, unit, source, note, created_at)
            VALUES (?,?,?,?,?,?,?,?,?,?)
            ON CONFLICT(subject_type, subject_id, metric_name, sampled_at) DO UPDATE SET
                value = excluded.value, unit = excluded.unit, source = excluded.source,
                note = excluded.note`,
			id, in.SubjectType, in.SubjectID, in.MetricName, in.SampledAt, in.Value,
			nullString(in.Unit), nullString(in.Source), nullString(in.Note), ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, in.SubjectType, in.SubjectID, "metric_updated", nil, in); err != nil {
			return nil, err
		}
		m := MetricSample{ID: id, SubjectType: in.SubjectType, SubjectID: in.SubjectID,
			MetricName: in.MetricName, SampledAt: in.SampledAt, Value: in.Value, CreatedAt: ts}
		assignOptional(&m.Unit, in.Unit)
		assignOptional(&m.Source, in.Source)
		assignOptional(&m.Note, in.Note)
		return &Result{
			Data: m,
			Changes: []protocol.Change{{EntityType: in.SubjectType, EntityID: in.SubjectID,
				EventType: "metric_updated", ProjectionKeys: []string{"metrics"}}},
		}, nil
	})
}

// ListMetricSamples returns the series for one subject and metric, in order.
func (s *Store) ListMetricSamples(ctx context.Context, subjectType, subjectID, metricName string) ([]MetricSample, error) {
	if subjectID == "" || metricName == "" {
		return nil, protocol.BadInput("subject_id and metric_name are required")
	}
	if !validEntityType[subjectType] {
		return nil, protocol.BadInput("subject_type must be one of %s", EntityTypeList())
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT id, subject_type, subject_id, metric_name, sampled_at, value, unit, source, note, created_at
          FROM metric_samples
         WHERE subject_type = ? AND subject_id = ? AND metric_name = ?
         ORDER BY sampled_at`, subjectType, subjectID, metricName)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []MetricSample{}
	for rows.Next() {
		var m MetricSample
		if err := rows.Scan(&m.ID, &m.SubjectType, &m.SubjectID, &m.MetricName, &m.SampledAt,
			&m.Value, &m.Unit, &m.Source, &m.Note, &m.CreatedAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, m)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// context_edges: SOFT relations only.
// ---------------------------------------------------------------------------

// AddContextEdgeInput is the payload of `edge.add`.
type AddContextEdgeInput struct {
	FromType string `json:"from_type"`
	FromID   string `json:"from_id"`
	ToType   string `json:"to_type"`
	ToID     string `json:"to_id"`
	EdgeType string `json:"edge_type,omitempty"`
	Note     string `json:"note,omitempty"`
}

// AddContextEdge records a soft relation - a referral, a fork, a citation.
// Unlike AddDependency, this never gates anything and never checks for
// cycles: it states a relation, nothing more.
func (s *Store) AddContextEdge(ctx context.Context, wc WriteContext, in AddContextEdgeInput) (*Result, error) {
	if in.FromType == "" || in.FromID == "" || in.ToType == "" || in.ToID == "" {
		return nil, protocol.BadInput("from_type, from_id, to_type and to_id are required")
	}
	if !validEntityType[in.FromType] || !validEntityType[in.ToType] {
		return nil, protocol.BadInput("from_type and to_type must be one of %s", EntityTypeList())
	}
	if in.EdgeType == "" {
		in.EdgeType = "relates_to"
	}
	if !validContextEdgeType[in.EdgeType] {
		return nil, protocol.BadInput("edge_type must be referred_by|derived_from|references|relates_to|inspired_by")
	}
	if in.FromType == in.ToType && in.FromID == in.ToID {
		return nil, protocol.BadInput("a node cannot relate to itself")
	}
	return s.execute(ctx, "edge.add", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, entityTables[in.FromType], in.FromID, in.FromType); err != nil {
			return nil, err
		}
		if err := requireExists(ctx, tx, entityTables[in.ToType], in.ToID, in.ToType); err != nil {
			return nil, err
		}
		id := system.NewID("edge")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO context_edges (id, from_type, from_id, to_type, to_id, edge_type, note, created_at)
            VALUES (?,?,?,?,?,?,?,?)
            ON CONFLICT(from_type, from_id, to_type, to_id, edge_type)
            DO UPDATE SET note = excluded.note`,
			id, in.FromType, in.FromID, in.ToType, in.ToID, in.EdgeType, nullString(in.Note), ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, in.ToType, in.ToID, "linked", nil, in); err != nil {
			return nil, err
		}
		e := ContextEdge{ID: id, FromType: in.FromType, FromID: in.FromID, ToType: in.ToType,
			ToID: in.ToID, EdgeType: in.EdgeType, CreatedAt: ts}
		assignOptional(&e.Note, in.Note)
		return &Result{
			Data: e,
			Changes: []protocol.Change{{EntityType: in.ToType, EntityID: in.ToID,
				EventType: "linked", ProjectionKeys: []string{"context_edges"}}},
		}, nil
	})
}

// ListContextEdges returns edges touching a node, in both directions by
// default.
func (s *Store) ListContextEdges(ctx context.Context, entityType, entityID, direction string) ([]ContextEdge, error) {
	if entityID == "" {
		return nil, protocol.BadInput("entity_id is required")
	}
	if !validEntityType[entityType] {
		return nil, protocol.BadInput("entity_type must be one of %s", EntityTypeList())
	}
	query := `SELECT id, from_type, from_id, to_type, to_id, edge_type, note, created_at
                FROM context_edges WHERE 1=1`
	var args []any
	switch direction {
	case "outgoing":
		query += " AND from_type = ? AND from_id = ?"
		args = append(args, entityType, entityID)
	case "incoming":
		query += " AND to_type = ? AND to_id = ?"
		args = append(args, entityType, entityID)
	default:
		query += " AND ((from_type = ? AND from_id = ?) OR (to_type = ? AND to_id = ?))"
		args = append(args, entityType, entityID, entityType, entityID)
	}
	query += " ORDER BY created_at"

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []ContextEdge{}
	for rows.Next() {
		var e ContextEdge
		if err := rows.Scan(&e.ID, &e.FromType, &e.FromID, &e.ToType, &e.ToID, &e.EdgeType,
			&e.Note, &e.CreatedAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, e)
	}
	return out, sqlite.Classify(rows.Err())
}

func nullInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
