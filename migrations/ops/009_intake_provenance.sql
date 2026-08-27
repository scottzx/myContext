-- ===========================================================================
-- 009_intake_provenance.sql
--
-- The layer between "the user handed us some bytes" and "the business tables
-- changed". Design §2: L0 Evidence stays in the Library, L1 Intake lives
-- here, and nothing crosses into L2 Business without a human decision.
--
-- Three separations are physical, not conventional:
--
--   evidence vs. interpretation  library_packages/documents hold what arrived;
--                                extraction_runs hold one model's reading of it.
--   candidate vs. fact           a model may only write *_candidates rows;
--                                only inbox.confirm touches typed business tables.
--   decision vs. revision        candidate_decisions record what a PERSON chose;
--                                candidate_revisions record that a proposal was
--                                replaced. An agent editing its own proposal must
--                                never be able to look like human approval.
--
-- SQLite CHECK cannot see other tables, so the invariants split three ways
-- (design §3 "不变量的执行边界"): enums, JSON validity, paired null-ness,
-- ref_type/ID exclusivity and the partial unique indexes live here; cross-table
-- and closure rules live in Go Tx primitives; drift detection lives in the
-- quality views added by 010.
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- capture_ingestions: the recoverable intent behind one capture.
--
-- library_packages only guarantees bytes reached 'sealed'. A crash after the
-- seal but before the document/inbox transaction would leave sealed bytes that
-- nothing in the system points at. This row is written FIRST, carries the
-- versioned input needed to finish the job, and is what the reconciler resumes
-- from. It never holds business facts - only enough to rebuild the registration.
-- ---------------------------------------------------------------------------
CREATE TABLE capture_ingestions (
    id                 TEXT PRIMARY KEY CHECK (id GLOB 'ingest_*'),
    request_id         TEXT NOT NULL UNIQUE,
    capture_kind       TEXT NOT NULL CHECK (capture_kind IN ('text')),
    source_ref         TEXT,
    title              TEXT,
    -- sha256 of the LF-normalised UTF-8 original. Together with request_id it
    -- is the idempotency authority for the cross-file phase: the same request
    -- replaying the same bytes must resume, never produce a second package.
    canonical_text_sha TEXT NOT NULL
                       CHECK (length(canonical_text_sha) = 64 AND
                              canonical_text_sha = lower(canonical_text_sha)),
    planned_json       TEXT NOT NULL CHECK (json_valid(planned_json)),
    package_id         TEXT REFERENCES library_packages(id),
    document_id        TEXT REFERENCES documents(id),
    inbox_id           TEXT,
    state              TEXT NOT NULL
                       CHECK (state IN ('planned','sealed','registered','failed')),
    error_code         TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    -- Each state names what must already be true. Without this a 'registered'
    -- row with no document_id would silently satisfy the reconciler.
    CHECK (state <> 'sealed'     OR package_id IS NOT NULL),
    CHECK (state <> 'registered' OR (package_id IS NOT NULL AND
                                     document_id IS NOT NULL AND
                                     inbox_id IS NOT NULL))
);

CREATE INDEX ix_capture_ingestions_state ON capture_ingestions(state, created_at);
CREATE INDEX ix_capture_ingestions_sha ON capture_ingestions(canonical_text_sha);

