-- ===========================================================================
-- 008_library_journal.sql
--
-- The database half of the Library's recoverable commit.
--
-- A SQLite transaction cannot span a file copy, so capturing a file is not
-- atomic and is not pretended to be (technical design §15). Instead the commit
-- leaves a trail: staged -> renamed -> sealed, with a journal row written
-- before and after the rename. On restart `library verify` compares this table
-- against what is on disk and resolves each case by §15.2's six-row matrix.
--
-- Two columns carry the weight:
--   request_id  makes capture idempotent. A retried commit finds the package
--               it already created instead of writing a second copy.
--   state       is the half of the matrix the filesystem cannot tell you. A
--               'sealed' row whose files are gone is a loud integrity error;
--               without the row, the same empty directory is indistinguishable
--               from a capture that never happened.
--
-- Recovery never fabricates a file and never deletes one. This table records
-- what was intended so a human can be told the truth about what is missing.
-- ===========================================================================

CREATE TABLE library_packages (
    id           TEXT PRIMARY KEY,
    request_id   TEXT NOT NULL UNIQUE,
    -- The capture-date directory this package lives under, in the instance
    -- timezone. Fixed at creation: a later title or metadata correction must
    -- never move bytes that are already sealed.
    storage_date TEXT NOT NULL
                 CHECK (storage_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    package_path TEXT NOT NULL UNIQUE,
    manifest_hash TEXT
                 CHECK (manifest_hash IS NULL OR
                        (length(manifest_hash) = 64 AND manifest_hash = lower(manifest_hash))),
    state        TEXT NOT NULL
                 CHECK (state IN ('staging','sealed')),
    asset_count  INTEGER NOT NULL DEFAULT 0 CHECK (asset_count >= 0),
    total_bytes  INTEGER NOT NULL DEFAULT 0 CHECK (total_bytes >= 0),
    captured_at  TEXT NOT NULL,
    sealed_at    TEXT,
    -- A package is sealed exactly when it has a sealing time; the two must not
    -- drift apart, because verify branches on state and reports sealed_at.
    CHECK ((state = 'sealed') = (sealed_at IS NOT NULL))
);

CREATE INDEX ix_library_packages_state ON library_packages(state);
CREATE INDEX ix_library_packages_date ON library_packages(storage_date);

-- ---------------------------------------------------------------------------
-- Which package a document's bytes came from. Nullable on the document side:
-- a metadata-only document is legitimate (a decision record whose text lives
-- in the row, an external article recorded by URL alone).
-- ---------------------------------------------------------------------------
ALTER TABLE document_files ADD COLUMN package_id TEXT REFERENCES library_packages(id);

CREATE INDEX ix_document_files_package ON document_files(package_id)
    WHERE package_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- v_library_incomplete: captures that never reached 'sealed'. Either they are
-- mid-flight, or a process died between the staging write and the rename.
-- `library verify` decides which; this view is what makes them visible at all
-- instead of leaving bytes in staging that nothing ever looks at again.
-- ---------------------------------------------------------------------------
CREATE VIEW v_library_incomplete AS
SELECT id, request_id, storage_date, package_path, captured_at, asset_count
  FROM library_packages
 WHERE state = 'staging'
 ORDER BY captured_at;
