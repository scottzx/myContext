package ops

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// TrigramMinRunes is the shortest query the FTS index can answer.
//
// documents_fts uses the trigram tokenizer because it is the only one that
// finds Chinese substrings without a word segmenter (007_document_search.sql
// records the measurements). Trigram builds three-character grams, so a query
// of one or two characters forms no complete gram and MATCH returns nothing --
// not "no results", but structurally cannot match.
//
// That is not an edge case for this corpus. 杨总 / 李总 / 王总 is how key
// contacts are referred to, and AI is two characters, so short queries are a
// main path. Below this length Search falls back to a LIKE scan, which is
// slower but correct. Verified by TestFTS5TrigramRequiresThreeCharacters.
const TrigramMinRunes = 3

// SearchMode reports which path answered a query, so callers (and the user)
// can tell an exhaustive index hit from a fallback scan.
type SearchMode string

const (
	// SearchModeIndex means the FTS5 trigram index answered.
	SearchModeIndex SearchMode = "index"
	// SearchModeScan means the query was too short for a trigram and a LIKE
	// scan answered instead.
	SearchModeScan SearchMode = "scan"
)

// DocumentHit is one search result. Snippet is a short excerpt for checking
// the match, never a substitute for opening the original.
//
// IsCurrent and SupersededBy are the difference between "these words appear in
// a document" and "this is what we currently think". A lineage keeps every
// version (005), so a replaced proposal matches the same terms as the proposal
// that replaced it. Both are returned - suppressing the old one would be the
// silent overwrite this system refuses everywhere else - but the caller is
// told which is which, and where to read instead.
type DocumentHit struct {
	DocID   string `json:"doc_id"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	RelPath string `json:"rel_path,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	// IsCurrent reports whether this document is the newest version of its
	// lineage, per v_document_current.
	IsCurrent bool `json:"is_current"`
	// SupersededBy names the current version to read instead. Empty when this
	// hit is already the current one.
	SupersededBy string `json:"superseded_by,omitempty"`
	// ReviewAt is the document's own "re-evaluate on" date, and ReviewDue says
	// that date has arrived. A current document can still be out of date; this
	// is the document saying so itself.
	ReviewAt  string `json:"review_at,omitempty"`
	ReviewDue bool   `json:"review_due,omitempty"`
}

// DocumentSearchResult carries the hits plus how they were found. Mode is part
// of the result rather than a log line: a scan and an index hit have different
// coverage, and a caller that cannot tell them apart will over-trust one.
//
// SupersededHits counts the hits that are not the current version of their
// lineage. It is a summary of the rows, not extra information, and exists so
// that a caller reading only the head of a JSON result still learns that some
// of what follows has been replaced.
type DocumentSearchResult struct {
	Query          string        `json:"query"`
	Mode           SearchMode    `json:"mode"`
	Hits           []DocumentHit `json:"hits"`
	SupersededHits int           `json:"superseded_hits"`
}

// SearchDocuments finds documents by content. It picks its path from the
// query length: see TrigramMinRunes for why that branch has to exist.
func (s *Store) SearchDocuments(ctx context.Context, query string, limit int) (*DocumentSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, protocol.BadInput("a search query is required")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	// The currency columns compare review_at against the same day the rest of
	// the read layer uses, so a search and `ops status` cannot disagree about
	// what "due" means.
	today := s.clock.Today()

	if utf8.RuneCountInString(query) >= TrigramMinRunes {
		hits, err := s.searchViaIndex(ctx, query, today, limit)
		if err != nil {
			return nil, err
		}
		return newSearchResult(query, SearchModeIndex, hits), nil
	}
	hits, err := s.searchViaScan(ctx, query, today, limit)
	if err != nil {
		return nil, err
	}
	return newSearchResult(query, SearchModeScan, hits), nil
}

func newSearchResult(query string, mode SearchMode, hits []DocumentHit) *DocumentSearchResult {
	superseded := 0
	for _, h := range hits {
		if !h.IsCurrent {
			superseded++
		}
	}
	return &DocumentSearchResult{Query: query, Mode: mode, Hits: hits, SupersededHits: superseded}
}

