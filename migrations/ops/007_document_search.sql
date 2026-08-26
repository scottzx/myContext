-- ===========================================================================
-- 007_document_search.sql
--
-- Full-text search over document bodies.
--
-- The corpus is overwhelmingly Chinese (客户档案, 会议纪要, 内容稿), and that
-- decides the tokenizer. Measured against modernc.org/sqlite v1.57.0, with the
-- regression tests in internal/adapters/sqlite/fts_test.go:
--
--   unicode61 (the default)  a whole run of Han characters becomes ONE token,
--                            so 云深处 / 杨总 / 数据标注 embedded in a sentence
--                            match NOTHING. Unusable here.
--   trigram                  finds Chinese substrings correctly, with no false
--                            positives -- but only for queries of three
--                            characters or more, because a shorter query forms
--                            no complete trigram.
--
-- The two-character gap is not an edge case for this data: 杨总 / 李总 / 王总
-- is how key contacts are named, and AI is two characters. Queries shorter
-- than three characters must therefore fall back to a LIKE scan. That branch
-- lives in Go (internal/ops/search.go), deliberately and documented, not as an
-- afterthought.
--
-- The table is contentless-by-convention: documents holds no body column
-- (the bytes are a file on disk, see document_files), so the caller supplies
-- the extracted text at write time and this index owns its own copy. It is a
-- derived artefact -- droppable and rebuildable from the library files.
-- ===========================================================================

CREATE VIRTUAL TABLE documents_fts USING fts5(
    doc_id UNINDEXED,
    title,
    body,
    tokenize = 'trigram'
);

-- ---------------------------------------------------------------------------
-- v_document_search_source: what SHOULD be indexed, so a rebuild and a
-- staleness check both have something to compare against. A document earns a
-- row once it has an original file; renditions (the .pdf beside the .html) are
-- the same text and must not be indexed twice.
-- ---------------------------------------------------------------------------
CREATE VIEW v_document_search_source AS
SELECT d.id        AS doc_id,
       d.title     AS title,
       f.rel_path  AS rel_path,
       f.sha256    AS sha256
  FROM documents d
  JOIN document_files f ON f.doc_id = d.id AND f.role = 'original';

-- ---------------------------------------------------------------------------
-- v_document_search_missing: documents whose text is not in the index. It is
-- a maintenance queue, not an error: a document added without its body text
-- is still a legitimate row, it just will not be found by content until the
-- index is populated.
-- ---------------------------------------------------------------------------
CREATE VIEW v_document_search_missing AS
SELECT s.doc_id, s.title, s.rel_path
  FROM v_document_search_source s
 WHERE NOT EXISTS (SELECT 1 FROM documents_fts x WHERE x.doc_id = s.doc_id);