-- ---------------------------------------------------------------------------
-- inbox_items: one captured package = one thing to deal with.
--
-- package_id is UNIQUE on purpose. Ten unrelated articles pasted in one sitting
-- must be ten packages and ten inbox items; splitting a package into several
-- semantic units after sealing would mean the review UI and the immutable
-- manifest disagree about what "the original" is.
-- ---------------------------------------------------------------------------
CREATE TABLE inbox_items (
    id                 TEXT PRIMARY KEY CHECK (id GLOB 'inbox_*'),
    package_id         TEXT NOT NULL UNIQUE REFERENCES library_packages(id),
    document_id        TEXT REFERENCES documents(id),
    capture_kind       TEXT NOT NULL CHECK (capture_kind IN ('text')),
    source_ref         TEXT,
    title              TEXT,
    status             TEXT NOT NULL DEFAULT 'captured'
                       CHECK (status IN ('captured','extracting','reviewing',
                                         'confirmed','archived','error')),
    -- Where this evidence ended up. V1a routes only to an opportunity; 011 adds
    -- external_program and standalone application. Both columns or neither:
    -- a root type with no id is not a route, it is a bug that reads as one.
    assigned_root_type TEXT CHECK (assigned_root_type IS NULL OR
                                   assigned_root_type IN ('opportunity','external_program','application')),
    assigned_root_id   TEXT,
    error_code         TEXT,
    error_message      TEXT,
    version            INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    confirmed_at       TEXT,
    CHECK ((assigned_root_type IS NULL) = (assigned_root_id IS NULL)),
    CHECK ((status = 'confirmed') = (confirmed_at IS NOT NULL)),
    CHECK (status <> 'error' OR error_code IS NOT NULL)
);

CREATE INDEX ix_inbox_items_status ON inbox_items(status, created_at);
CREATE INDEX ix_inbox_items_root ON inbox_items(assigned_root_type, assigned_root_id)
    WHERE assigned_root_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- extraction_runs: one model's reading of one inbox item, never overwritten.
--
-- (inbox_id, logical_run_key) is the LOGICAL extraction; attempt_no separates
-- retries of it. A run that dies leaves a 'running' row, which goes stale and
-- fails rather than blocking the next attempt forever - and the failed attempt
-- stays, because "the model tried and produced nothing usable" is itself a
-- fact worth keeping when a later attempt disagrees.
-- ---------------------------------------------------------------------------
CREATE TABLE extraction_runs (
    id              TEXT PRIMARY KEY CHECK (id GLOB 'ext_*'),
    inbox_id        TEXT NOT NULL REFERENCES inbox_items(id),
    logical_run_key TEXT NOT NULL CHECK (logical_run_key <> ''),
    attempt_no      INTEGER NOT NULL CHECK (attempt_no > 0),
    extractor       TEXT NOT NULL CHECK (extractor <> ''),
    model           TEXT,
    prompt_version  TEXT NOT NULL CHECK (prompt_version <> ''),
    schema_version  INTEGER NOT NULL CHECK (schema_version > 0),
    status          TEXT NOT NULL
                    CHECK (status IN ('running','completed','failed','superseded')),
    raw_result_json TEXT CHECK (raw_result_json IS NULL OR json_valid(raw_result_json)),
    input_hash      TEXT NOT NULL
                    CHECK (length(input_hash) = 64 AND input_hash = lower(input_hash)),
    started_at      TEXT NOT NULL,
    completed_at    TEXT,
    error_code      TEXT,
    error_message   TEXT,
    CHECK (status NOT IN ('completed','failed') OR completed_at IS NOT NULL),
    CHECK (status <> 'failed' OR error_code IS NOT NULL)
);

CREATE UNIQUE INDEX ux_extraction_runs_attempt
    ON extraction_runs(inbox_id, logical_run_key, attempt_no);
-- At most one usable run per logical extraction. Superseding one is an explicit
-- status change, so "which candidates am I reviewing" always has one answer.
CREATE UNIQUE INDEX ux_extraction_runs_active
    ON extraction_runs(inbox_id, logical_run_key) WHERE status = 'completed';
CREATE INDEX ix_extraction_runs_inbox ON extraction_runs(inbox_id, started_at);

-- ---------------------------------------------------------------------------
-- entity_candidate_groups: the stable identity of a PROPOSED object.
--
-- Facts, relations and actions reference the group, never a particular
-- candidate row. That is what lets `candidate.revise` replace a proposal
-- without cloning or rewriting everything that pointed at it.
-- ---------------------------------------------------------------------------
CREATE TABLE entity_candidate_groups (
    id          TEXT PRIMARY KEY CHECK (id GLOB 'entitygroup_*'),
    run_id      TEXT NOT NULL REFERENCES extraction_runs(id),
    entity_type TEXT NOT NULL
                CHECK (entity_type IN ('account','contact','opportunity','interaction',
                                       'external_program','application')),
    created_at  TEXT NOT NULL
);

