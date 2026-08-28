-- ===========================================================================
-- 011_search_currency.sql
--
-- Two things a content search has to know beyond "the words are in there":
-- whether the hit is still the current conclusion, and whether the indexed
-- text still matches the bytes it was built from.
--
-- The first was already answerable -- v_document_current (005) resolves a
-- lineage to its newest version -- and simply was not asked. Search joined
-- documents directly, so a superseded proposal and the proposal that replaced
-- it ranked side by side with nothing to tell them apart. That is fixed in
-- the query (internal/ops/search.go), not here; no schema was missing.
--
-- The second needs this migration. documents_fts owns a private copy of the
-- extracted text, supplied by the caller at index time (007). Nothing recorded
-- WHICH bytes that copy came from, so once a document gained a new original
-- file the index kept serving the old text and no view could say so:
-- v_document_search_missing answers "never indexed", and there was no way to
-- ask "indexed, but from bytes that are gone".
--
-- The fix is a side table rather than a column on documents_fts. FTS5 virtual
-- tables cannot be ALTERed, and recreating the index would drop every indexed
-- body -- text this system cannot regenerate on its own, because re-indexing
-- needs the file's text handed back in through `doc index`. A silent mass
-- un-indexing is a far worse outcome than the staleness this is meant to
-- detect.
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- document_index_state: which bytes the indexed text was built from.
--
-- indexed_sha256 is the original file's hash AS OF the moment the body was
-- indexed, copied from document_files. It is nullable on purpose: a document
-- may be indexed before its original file is registered, or with no original
-- at all. A null is not "unchanged" -- it is "cannot be compared", and
-- v_document_search_stale treats it that way rather than assuming the best.
--
-- The row is a derived artefact, like documents_fts itself: dropping it costs
-- knowledge of freshness, never content.
-- ---------------------------------------------------------------------------
CREATE TABLE document_index_state (
    doc_id         TEXT PRIMARY KEY REFERENCES documents(id),
    indexed_sha256 TEXT CHECK (indexed_sha256 IS NULL OR length(indexed_sha256) = 64),
    indexed_at     TEXT NOT NULL
);

-- ---------------------------------------------------------------------------
-- v_document_search_stale: indexed text that can no longer be trusted to be
-- the document's text. Like v_document_search_missing this is a maintenance
-- queue, not an error, and it reports WHY so the two cases stay distinct:
--
--   content_changed      the original's hash moved after the body was indexed.
--                        The index is serving text that is provably not the
--                        current file.
--   provenance_unknown   there is indexed text but no recorded hash behind it
--                        -- documents indexed before this table existed, or
--                        indexed while the document had no original file. It
--                        may well be current; nothing here can establish that.
--
-- Both mean the same next step (feed the text back through `doc index`), so
-- one view with a reason column beats two views a caller has to union.
--
-- An original file that carries no sha256 of its own (document_files.sha256 is
-- nullable; `doc add --path` does not require one) is excluded rather than
-- reported. Re-indexing such a document would record another null and the row
-- would never clear -- a queue item no action can resolve is worse than
-- silence. Those files are a hashing gap, not an index gap.
-- ---------------------------------------------------------------------------
CREATE VIEW v_document_search_stale AS
SELECT s.doc_id,
       s.title,
       s.rel_path,
       CASE WHEN st.doc_id IS NULL OR st.indexed_sha256 IS NULL
            THEN 'provenance_unknown'
            ELSE 'content_changed'
       END                AS reason,
       st.indexed_sha256  AS indexed_sha256,
       s.sha256           AS current_sha256
  FROM v_document_search_source s
  LEFT JOIN document_index_state st ON st.doc_id = s.doc_id
 WHERE s.sha256 IS NOT NULL
   AND EXISTS (SELECT 1 FROM documents_fts x WHERE x.doc_id = s.doc_id)
   AND (st.doc_id IS NULL
        OR st.indexed_sha256 IS NULL
        OR st.indexed_sha256 <> s.sha256);