// hitJoins is shared by both search paths so an index hit and a scan hit
// cannot describe the same document differently.
//
// v_document_current holds exactly one row per lineage, so joining on
// lineage_id yields the current sibling of whatever matched - which is both
// the is_current test and the "read this instead" pointer, in one join.
const hitJoins = `
          FROM documents_fts f
          JOIN documents d ON d.id = f.doc_id
     LEFT JOIN v_document_search_source src ON src.doc_id = f.doc_id
     LEFT JOIN v_document_current c ON c.lineage_id = d.lineage_id`

// hitCurrencyColumns are the four currency columns, in scanDocumentHits order.
// The `?` is the day to compare review_at against.
const hitCurrencyColumns = `
               CASE WHEN c.document_id = d.id THEN 1 ELSE 0 END,
               CASE WHEN c.document_id IS NOT NULL AND c.document_id <> d.id
                    THEN c.document_id ELSE '' END,
               COALESCE(d.review_at, ''),
               CASE WHEN d.review_at IS NOT NULL AND d.review_at <= ? THEN 1 ELSE 0 END`

// searchViaIndex asks the trigram index. The query is passed as a quoted FTS5
// string so punctuation in it is data, never operator syntax.
func (s *Store) searchViaIndex(ctx context.Context, query, today string, limit int) ([]DocumentHit, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT f.doc_id,
               d.title,
               d.kind,
               COALESCE(src.rel_path, ''),
               snippet(documents_fts, 2, '[', ']', '…', 12),`+hitCurrencyColumns+hitJoins+`
         WHERE documents_fts MATCH ?
         ORDER BY rank
         LIMIT ?`, today, fts5Phrase(query), limit)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return scanDocumentHits(rows)
}

// searchViaScan is the short-query path. It reads the same body text out of
// the index table rather than re-reading files, so both paths see one corpus.
func (s *Store) searchViaScan(ctx context.Context, query, today string, limit int) ([]DocumentHit, error) {
	like := "%" + escapeLike(query) + "%"
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT f.doc_id,
               d.title,
               d.kind,
               COALESCE(src.rel_path, ''),
               '',`+hitCurrencyColumns+hitJoins+`
         WHERE f.body LIKE ? ESCAPE '\' OR f.title LIKE ? ESCAPE '\'
         ORDER BY d.updated_at DESC
         LIMIT ?`, today, like, like, limit)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return scanDocumentHits(rows)
}

func scanDocumentHits(rows *sql.Rows) ([]DocumentHit, error) {
	defer rows.Close()
	out := []DocumentHit{}
	for rows.Next() {
		var h DocumentHit
		if err := rows.Scan(&h.DocID, &h.Title, &h.Kind, &h.RelPath, &h.Snippet,
			&h.IsCurrent, &h.SupersededBy, &h.ReviewAt, &h.ReviewDue); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, h)
	}
	return out, sqlite.Classify(rows.Err())
}

// fts5Phrase wraps a query as a single quoted FTS5 phrase. Inside double
// quotes every character is literal, so a doubled quote is the only escape
// needed and nothing the user typed can act as a query operator.
func fts5Phrase(query string) string {
	return `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
}

// IndexDocumentInput carries the extracted text for one document.
type IndexDocumentInput struct {
	DocumentID string `json:"document_id"`
	Body       string `json:"body"`
}

// IndexDocument puts a document's text into the search index, replacing any
// earlier copy so re-indexing is idempotent rather than additive.
//
// The text is supplied by the caller instead of read here: documents store no
// body column, the bytes live in the library as a file, and reading files is
// the library package's job. This keeps the domain layer free of filesystem
// access, which is what lets both be tested independently.
func (s *Store) IndexDocument(ctx context.Context, wc WriteContext, in IndexDocumentInput) (*Result, error) {
	if in.DocumentID == "" {
		return nil, protocol.BadInput("document_id is required")
	}
	return s.execute(ctx, "doc.index", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		doc, err := loadDocument(ctx, tx, in.DocumentID)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM documents_fts WHERE doc_id = ?`, in.DocumentID); err != nil {
			return nil, sqlite.Classify(err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO documents_fts (doc_id, title, body) VALUES (?,?,?)`,
			in.DocumentID, doc.Title, in.Body); err != nil {
			return nil, sqlite.Classify(err)
		}
		// Record which bytes this text belongs to, in the same transaction as
		// the text itself: an index entry and a claim about its provenance
		// must never be able to disagree.
		//
		// The hash is read from the original file here rather than accepted
		// from the caller, which bounds what staleness can detect: that the
		// FILE changed after indexing, not that the caller supplied text from
		// somewhere else. Verifying the body against the bytes would mean
		// reading the file, and this layer does not touch the filesystem.
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO document_index_state (doc_id, indexed_sha256, indexed_at)
            VALUES (?, (SELECT f.sha256 FROM document_files f
                         WHERE f.doc_id = ? AND f.role = 'original'), ?)
            ON CONFLICT(doc_id) DO UPDATE SET
                indexed_sha256 = excluded.indexed_sha256,
                indexed_at     = excluded.indexed_at`,
			in.DocumentID, in.DocumentID, system.FormatTimestamp(now)); err != nil {
			return nil, sqlite.Classify(err)
		}
		return &Result{
			Data: doc,
			Changes: []protocol.Change{{EntityType: "document", EntityID: in.DocumentID,
				EventType: "updated", Version: doc.Version,
				ProjectionKeys: []string{"documents", "search"}}},
		}, nil
	})
}