CREATE INDEX ix_entity_candidate_groups_run ON entity_candidate_groups(run_id);

-- ---------------------------------------------------------------------------
-- entity_candidates: "create a new one / patch that one / just link that one".
--
-- Deciding this BEFORE reviewing fields is what stops ten documents about the
-- same customer from creating ten accounts. Similarity may suggest; only a
-- person may merge (design §3).
-- ---------------------------------------------------------------------------
CREATE TABLE entity_candidates (
    id                TEXT PRIMARY KEY CHECK (id GLOB 'entitycand_*'),
    group_id          TEXT NOT NULL REFERENCES entity_candidate_groups(id),
    run_id            TEXT NOT NULL REFERENCES extraction_runs(id),
    entity_type       TEXT NOT NULL
                      CHECK (entity_type IN ('account','contact','opportunity','interaction',
                                             'external_program','application')),
    intent            TEXT NOT NULL CHECK (intent IN ('create','update','link_existing')),
    target_id         TEXT,
    target_version    INTEGER CHECK (target_version IS NULL OR target_version > 0),
    match_basis_json  TEXT CHECK (match_basis_json IS NULL OR json_valid(match_basis_json)),
    status            TEXT NOT NULL DEFAULT 'proposed'
                      CHECK (status IN ('proposed','accepted','rejected','superseded')),
    supersedes_id     TEXT REFERENCES entity_candidates(id),
    materialized_type TEXT,
    materialized_id   TEXT,
    created_at        TEXT NOT NULL,
    -- An update without the version it was read at cannot be applied safely;
    -- a create with a target is not a create.
    CHECK (intent <> 'create'        OR (target_id IS NULL AND target_version IS NULL)),
    CHECK (intent <> 'update'        OR (target_id IS NOT NULL AND target_version IS NOT NULL)),
    CHECK (intent <> 'link_existing' OR target_id IS NOT NULL),
    CHECK ((materialized_type IS NULL) = (materialized_id IS NULL)),
    CHECK (supersedes_id IS NULL OR supersedes_id <> id)
);

-- One live proposal per identity. 'superseded' rows stay for the audit trail.
CREATE UNIQUE INDEX ux_entity_candidates_active
    ON entity_candidates(group_id) WHERE status <> 'superseded';
CREATE INDEX ix_entity_candidates_run ON entity_candidates(run_id, status);
CREATE INDEX ix_entity_candidates_target ON entity_candidates(target_id)
    WHERE target_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- fact_candidates: "this object's this field should be this value".
--
-- Field-level, not blob-level: a user accepting a customer name must not be
-- forced to also accept a hallucinated deal size. value_json is parsed by the
-- Go Field Registry, which is also what forbids an unregistered field name -
-- there is no SQL path that writes an arbitrary column.
-- ---------------------------------------------------------------------------
CREATE TABLE fact_candidates (
    id                  TEXT PRIMARY KEY CHECK (id GLOB 'fact_*'),
    run_id              TEXT NOT NULL REFERENCES extraction_runs(id),
    entity_group_id     TEXT NOT NULL REFERENCES entity_candidate_groups(id),
    field_name          TEXT NOT NULL CHECK (field_name <> '' AND field_name = trim(field_name)),
    value_type          TEXT NOT NULL
                        CHECK (value_type IN ('text','number','date','timestamp','boolean','money')),
    value_json          TEXT NOT NULL CHECK (json_valid(value_json)),
    confidence          REAL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    source_document_id  TEXT NOT NULL REFERENCES documents(id),
    source_locator_json TEXT NOT NULL CHECK (json_valid(source_locator_json)),
    status              TEXT NOT NULL DEFAULT 'proposed'
                        CHECK (status IN ('proposed','accepted','rejected','superseded')),
    supersedes_id       TEXT REFERENCES fact_candidates(id),
    created_at          TEXT NOT NULL,
    CHECK (supersedes_id IS NULL OR supersedes_id <> id)
);

