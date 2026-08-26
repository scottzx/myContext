package ops

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
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
type DocumentHit struct {
	DocID   string `json:"doc_id"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	RelPath string `json:"rel_path,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// DocumentSearchResult carries the hits plus how they were found. Mode is part
// of the result rather than a log line: a scan and an index hit have different
// coverage, and a caller that cannot tell them apart will over-trust one.
type DocumentSearchResult struct {
	Query string        `json:"query"`
	Mode  SearchMode    `json:"mode"`
	Hits  []DocumentHit `json:"hits"`
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

	if utf8.RuneCountInString(query) >= TrigramMinRunes {
		hits, err := s.searchViaIndex(ctx, query, limit)
		if err != nil {
			return nil, err
		}
		return &DocumentSearchResult{Query: query, Mode: SearchModeIndex, Hits: hits}, nil
	}
	hits, err := s.searchViaScan(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return &DocumentSearchResult{Query: query, Mode: SearchModeScan, Hits: hits}, nil
}

// searchViaIndex asks the trigram index. The query is passed as a quoted FTS5
// string so punctuation in it is data, never operator syntax.
func (s *Store) searchViaIndex(ctx context.Context, query string, limit int) ([]DocumentHit, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT f.doc_id,
               d.title,
               d.kind,
               COALESCE(src.rel_path, ''),
               snippet(documents_fts, 2, '[', ']', '…', 12)
          FROM documents_fts f
          JOIN documents d ON d.id = f.doc_id
     LEFT JOIN v_document_search_source src ON src.doc_id = f.doc_id
         WHERE documents_fts MATCH ?
         ORDER BY rank
         LIMIT ?`, fts5Phrase(query), limit)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return scanDocumentHits(rows)
}

// searchViaScan is the short-query path. It reads the same body text out of
// the index table rather than re-reading files, so both paths see one corpus.
func (s *Store) searchViaScan(ctx context.Context, query string, limit int) ([]DocumentHit, error) {
	like := "%" + escapeLike(query) + "%"
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT f.doc_id,
               d.title,
               d.kind,
               COALESCE(src.rel_path, ''),
               ''
          FROM documents_fts f
          JOIN documents d ON d.id = f.doc_id
     LEFT JOIN v_document_search_source src ON src.doc_id = f.doc_id
         WHERE f.body LIKE ? ESCAPE '\' OR f.title LIKE ? ESCAPE '\'
         ORDER BY d.updated_at DESC
         LIMIT ?`, like, like, limit)
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
		if err := rows.Scan(&h.DocID, &h.Title, &h.Kind, &h.RelPath, &h.Snippet); err != nil {
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
		return &Result{
			Data: doc,
			Changes: []protocol.Change{{EntityType: "document", EntityID: in.DocumentID,
				EventType: "updated", Version: doc.Version,
				ProjectionKeys: []string{"documents", "search"}}},
		}, nil
	})
}

// UnindexedDocuments lists documents that have an original file but no text in
// the index. They are findable by metadata and not by content until indexed;
// that is a maintenance queue, not a failure.
func (s *Store) UnindexedDocuments(ctx context.Context) ([]DocumentHit, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT m.doc_id, m.title, d.kind, m.rel_path, ''
          FROM v_document_search_missing m
          JOIN documents d ON d.id = m.doc_id
         ORDER BY d.updated_at DESC
         LIMIT 500`)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return scanDocumentHits(rows)
}