// Reasons a document is in the index queue. They are distinct because they
// describe different amounts of damage: not_indexed means content search
// cannot find the document at all, while the other two mean it can be found -
// by text that may no longer be the document's.
const (
	// IndexReasonNotIndexed: an original file exists, its text was never
	// supplied. Findable by metadata, not by content.
	IndexReasonNotIndexed = "not_indexed"
	// IndexReasonContentChanged: the original's hash moved after indexing, so
	// the index is serving text that is provably not the current file.
	IndexReasonContentChanged = "content_changed"
	// IndexReasonProvenanceUnknown: indexed text with no recorded hash behind
	// it. It may be current; nothing recorded can establish that.
	IndexReasonProvenanceUnknown = "provenance_unknown"
)

// IndexQueueEntry is one document whose search index needs work, and why.
//
// It is deliberately not a DocumentHit: a hit answers "what did the search
// find", this answers "what is the index missing or wrong about". Sharing one
// struct would have forced every queue row to carry currency fields it has no
// business asserting.
type IndexQueueEntry struct {
	DocID   string `json:"doc_id"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	RelPath string `json:"rel_path,omitempty"`
	Reason  string `json:"reason"`
	// The two hashes, on content_changed rows, so the report can be checked
	// against the files rather than believed.
	IndexedSHA256 string `json:"indexed_sha256,omitempty"`
	CurrentSHA256 string `json:"current_sha256,omitempty"`
}

// DocumentsNeedingIndex lists documents whose indexed text is missing or can
// no longer be trusted to be theirs. It is a maintenance queue, not a failure:
// every row is still a legitimate document, and the fix is to feed its text
// back through `doc index`.
//
// Re-indexing needs the file's bytes, and reading files is not this layer's
// job (007), so this reports what to re-index rather than doing it.
func (s *Store) DocumentsNeedingIndex(ctx context.Context) ([]IndexQueueEntry, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT m.doc_id AS doc_id, m.title AS title, d.kind AS kind,
               m.rel_path AS rel_path, ? AS reason,
               '' AS indexed_sha256, '' AS current_sha256
          FROM v_document_search_missing m
          JOIN documents d ON d.id = m.doc_id
        UNION ALL
        SELECT st.doc_id, st.title, d.kind, st.rel_path, st.reason,
               COALESCE(st.indexed_sha256, ''), COALESCE(st.current_sha256, '')
          FROM v_document_search_stale st
          JOIN documents d ON d.id = st.doc_id
         ORDER BY reason, title
         LIMIT 500`, IndexReasonNotIndexed)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []IndexQueueEntry{}
	for rows.Next() {
		var e IndexQueueEntry
		if err := rows.Scan(&e.DocID, &e.Title, &e.Kind, &e.RelPath, &e.Reason,
			&e.IndexedSHA256, &e.CurrentSHA256); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, e)
	}
	return out, sqlite.Classify(rows.Err())
}