CREATE UNIQUE INDEX ux_fact_candidates_active
    ON fact_candidates(entity_group_id, field_name) WHERE status <> 'superseded';
CREATE INDEX ix_fact_candidates_run ON fact_candidates(run_id, status);

-- ---------------------------------------------------------------------------
-- relation_candidates: "these two objects are connected this way".
--
-- Each end is one of three kinds of reference and carries exactly the ID column
-- that kind allows, so a malformed endpoint is a constraint violation rather
-- than a silently ignored column. attributes_json is restricted to what the
-- Materialization Matrix declares for the relation type; everything else must
-- be an empty object, so relation semantics cannot leak into free-form JSON.
-- ---------------------------------------------------------------------------
CREATE TABLE relation_candidates (
    id                    TEXT PRIMARY KEY CHECK (id GLOB 'rel_*'),
    run_id                TEXT NOT NULL REFERENCES extraction_runs(id),

    from_ref_type         TEXT NOT NULL
                          CHECK (from_ref_type IN ('existing','entity_group','action_group')),
    from_type             TEXT NOT NULL,
    from_id               TEXT,
    from_entity_group_id  TEXT REFERENCES entity_candidate_groups(id),
    from_action_group_id  TEXT,

    relation_type         TEXT NOT NULL
                          CHECK (relation_type IN ('belongs_to','primary_contact','advances',
                                                   'evidence_for','about','documented_by',
                                                   'organized_by','applies_to','executed_by')),

    to_ref_type           TEXT NOT NULL
                          CHECK (to_ref_type IN ('existing','entity_group','action_group')),
    to_type               TEXT NOT NULL,
    to_id                 TEXT,
    to_entity_group_id    TEXT REFERENCES entity_candidate_groups(id),
    to_action_group_id    TEXT,

    confidence            REAL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    attributes_json       TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(attributes_json)),
    source_document_id    TEXT NOT NULL REFERENCES documents(id),
    source_locator_json   TEXT NOT NULL CHECK (json_valid(source_locator_json)),
    status                TEXT NOT NULL DEFAULT 'proposed'
                          CHECK (status IN ('proposed','accepted','rejected','superseded')),
    supersedes_id         TEXT REFERENCES relation_candidates(id),
    created_at            TEXT NOT NULL,

    CHECK (from_ref_type <> 'existing'     OR (from_id IS NOT NULL AND from_entity_group_id IS NULL AND from_action_group_id IS NULL)),
    CHECK (from_ref_type <> 'entity_group' OR (from_entity_group_id IS NOT NULL AND from_id IS NULL AND from_action_group_id IS NULL)),
    CHECK (from_ref_type <> 'action_group' OR (from_action_group_id IS NOT NULL AND from_id IS NULL AND from_entity_group_id IS NULL)),
    CHECK (to_ref_type   <> 'existing'     OR (to_id IS NOT NULL AND to_entity_group_id IS NULL AND to_action_group_id IS NULL)),
    CHECK (to_ref_type   <> 'entity_group' OR (to_entity_group_id IS NOT NULL AND to_id IS NULL AND to_action_group_id IS NULL)),
    CHECK (to_ref_type   <> 'action_group' OR (to_action_group_id IS NOT NULL AND to_id IS NULL AND to_entity_group_id IS NULL)),
    CHECK (supersedes_id IS NULL OR supersedes_id <> id)
);

-- One live proposal per formal relation. The COALESCE key normalises the three
-- reference kinds into one comparable tuple, so proposing the same edge twice
-- through different reference kinds is caught here rather than at write time.
CREATE UNIQUE INDEX ux_relation_candidates_active ON relation_candidates(
    from_ref_type, from_type, COALESCE(from_id, from_entity_group_id, from_action_group_id),
    relation_type,
    to_ref_type, to_type, COALESCE(to_id, to_entity_group_id, to_action_group_id)
) WHERE status <> 'superseded';
CREATE INDEX ix_relation_candidates_run ON relation_candidates(run_id, status);

-- ---------------------------------------------------------------------------
-- action_candidate_groups / action_candidates: proposed Projects, Milestones
-- and Tasks. They occupy no planning capacity until confirmed - that is the
-- whole point of keeping them out of the typed tables.
--
-- Objects and actions stay separate (design premise 3): a Project action may
-- NOT carry a subject, because "which opportunity does this project advance"
-- is a relation and must have exactly one authority.
-- ---------------------------------------------------------------------------
CREATE TABLE action_candidate_groups (
    id          TEXT PRIMARY KEY CHECK (id GLOB 'actiongroup_*'),
    run_id      TEXT NOT NULL REFERENCES extraction_runs(id),
    action_type TEXT NOT NULL CHECK (action_type IN ('project','milestone','task')),
    created_at  TEXT NOT NULL
);

CREATE INDEX ix_action_candidate_groups_run ON action_candidate_groups(run_id);

CREATE TABLE action_candidates (
    id                     TEXT PRIMARY KEY CHECK (id GLOB 'action_*'),
    group_id               TEXT NOT NULL REFERENCES action_candidate_groups(id),
    run_id                 TEXT NOT NULL REFERENCES extraction_runs(id),
    action_type            TEXT NOT NULL CHECK (action_type IN ('project','milestone','task')),
    parent_action_group_id TEXT REFERENCES action_candidate_groups(id),
    subject_type           TEXT,
    subject_id             TEXT,
    subject_entity_group_id TEXT REFERENCES entity_candidate_groups(id),
    draft_json             TEXT NOT NULL CHECK (json_valid(draft_json)),
    source_document_id     TEXT NOT NULL REFERENCES documents(id),
    source_locator_json    TEXT NOT NULL CHECK (json_valid(source_locator_json)),
    status                 TEXT NOT NULL DEFAULT 'proposed'
                           CHECK (status IN ('proposed','accepted','rejected','superseded')),
    supersedes_id          TEXT REFERENCES action_candidates(id),
    materialized_type      TEXT,
    materialized_id        TEXT,
    created_at             TEXT NOT NULL,
    -- A milestone or task with no parent has nowhere to be scheduled.
    CHECK (action_type <> 'project' OR parent_action_group_id IS NULL),
    CHECK (action_type =  'project' OR parent_action_group_id IS NOT NULL),
    -- Business membership of a project is a relation, never a subject.
    CHECK (action_type <> 'project' OR (subject_type IS NULL AND subject_id IS NULL
                                        AND subject_entity_group_id IS NULL)),
    CHECK ((subject_type IS NULL AND subject_id IS NULL) OR
           (subject_type IS NOT NULL AND subject_id IS NOT NULL)),
    CHECK (subject_id IS NULL OR subject_entity_group_id IS NULL),
    CHECK ((materialized_type IS NULL) = (materialized_id IS NULL)),
    CHECK (supersedes_id IS NULL OR supersedes_id <> id)
);

CREATE UNIQUE INDEX ux_action_candidates_active
    ON action_candidates(group_id) WHERE status <> 'superseded';
CREATE INDEX ix_action_candidates_run ON action_candidates(run_id, status);
CREATE INDEX ix_action_candidates_parent ON action_candidates(parent_action_group_id)
    WHERE parent_action_group_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- candidate_decisions: append-only record of what a PERSON decided.
--
-- reviewer_type is constrained to 'user' and decision to accept|reject. An
-- agent revising its own proposal is a different act entirely and is recorded
-- in candidate_revisions below; conflating the two would make "a human approved
-- this" unprovable, which is the one claim this whole layer exists to support.
-- ---------------------------------------------------------------------------
CREATE TABLE candidate_decisions (
    id             TEXT PRIMARY KEY CHECK (id GLOB 'dec_*'),
    candidate_type TEXT NOT NULL CHECK (candidate_type IN ('entity','fact','relation','action')),
    candidate_id   TEXT NOT NULL,
    decision       TEXT NOT NULL CHECK (decision IN ('accept','reject')),
    reviewer_type  TEXT NOT NULL DEFAULT 'user' CHECK (reviewer_type = 'user'),
    reviewer_id    TEXT,
    reason         TEXT,
    correlation_id TEXT NOT NULL CHECK (correlation_id <> ''),
    decided_at     TEXT NOT NULL
);

CREATE INDEX ix_candidate_decisions_candidate
    ON candidate_decisions(candidate_type, candidate_id, decided_at);
CREATE INDEX ix_candidate_decisions_correlation ON candidate_decisions(correlation_id);

-- ---------------------------------------------------------------------------
-- candidate_revisions: a proposal was replaced by a newer proposal.
--
-- Either a user or an agent may revise. Revision never accepts or rejects, so
-- nothing an agent can call on its own moves data into the business tables.
-- ---------------------------------------------------------------------------
CREATE TABLE candidate_revisions (
    id               TEXT PRIMARY KEY CHECK (id GLOB 'rev_*'),
    candidate_type   TEXT NOT NULL CHECK (candidate_type IN ('entity','fact','relation','action')),
    old_candidate_id TEXT NOT NULL,
    new_candidate_id TEXT NOT NULL,
    actor_type       TEXT NOT NULL CHECK (actor_type IN ('user','agent')),
    actor_id         TEXT,
    reason           TEXT,
    request_id       TEXT NOT NULL,
    revised_at       TEXT NOT NULL,
    CHECK (old_candidate_id <> new_candidate_id)
);

CREATE UNIQUE INDEX ux_candidate_revisions_old
    ON candidate_revisions(candidate_type, old_candidate_id);
CREATE INDEX ix_candidate_revisions_new ON candidate_revisions(candidate_type, new_candidate_id);

-- ---------------------------------------------------------------------------
-- confirmation_grants: proof that a human clicked confirm in a real session.
--
-- An actor string proves nothing - anything that can invoke can send one. The
-- review UI mints a single-use nonce bound to this session, this inbox, this
-- run, the version it displayed and a hash of the exact decisions on screen.
-- Only the nonce's hash is stored, so reading the database does not let you
-- confirm anything. CLI and agents have no endpoint that issues one.
-- ---------------------------------------------------------------------------
CREATE TABLE confirmation_grants (
    nonce_hash       TEXT PRIMARY KEY
                     CHECK (length(nonce_hash) = 64 AND nonce_hash = lower(nonce_hash)),
    session_id_hash  TEXT NOT NULL
                     CHECK (length(session_id_hash) = 64 AND session_id_hash = lower(session_id_hash)),
    inbox_id         TEXT NOT NULL REFERENCES inbox_items(id),
    active_run_id    TEXT NOT NULL REFERENCES extraction_runs(id),
    decisions_hash   TEXT NOT NULL
                     CHECK (length(decisions_hash) = 64 AND decisions_hash = lower(decisions_hash)),
    expected_version INTEGER NOT NULL CHECK (expected_version > 0),
    expires_at       TEXT NOT NULL,
    consumed_at      TEXT,
    correlation_id   TEXT,
    created_at       TEXT NOT NULL
);

CREATE INDEX ix_confirmation_grants_inbox ON confirmation_grants(inbox_id, expires_at);

-- ---------------------------------------------------------------------------
-- source_attributions: a confirmed FIELD, traced back to a byte range.
--
-- materialized_version + normalized_value_hash are what make "current" a
-- computable question: the workspace marks an attribution current only when its
-- hash still equals the field's normalised value. Editing a field by hand later
-- does not falsify the record - it just stops being the current source.
--
-- origin_type states what kind of claim this is, and each kind has to carry its
-- own evidence: evidence needs a locator, a manual entry needs a human note, a
-- migration needs the batch that produced it.
-- ---------------------------------------------------------------------------
CREATE TABLE source_attributions (
    id                    TEXT PRIMARY KEY CHECK (id GLOB 'src_*'),
    entity_type           TEXT NOT NULL,
    entity_id             TEXT NOT NULL,
    field_name            TEXT NOT NULL CHECK (field_name <> ''),
    materialized_version  INTEGER CHECK (materialized_version IS NULL OR materialized_version > 0),
    normalized_value_hash TEXT NOT NULL
                          CHECK (length(normalized_value_hash) = 64 AND
                                 normalized_value_hash = lower(normalized_value_hash)),
    document_id           TEXT REFERENCES documents(id),
    source_locator_json   TEXT CHECK (source_locator_json IS NULL OR json_valid(source_locator_json)),
    extraction_run_id     TEXT REFERENCES extraction_runs(id),
    decision_id           TEXT REFERENCES candidate_decisions(id),
    origin_type           TEXT NOT NULL CHECK (origin_type IN ('evidence','manual','migration')),
    origin_note           TEXT,
    import_batch_id       TEXT,
    created_at            TEXT NOT NULL,
    CHECK (origin_type <> 'evidence'  OR (document_id IS NOT NULL AND source_locator_json IS NOT NULL)),
    CHECK (origin_type <> 'manual'    OR origin_note IS NOT NULL),
    CHECK (origin_type <> 'migration' OR import_batch_id IS NOT NULL)
);

CREATE INDEX ix_source_attributions_entity
    ON source_attributions(entity_type, entity_id, field_name, created_at);
CREATE INDEX ix_source_attributions_document ON source_attributions(document_id)
    WHERE document_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- relation_attributions: a confirmed RELATION, traced back to a byte range.
--
-- Deliberately not a source_attributions row with a fake field name. A relation
-- has two endpoints and a storage location, not a value, and pretending
-- otherwise is what makes provenance queries start guessing.
--
-- storage_type/storage_key name the real place the relation lives -
-- 'opportunity_projects' / 'opp_x:proj_y' - so provenance can be checked
-- against the actual row rather than against a description of it. Replacing a
-- relation closes the old attribution with the closing operation's correlation
-- id instead of deleting it: how a link came to be, and when it stopped being
-- true, are both part of the record.
-- ---------------------------------------------------------------------------
CREATE TABLE relation_attributions (
    id                        TEXT PRIMARY KEY CHECK (id GLOB 'relsrc_*'),
    relation_type             TEXT NOT NULL,
    from_type                 TEXT NOT NULL,
    from_id                   TEXT NOT NULL,
    to_type                   TEXT NOT NULL,
    to_id                     TEXT NOT NULL,
    storage_type              TEXT NOT NULL CHECK (storage_type <> ''),
    storage_key               TEXT NOT NULL CHECK (storage_key <> ''),
    valid_from_correlation_id TEXT NOT NULL CHECK (valid_from_correlation_id <> ''),
    valid_to_correlation_id   TEXT,
    document_id               TEXT REFERENCES documents(id),
    source_locator_json       TEXT CHECK (source_locator_json IS NULL OR json_valid(source_locator_json)),
    extraction_run_id         TEXT REFERENCES extraction_runs(id),
    decision_id               TEXT REFERENCES candidate_decisions(id),
    origin_type               TEXT NOT NULL CHECK (origin_type IN ('evidence','manual','migration')),
    origin_note               TEXT,
    import_batch_id           TEXT,
    created_at                TEXT NOT NULL,
    CHECK (origin_type <> 'evidence'  OR (document_id IS NOT NULL AND source_locator_json IS NOT NULL)),
    CHECK (origin_type <> 'manual'    OR origin_note IS NOT NULL),
    CHECK (origin_type <> 'migration' OR import_batch_id IS NOT NULL)
);

CREATE INDEX ix_relation_attributions_storage
    ON relation_attributions(storage_type, storage_key);
CREATE INDEX ix_relation_attributions_from ON relation_attributions(from_type, from_id);
CREATE INDEX ix_relation_attributions_open
    ON relation_attributions(storage_type, storage_key) WHERE valid_to_correlation_id IS NULL;
