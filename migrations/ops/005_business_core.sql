-- ops.db v4: the execution core gains a business core. Until now the database
-- answered "what am I doing"; it could not answer "who is this for, did the
-- money arrive, which product does this push". Those are objects, not tasks.
--
--   business line          areas (unchanged - Area > Initiative > Project is
--                          already "business line > initiative > project")
--   who                    accounts, contacts
--   the deal               opportunities -> contracts -> receivable_plans -> receipts
--   the application        applications (competition / program / job / listing)
--   after delivery         service_tickets
--   the conversation       interactions
--   the artefact           documents + document_files + doc_links
--   the number over time   metric_samples
--   the soft relation      context_edges
--
-- Two organising rules, applied everywhere below:
--   * A business table holds the OBJECT and its result; `tasks` holds the
--     ACTIONS. They meet through tasks.subject_type/subject_id, never by
--     merging.
--   * Computation is read-time only. There is not one trigger and not one
--     derived column in this file. Every rollup is a VIEW, following
--     v_metric_rollup (004:531): state the declared number AND the computed
--     number side by side, and never overwrite what the user wrote.
--
-- Shaped by the legacy data, not by speculation:
--   * 7 of 18 legacy projects are application-shaped (GOAI's seven gates,
--     From Idea to Frontier, Google x ZhenFund, brand submissions, a job
--     application) -> applications is a table, not a special project.
--   * `数据标注 · 18 x Y300 = 5400` is piecework -> contracts.unit_price and
--     contracts.quantity, with `amount` still the authoritative figure.
--   * `yunshen_AI_training_solution.html` and `.pdf` are two renditions of ONE
--     deliverable -> document_files, and documents stores no path at all.
--   * `training_curriculum_system_v0.1 -> v0.2 -> v0.3`, each version carrying
--     its own change note -> documents.lineage_id + supersedes_id +
--     change_note; the manual habit becomes a column.
--   * `notes/decisions/` all end in "when to re-evaluate" -> documents.kind
--     'decision' + review_at + v_doc_review_due.
--   * `周三回访申彤老师(国金证券·问物业引荐)` is a referral, and
--     `nextgen_ai_organization_v0.1 -> ..._yunshen_v0.1` is a fork rather than
--     a version -> context_edges, for soft relations only. The main chain
--     stays on real foreign keys.
--   * The demand pool is 429 + 1200 = 1629 rows, two orders of magnitude
--     bigger than a consulting lead -> opportunities.source_batch + its index.
--
-- Three gates keep writes correct, since there are no triggers:
--   1. this file        - single-row and single-table invariants (FK, CHECK,
--                         UNIQUE). A deal must have a customer; an amount must
--                         be positive; a contract number cannot repeat.
--   2. the Go layer     - cross-table and cross-state legality (only a won
--                         opportunity may become a contract).
--   3. v_biz_quality_issues - cross-row aggregate disagreements, which a CHECK
--                         cannot express. Contract amount <> sum of receivable
--                         plans is NOT blocked and NOT auto-corrected: it
--                         becomes a row to look at. Same philosophy as
--                         "overload is stated, the system never picks what to
--                         drop".

-- Thresholds the quality views need. Kept in the database, like the capacity
-- defaults in 001, so a view stays self-contained and the number is visible.
INSERT INTO ops_settings (key, value, updated_at) VALUES
    ('biz_opportunity_stale_days', '30', '1970-01-01T00:00:00Z'),
    ('biz_application_stale_days', '30', '1970-01-01T00:00:00Z');

-- ===========================================================================
-- Cross-cutting layer. Shared by every business line, so it has to be right
-- once. All five carry the full entity vocabulary - including the four types
-- 006 will introduce - so that 006 never has to rebuild a table to widen a
-- CHECK.
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- documents: one row per version of one artefact.
--
-- It stores no path. A document is the deliverable; the bytes on disk are
-- document_files rows, because one deliverable routinely has several files.
--
-- lineage_id groups every version of the same document and is NOT NULL by
-- design: a document with no predecessor is the root of its own lineage and
-- carries its own id there. SQLite cannot default a column to the row's own
-- id, so the Go writer sets it (new lineage -> lineage_id = id; new version ->
-- lineage_id = predecessor's lineage_id, supersedes_id = predecessor's id).
-- v_document_current resolves a lineage to its newest version, which is the
-- question every consumer actually asks.
-- ---------------------------------------------------------------------------
CREATE TABLE documents (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL DEFAULT 'other'
                  CHECK (kind IN ('dossier','meeting_note','contract_doc','proposal',
                                  'content_draft','release_note','decision','report','other')),
    title         TEXT NOT NULL CHECK (title <> ''),
    -- When the document is ABOUT, not when it was filed: a meeting note filed
    -- on Friday can be about Tuesday's meeting.
    occurred_at   TEXT,
    captured_at   TEXT,
    -- "When to re-evaluate" from the decision notes. Same shape as
    -- tasks.next_review_at, so `task.set-review` and `doc` agree.
    review_at     TEXT CHECK (review_at IS NULL OR review_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    lineage_id    TEXT NOT NULL CHECK (lineage_id <> ''),
    supersedes_id TEXT REFERENCES documents(id),
    change_note   TEXT,
    -- Provenance for captured external material (the articles/ front matter:
    -- source, author, url, and the user's own note on why it was kept).
    source        TEXT,
    author_name   TEXT,
    canonical_url TEXT,
    user_note     TEXT,
    legacy_ref    TEXT,
    version       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    CHECK (supersedes_id IS NULL OR supersedes_id <> id)
);
CREATE INDEX ix_documents_lineage ON documents(lineage_id, created_at);
CREATE INDEX ix_documents_kind ON documents(kind, occurred_at);
CREATE INDEX ix_documents_review ON documents(review_at) WHERE review_at IS NOT NULL;
CREATE INDEX ix_documents_supersedes ON documents(supersedes_id) WHERE supersedes_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- document_files: the bytes. A .html and a .pdf of the same solution are two
-- renditions of ONE document - not two documents, and not two versions.
--
-- Exactly one file per document may be the `original`: it is the authoritative
-- text, the one FTS indexes, and the one a summary may never impersonate. The
-- partial unique index states that the same way task_schedules states "one
-- active plan per task" (001).
-- ---------------------------------------------------------------------------
CREATE TABLE document_files (
    id         TEXT PRIMARY KEY,
    doc_id     TEXT NOT NULL REFERENCES documents(id),
    -- Relative to the library root; the absolute path is an instance detail.
    rel_path   TEXT NOT NULL UNIQUE CHECK (rel_path <> '' AND rel_path NOT GLOB '/*'),
    mime       TEXT,
    size_bytes INTEGER CHECK (size_bytes IS NULL OR size_bytes >= 0),
    sha256     TEXT CHECK (sha256 IS NULL OR (length(sha256) = 64 AND sha256 = lower(sha256))),
    role       TEXT NOT NULL DEFAULT 'original'
               CHECK (role IN ('original','rendition','attachment')),
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);
CREATE INDEX ix_document_files_doc ON document_files(doc_id, role, sort_order);
CREATE UNIQUE INDEX ux_document_files_original ON document_files(doc_id) WHERE role = 'original';
CREATE INDEX ix_document_files_sha ON document_files(sha256) WHERE sha256 IS NOT NULL;

-- ---------------------------------------------------------------------------
-- doc_links: a document hangs off any business object. The legacy filenames
-- already do this by hand - `TASK 104 v0.1`, `TASK 140` - which is exactly
-- doc_links(task, 'deliverable').
-- ---------------------------------------------------------------------------
CREATE TABLE doc_links (
    id          TEXT PRIMARY KEY,
    doc_id      TEXT NOT NULL REFERENCES documents(id),
    entity_type TEXT NOT NULL
                CHECK (entity_type IN ('objective','key_result','initiative','project','milestone','task',
                                       'account','contact','opportunity','application','contract','ticket',
                                       'document','product','content_piece','channel','release','campaign')),
    entity_id   TEXT NOT NULL,
    link_type   TEXT NOT NULL DEFAULT 'attachment'
                CHECK (link_type IN ('dossier','minutes','evidence','attachment','deliverable')),
    created_at  TEXT NOT NULL
);
CREATE UNIQUE INDEX ux_doc_links ON doc_links(doc_id, entity_type, entity_id, link_type);
CREATE INDEX ix_doc_links_entity ON doc_links(entity_type, entity_id, link_type);

-- ---------------------------------------------------------------------------
-- metric_samples: any number, about anything, over time.
--
-- A key result carries a single current_value and therefore has no trend; this
-- is the missing history. Followers, impressions, DAU, monthly sales, platform
-- revenue share all live here rather than becoming columns.
--
-- UNIQUE(subject, metric, sampled_at): re-recording the same metric at the
-- same instant is a CORRECTION, not a second observation. Without it a
-- repeated ingest silently doubles a series and v_metric_trend would lie.
-- ---------------------------------------------------------------------------
CREATE TABLE metric_samples (
    id           TEXT PRIMARY KEY,
    subject_type TEXT NOT NULL
                 CHECK (subject_type IN ('objective','key_result','initiative','project','milestone','task',
                                         'account','contact','opportunity','application','contract','ticket',
                                         'document','product','content_piece','channel','release','campaign')),
    subject_id   TEXT NOT NULL,
    metric_name  TEXT NOT NULL CHECK (metric_name <> '' AND metric_name = trim(metric_name)),
    sampled_at   TEXT NOT NULL,
    value        REAL NOT NULL,
    unit         TEXT,
    source       TEXT,
    note         TEXT,
    created_at   TEXT NOT NULL
);
CREATE UNIQUE INDEX ux_metric_samples ON metric_samples(subject_type, subject_id, metric_name, sampled_at);
CREATE INDEX ix_metric_samples_metric ON metric_samples(metric_name, sampled_at);

-- ---------------------------------------------------------------------------
-- context_edges: SOFT relations only, and nothing else.
--
-- The main chain is real foreign keys and stays that way. What has nowhere to
-- live is the weak stuff: a contact introduced us to another contact
-- (referred_by); a customer-specific proposal forked off the generic one
-- (derived_from - a fork, not a version, so it is not a lineage); one document
-- cites another (references).
--
-- Both ends are checked by the Go layer against entityTables before insert;
-- edges that go dangling later are reconciled by `doctor` and reported by
-- v_biz_quality_issues. Shape copied from `dependencies` (004) deliberately.
-- ---------------------------------------------------------------------------
CREATE TABLE context_edges (
    id         TEXT PRIMARY KEY,
    from_type  TEXT NOT NULL
               CHECK (from_type IN ('objective','key_result','initiative','project','milestone','task',
                                    'account','contact','opportunity','application','contract','ticket',
                                    'document','product','content_piece','channel','release','campaign')),
    from_id    TEXT NOT NULL,
    to_type    TEXT NOT NULL
               CHECK (to_type IN ('objective','key_result','initiative','project','milestone','task',
                                  'account','contact','opportunity','application','contract','ticket',
                                  'document','product','content_piece','channel','release','campaign')),
    to_id      TEXT NOT NULL,
    edge_type  TEXT NOT NULL DEFAULT 'relates_to'
               CHECK (edge_type IN ('referred_by','derived_from','references','relates_to','inspired_by')),
    note       TEXT,
    created_at TEXT NOT NULL,
    CHECK (NOT (from_type = to_type AND from_id = to_id))
);
CREATE UNIQUE INDEX ux_context_edges ON context_edges(from_type, from_id, to_type, to_id, edge_type);
CREATE INDEX ix_context_edges_to ON context_edges(to_type, to_id);

-- ===========================================================================
-- Mode A (project delivery) and mode D (applications).
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- accounts: any external party we deal with. Deliberately wider than
-- "customer" - a competition organiser, a media outlet, a community and a
-- consumer buyer are all counterparties, and only some of them ever produce an
-- opportunity.
--
-- Only plain metadata lives here. Detail belongs in a dossier document
-- (doc_links link_type='dossier'), which is why there is no room to grow a
-- profile schema by accident. Names are NOT unique: the demand pool imports
-- 1629 rows in two batches and individuals collide by name; deduplication is a
-- judgement, not a constraint.
-- ---------------------------------------------------------------------------
CREATE TABLE accounts (
    id           TEXT PRIMARY KEY,
    -- Full legal name; short_name is the brand ("杭州云深处科技股份有限公司"
    -- vs "DEEP Robotics"), which is how the legacy notes already record them.
    name         TEXT NOT NULL CHECK (name <> ''),
    short_name   TEXT,
    account_type TEXT NOT NULL DEFAULT 'prospect'
                 CHECK (account_type IN ('customer','prospect','partner','vendor',
                                         'organizer','media','community','individual')),
    industry     TEXT,
    region       TEXT,
    status       TEXT NOT NULL DEFAULT 'active'
                 CHECK (status IN ('active','dormant','archived')),
    owner        TEXT,
    note         TEXT,
    legacy_ref   TEXT,
    version      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE INDEX ix_accounts_type ON accounts(account_type, status);
CREATE INDEX ix_accounts_name ON accounts(name);

-- ---------------------------------------------------------------------------
-- contacts: a person at an account. account_id is NOT NULL - a person we know
-- only through a company is still reached through that company - but a contact
-- does NOT need an opportunity. Most of the people worth remembering never
-- appear in a deal.
-- ---------------------------------------------------------------------------
CREATE TABLE contacts (
    id         TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id),
    name       TEXT NOT NULL CHECK (name <> ''),
    title      TEXT,
    deal_role  TEXT CHECK (deal_role IS NULL OR
                           deal_role IN ('decider','influencer','user','gatekeeper')),
    phone      TEXT,
    email      TEXT,
    wechat     TEXT,
    status     TEXT NOT NULL DEFAULT 'active'
               CHECK (status IN ('active','inactive','left','archived')),
    note       TEXT,
    legacy_ref TEXT,
    version    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX ix_contacts_account ON contacts(account_id, status);
CREATE INDEX ix_contacts_name ON contacts(name);

-- ---------------------------------------------------------------------------
-- opportunities: a possible deal. account_id NOT NULL, because a deal with
-- nobody is not a deal.
--
-- Closing is a fact with a date, and losing is a fact with a reason: both are
-- enforced here rather than left to discipline. Stage dwell time is NOT a
-- column - v_opportunity_full derives it from the `stage_changed` events, so
-- there is no second copy of the truth to drift.
--
-- source_batch names the import that produced the row (the 429 Hangzhou / 1200
-- Hefei demand pool). A batch lead and a consulting lead differ by two orders
-- of magnitude in count, so they must be filterable apart.
-- ---------------------------------------------------------------------------
CREATE TABLE opportunities (
    id                 TEXT PRIMARY KEY,
    account_id         TEXT NOT NULL REFERENCES accounts(id),
    area_id            TEXT REFERENCES areas(id),
    primary_contact_id TEXT REFERENCES contacts(id),
    name               TEXT NOT NULL CHECK (name <> ''),
    source             TEXT,
    source_batch       TEXT,
    stage              TEXT NOT NULL DEFAULT 'lead'
                       CHECK (stage IN ('lead','qualified','proposal','negotiation','won','lost')),
    est_amount         REAL CHECK (est_amount IS NULL OR est_amount >= 0),
    win_probability    REAL CHECK (win_probability IS NULL OR
                                   (win_probability >= 0 AND win_probability <= 1)),
    expected_sign_date TEXT CHECK (expected_sign_date IS NULL OR
                                   expected_sign_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    owner              TEXT,
    next_step          TEXT,
    lost_reason        TEXT,
    closed_at          TEXT,
    note               TEXT,
    legacy_ref         TEXT,
    version            INTEGER NOT NULL DEFAULT 1,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    CHECK (stage NOT IN ('won','lost') OR closed_at IS NOT NULL),
    CHECK (stage <> 'lost' OR (lost_reason IS NOT NULL AND lost_reason <> ''))
);
CREATE INDEX ix_opportunities_stage ON opportunities(stage, expected_sign_date);
CREATE INDEX ix_opportunities_account ON opportunities(account_id, stage);
CREATE INDEX ix_opportunities_batch ON opportunities(source_batch) WHERE source_batch IS NOT NULL;
CREATE INDEX ix_opportunities_area ON opportunities(area_id, stage);
CREATE INDEX ix_opportunities_open ON opportunities(area_id, expected_sign_date)
    WHERE stage NOT IN ('won','lost');

-- ---------------------------------------------------------------------------
-- applications: we apply, someone else decides. Competitions, ecosystem
-- programmes, jobs, directory listings, partnership submissions.
--
-- This is a table because the shape recurs: 7 of 18 legacy projects have it.
-- The application owns the RESULT; a project owns the ACTIONS, and a multi-
-- round schedule is milestones on that project (GOAI has seven gates). So
-- there is exactly one project_id here and no gate columns.
--
-- account_id is the counterparty - the organiser, the programme, the employer
-- - and it is AUTHORITATIVE for an application-driven project. projects.
-- account_id is for projects that no application and no contract drives; when
-- both exist the views read through the application. Do not "fix" this by
-- copying one into the other: two writable copies of one fact drift.
-- ---------------------------------------------------------------------------
CREATE TABLE applications (
    id            TEXT PRIMARY KEY,
    area_id       TEXT REFERENCES areas(id),
    account_id    TEXT REFERENCES accounts(id),
    project_id    TEXT REFERENCES projects(id),
    name          TEXT NOT NULL CHECK (name <> ''),
    kind          TEXT NOT NULL DEFAULT 'competition'
                  CHECK (kind IN ('competition','program','job','listing','partnership')),
    stage         TEXT NOT NULL DEFAULT 'discovered'
                  CHECK (stage IN ('discovered','preparing','submitted','under_review',
                                   'shortlisted','won','rejected','withdrawn')),
    submitted_at  TEXT,
    decided_at    TEXT,
    -- The announced figure. The money actually receivable is a contract; the
    -- two are allowed to disagree and v_biz_quality_issues states it.
    prize_amount  REAL CHECK (prize_amount IS NULL OR prize_amount >= 0),
    outcome_note  TEXT,
    reject_reason TEXT,
    owner         TEXT,
    next_step     TEXT,
    legacy_ref    TEXT,
    version       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    CHECK (stage NOT IN ('won','rejected','withdrawn') OR decided_at IS NOT NULL),
    CHECK (stage <> 'rejected' OR (reject_reason IS NOT NULL AND reject_reason <> ''))
);
CREATE INDEX ix_applications_stage ON applications(stage, submitted_at);
CREATE INDEX ix_applications_area ON applications(area_id, kind, stage);
CREATE INDEX ix_applications_project ON applications(project_id) WHERE project_id IS NOT NULL;
CREATE INDEX ix_applications_account ON applications(account_id) WHERE account_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- contracts: contractual income of every kind - a sale, a prize, a sponsored
-- post, a grant, piecework. Aggregate income (app sales, platform revenue
-- share) is a metric_samples series instead, because it has no counterparty
-- document.
--
-- `amount` is ALWAYS the authoritative figure. unit_price and quantity exist
-- so piecework can record how it was computed (18 x Y300), and they are
-- allowed to disagree with `amount` after a re-count: the disagreement is
-- reported, never silently resolved.
--
-- A contract comes from an opportunity (a sale) OR from an application (a
-- prize) OR from neither (signed directly). Both at once is meaningless, so it
-- is rejected here.
-- ---------------------------------------------------------------------------
CREATE TABLE contracts (
    id             TEXT PRIMARY KEY,
    account_id     TEXT NOT NULL REFERENCES accounts(id),
    opportunity_id TEXT REFERENCES opportunities(id),
    application_id TEXT REFERENCES applications(id),
    kind           TEXT NOT NULL DEFAULT 'sales'
                   CHECK (kind IN ('sales','prize','sponsorship','grant','piecework','other')),
    contract_no    TEXT UNIQUE,
    name           TEXT NOT NULL CHECK (name <> ''),
    sign_date      TEXT CHECK (sign_date  IS NULL OR sign_date  GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    start_date     TEXT CHECK (start_date IS NULL OR start_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    end_date       TEXT CHECK (end_date   IS NULL OR end_date   GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    amount         REAL NOT NULL CHECK (amount > 0),
    unit_price     REAL CHECK (unit_price IS NULL OR unit_price > 0),
    quantity       REAL CHECK (quantity   IS NULL OR quantity   > 0),
    currency       TEXT NOT NULL DEFAULT 'CNY' CHECK (currency <> ''),
    status         TEXT NOT NULL DEFAULT 'draft'
                   CHECK (status IN ('draft','signed','active','completed','terminated')),
    payment_terms  TEXT,
    note           TEXT,
    legacy_ref     TEXT,
    version        INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    CHECK (status = 'draft' OR sign_date IS NOT NULL),
    CHECK (opportunity_id IS NULL OR application_id IS NULL),
    CHECK (start_date IS NULL OR end_date IS NULL OR start_date <= end_date)
);
CREATE INDEX ix_contracts_account ON contracts(account_id, status);
CREATE INDEX ix_contracts_status ON contracts(status, sign_date);
CREATE INDEX ix_contracts_opportunity ON contracts(opportunity_id) WHERE opportunity_id IS NOT NULL;
CREATE INDEX ix_contracts_application ON contracts(application_id) WHERE application_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- receivable_plans: when the money is supposed to arrive. A plan without a
-- date cannot age, so due_date is NOT NULL; the condition a payment waits on
-- goes in condition_note beside it, not instead of it.
--
-- The sum of these is deliberately NOT constrained to equal contracts.amount.
-- ---------------------------------------------------------------------------
CREATE TABLE receivable_plans (
    id             TEXT PRIMARY KEY,
    contract_id    TEXT NOT NULL REFERENCES contracts(id),
    seq            INTEGER NOT NULL CHECK (seq > 0),
    due_date       TEXT NOT NULL CHECK (due_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    amount         REAL NOT NULL CHECK (amount > 0),
    condition_note TEXT,
    status         TEXT NOT NULL DEFAULT 'planned'
                   CHECK (status IN ('planned','invoiced','received','waived')),
    version        INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
CREATE UNIQUE INDEX ux_receivable_plans_seq ON receivable_plans(contract_id, seq);
CREATE INDEX ix_receivable_plans_due ON receivable_plans(due_date, status)
    WHERE status NOT IN ('received','waived');

-- ---------------------------------------------------------------------------
-- receipts: money that actually arrived. plan_id is nullable on purpose - an
-- unplanned payment is still a payment, and refusing to record it would push
-- the fact out of the database.
-- ---------------------------------------------------------------------------
CREATE TABLE receipts (
    id          TEXT PRIMARY KEY,
    contract_id TEXT NOT NULL REFERENCES contracts(id),
    plan_id     TEXT REFERENCES receivable_plans(id),
    received_at TEXT NOT NULL,
    amount      REAL NOT NULL CHECK (amount > 0),
    method      TEXT,
    note        TEXT,
    created_at  TEXT NOT NULL
);
CREATE INDEX ix_receipts_contract ON receipts(contract_id, received_at);
CREATE INDEX ix_receipts_plan ON receipts(plan_id) WHERE plan_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- service_tickets: what happens after delivery. account_id NOT NULL; contract
-- and project are optional because plenty of support has neither.
-- ---------------------------------------------------------------------------
CREATE TABLE service_tickets (
    id          TEXT PRIMARY KEY,
    account_id  TEXT NOT NULL REFERENCES accounts(id),
    contract_id TEXT REFERENCES contracts(id),
    project_id  TEXT REFERENCES projects(id),
    -- Every other entity carries a human label; a ticket that cannot be read
    -- in a list is not usable.
    title       TEXT NOT NULL CHECK (title <> ''),
    opened_at   TEXT NOT NULL,
    kind        TEXT NOT NULL DEFAULT 'question'
                CHECK (kind IN ('question','incident','change_request','training','other')),
    -- S1 is "they are blocked right now". Deliberately not P0-P3: task
    -- importance is our priority, ticket severity is their impact.
    severity    TEXT NOT NULL DEFAULT 'S3' CHECK (severity IN ('S1','S2','S3','S4')),
    status      TEXT NOT NULL DEFAULT 'open'
                CHECK (status IN ('open','in_progress','waiting','resolved','closed')),
    assignee    TEXT,
    closed_at   TEXT,
    resolution  TEXT,
    version     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    CHECK (status <> 'closed' OR closed_at IS NOT NULL)
);
CREATE INDEX ix_service_tickets_account ON service_tickets(account_id, status);
CREATE INDEX ix_service_tickets_open ON service_tickets(severity, opened_at)
    WHERE status <> 'closed';
CREATE INDEX ix_service_tickets_project ON service_tickets(project_id) WHERE project_id IS NOT NULL;
CREATE INDEX ix_service_tickets_contract ON service_tickets(contract_id) WHERE contract_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- interactions: a conversation that happened. Hangs off whatever it was about
-- through subject_type/subject_id. Modes A and D use account / contact /
-- opportunity / contract / ticket / project; the vocabulary is the full entity
-- list so 006 never has to rebuild this table.
--
-- The minutes themselves are a document linked with doc_links link_type=
-- 'minutes'; `summary` here is the one-line record, not a substitute for the
-- original.
-- ---------------------------------------------------------------------------
CREATE TABLE interactions (
    id           TEXT PRIMARY KEY,
    subject_type TEXT NOT NULL
                 CHECK (subject_type IN ('objective','key_result','initiative','project','milestone','task',
                                         'account','contact','opportunity','application','contract','ticket',
                                         'document','product','content_piece','channel','release','campaign')),
    subject_id   TEXT NOT NULL,
    occurred_at  TEXT NOT NULL,
    channel      TEXT NOT NULL DEFAULT 'meeting'
                 CHECK (channel IN ('meeting','call','im','email','visit')),
    summary      TEXT,
    participants TEXT,
    owner        TEXT,
    created_at   TEXT NOT NULL
);
CREATE INDEX ix_interactions_subject ON interactions(subject_type, subject_id, occurred_at);
CREATE INDEX ix_interactions_occurred ON interactions(occurred_at);

-- ---------------------------------------------------------------------------
-- products: the hub of all three business lines - a contract sells one, a
-- content piece promotes one, a release iterates one. 006 adds
-- current_release_id and launch_date by ALTER TABLE ADD COLUMN; it does not
-- reshape this table.
-- ---------------------------------------------------------------------------
CREATE TABLE products (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL CHECK (name <> ''),
    kind        TEXT NOT NULL DEFAULT 'product'
                CHECK (kind IN ('product','service','solution')),
    status      TEXT NOT NULL DEFAULT 'concept'
                CHECK (status IN ('concept','developing','released','maintained','sunset')),
    positioning TEXT,
    repo_url    TEXT,
    owner       TEXT,
    version     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE INDEX ix_products_status ON products(status, kind);

-- ===========================================================================
-- Existing tables: two new links, and three CHECK vocabularies widened.
-- ===========================================================================

-- A delivery project is an ordinary project - there is no separate delivery
-- table. contract_id is the paid engagement it delivers; account_id is the
-- external counterparty of a project that neither a contract nor an
-- application drives (see the comment on applications). The views resolve them
-- with COALESCE and never write one from the other.
ALTER TABLE projects ADD COLUMN contract_id TEXT REFERENCES contracts(id);
ALTER TABLE projects ADD COLUMN account_id  TEXT REFERENCES accounts(id);
CREATE INDEX ix_projects_contract ON projects(contract_id) WHERE contract_id IS NOT NULL;
CREATE INDEX ix_projects_account  ON projects(account_id)  WHERE account_id  IS NOT NULL;

-- Where "the task table holds the actions" actually lands. project_id remains
-- the scheduling and rollup home; subject is the business home. One content
-- production task legitimately has project_id = the Q3 content initiative AND
-- subject = content_piece:xxx. Existence of the subject is checked by the Go
-- layer against entityTables, exactly like a dependency edge.
ALTER TABLE tasks ADD COLUMN subject_type TEXT
    CHECK (subject_type IS NULL OR
           subject_type IN ('objective','key_result','initiative','project','milestone','task',
                            'account','contact','opportunity','application','contract','ticket',
                            'document','product','content_piece','channel','release','campaign'));
ALTER TABLE tasks ADD COLUMN subject_id TEXT;
CREATE INDEX ix_tasks_subject ON tasks(subject_type, subject_id) WHERE subject_type IS NOT NULL;

-- ---------------------------------------------------------------------------
-- tags: rebuilt only to widen entity_type. The legacy `tags` field is a junk
-- drawer holding six dimensions at once - business line, customer, priority,
-- status, date, metric - and this migration exists largely to give five of
-- them real columns. What remains genuinely a tag must still be attachable to
-- the new objects.
--
-- SQLite cannot ALTER a CHECK, so the table is rebuilt: 004 did this three
-- times and the technique is copied verbatim. The vocabulary already includes
-- content_piece / channel / release / campaign so that 006 - which creates
-- those tables - needs no rebuild at all.
-- ---------------------------------------------------------------------------
CREATE TABLE tags_new (
    entity_type TEXT NOT NULL
                CHECK (entity_type IN ('objective','key_result','initiative','project','milestone','task',
                                       'account','contact','opportunity','application','contract','ticket',
                                       'document','product','content_piece','channel','release','campaign')),
    entity_id   TEXT NOT NULL,
    tag         TEXT NOT NULL CHECK (tag <> '' AND tag = trim(tag)),
    created_at  TEXT NOT NULL,
    PRIMARY KEY (entity_type, entity_id, tag)
);
INSERT INTO tags_new SELECT * FROM tags;
DROP TABLE tags;
ALTER TABLE tags_new RENAME TO tags;
CREATE INDEX ix_tags_tag ON tags(tag, entity_type);

-- ---------------------------------------------------------------------------
-- dependencies: rebuilt only to widen from_type/to_type. A contract can now
-- require an application to be won, a release can block a campaign.
--
-- v_blocked reads this table, and SQLite re-validates every view when a table
-- is renamed, so the view is dropped first and recreated below unchanged -
-- the same order 004 used when it rebuilt three tables at once.
-- ---------------------------------------------------------------------------
DROP VIEW v_blocked;

CREATE TABLE dependencies_new (
    id              TEXT PRIMARY KEY,
    from_type       TEXT NOT NULL
                    CHECK (from_type IN ('objective','key_result','initiative','project','milestone','task',
                                         'account','contact','opportunity','application','contract','ticket',
                                         'document','product','content_piece','channel','release','campaign')),
    from_id         TEXT NOT NULL,
    to_type         TEXT NOT NULL
                    CHECK (to_type IN ('objective','key_result','initiative','project','milestone','task',
                                       'account','contact','opportunity','application','contract','ticket',
                                       'document','product','content_piece','channel','release','campaign')),
    to_id           TEXT NOT NULL,
    dependency_type TEXT NOT NULL
                    CHECK (dependency_type IN ('blocks','requires','related','supports')),
    lag_days        INTEGER CHECK (lag_days IS NULL OR lag_days >= 0),
    weight          REAL CHECK (weight IS NULL OR (weight >= 0 AND weight <= 1)),
    note            TEXT,
    created_at      TEXT NOT NULL,
    CHECK (NOT (from_type = to_type AND from_id = to_id))
);
INSERT INTO dependencies_new SELECT * FROM dependencies;
DROP TABLE dependencies;
ALTER TABLE dependencies_new RENAME TO dependencies;
CREATE UNIQUE INDEX ux_dependencies ON dependencies(from_type, from_id, to_type, to_id, dependency_type);
CREATE INDEX ix_dependencies_to ON dependencies(to_type, to_id);

-- Recreated verbatim from 004; the widened vocabulary changes nothing it reads.
CREATE VIEW v_blocked AS
SELECT f.task_id, f.title, f.status, f.importance, f.project_id, f.project_name,
       f.waiting_for, f.next_review_at,
       d.from_id AS blocked_by_task_id,
       b.title   AS blocked_by_title,
       b.status  AS blocked_by_status
  FROM v_task_full f
  LEFT JOIN dependencies d
         ON d.to_type = 'task' AND d.to_id = f.task_id
        AND d.from_type = 'task' AND d.dependency_type IN ('blocks','requires')
  LEFT JOIN tasks b
         ON b.id = d.from_id AND b.status NOT IN ('done','cancelled','archived')
 WHERE f.is_open AND (f.status = 'waiting' OR b.id IS NOT NULL);

-- ---------------------------------------------------------------------------
-- events: rebuilt to widen event_type. entity_type has never carried a CHECK
-- here - the audit log must be able to record a verb about anything, including
-- an entity a later migration removes - so nothing changes on that column.
--
-- Six of the thirteen new verbs (amount_set, plan_change, phase_close,
-- project_cancel, target_change, due_change) are not speculative: they occur
-- in the legacy event log already. The other seven are the state changes this
-- migration makes possible, and `stage_changed` in particular is load-bearing:
-- v_opportunity_full derives stage dwell time from it rather than storing a
-- redundant timestamp column.
-- ---------------------------------------------------------------------------
CREATE TABLE events_new (
    id             TEXT PRIMARY KEY,
    entity_type    TEXT NOT NULL,
    entity_id      TEXT NOT NULL,
    event_type     TEXT NOT NULL
                   CHECK (event_type IN ('created','updated','status_changed','rescheduled',
                                         'importance_changed','deadline_changed','review_set',
                                         'linked','unlinked','completed','metric_updated',
                                         'note','migrated',
                                         'stage_changed','won','lost','signed','received',
                                         'published','released','amount_set','plan_change',
                                         'phase_close','project_cancel','target_change','due_change')),
    before_json    TEXT,
    after_json     TEXT,
    actor_type     TEXT NOT NULL CHECK (actor_type IN ('user','agent','ui','migration','system')),
    actor_id       TEXT,
    entry_point    TEXT NOT NULL DEFAULT 'cli'
                   CHECK (entry_point IN ('cli','bridge','http','import')),
    reason         TEXT,
    confirmed      INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0,1)),
    request_id     TEXT,
    correlation_id TEXT,
    occurred_at    TEXT NOT NULL
);
INSERT INTO events_new SELECT * FROM events;
DROP TABLE events;
ALTER TABLE events_new RENAME TO events;
CREATE INDEX ix_events_entity ON events(entity_type, entity_id, occurred_at);
CREATE INDEX ix_events_occurred ON events(occurred_at);
CREATE INDEX ix_events_correlation ON events(correlation_id) WHERE correlation_id IS NOT NULL;
CREATE INDEX ix_events_stage ON events(entity_type, entity_id, event_type, occurred_at)
    WHERE event_type = 'stage_changed';

-- ===========================================================================
-- Views. Read-time computation, all of it. Nothing here writes, and nothing
-- here replaces a number a person entered.
--
-- Views for modes B and C (v_content_pipeline, v_content_performance,
-- v_channel_growth, v_product_overview, v_campaign_effect) arrive in 006 with
-- the tables they read.
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- v_entity_index: the SQL mirror of the Go `entityTables` map - the id and
-- human label of every addressable object, in one place.
--
-- It exists because "is this edge dangling" cannot otherwise be asked in SQL:
-- an entity_type is a string, and SQLite has no way to look up a table by
-- name. 006 redefines this ONE view to add its four types, and every consumer
-- of it widens for free.
-- ---------------------------------------------------------------------------
-- ---------------------------------------------------------------------------
-- v_entity_types: the entity types this schema version knows how to resolve.
--
-- This MUST be a list of literals, never `SELECT DISTINCT entity_type FROM
-- v_entity_index`. v_entity_index draws its rows from the entity tables, so a
-- type whose table is empty simply does not appear there. The referential
-- guards below use this list to skip types they cannot resolve; deriving it
-- from data meant an empty table made its type "unknown", and a dangling
-- reference to it was silently NOT reported -- the check stopped checking at
-- exactly the moment a fresh instance most needed it. Adding a row here is a
-- schema fact, so it holds with zero rows in the table.
-- ---------------------------------------------------------------------------
CREATE VIEW v_entity_types AS
SELECT 'objective' AS entity_type
UNION ALL SELECT 'key_result'
UNION ALL SELECT 'initiative'
UNION ALL SELECT 'project'
UNION ALL SELECT 'milestone'
UNION ALL SELECT 'task'
UNION ALL SELECT 'account'
UNION ALL SELECT 'contact'
UNION ALL SELECT 'opportunity'
UNION ALL SELECT 'application'
UNION ALL SELECT 'contract'
UNION ALL SELECT 'ticket'
UNION ALL SELECT 'document'
UNION ALL SELECT 'product';

CREATE VIEW v_entity_index AS
SELECT 'objective'   AS entity_type, id, name  AS title FROM objectives
UNION ALL SELECT 'key_result',  id, name  FROM key_results
UNION ALL SELECT 'initiative',  id, name  FROM initiatives
UNION ALL SELECT 'project',     id, name  FROM projects
UNION ALL SELECT 'milestone',   id, name  FROM milestones
UNION ALL SELECT 'task',        id, title FROM tasks
UNION ALL SELECT 'account',     id, name  FROM accounts
UNION ALL SELECT 'contact',     id, name  FROM contacts
UNION ALL SELECT 'opportunity', id, name  FROM opportunities
UNION ALL SELECT 'application', id, name  FROM applications
UNION ALL SELECT 'contract',    id, name  FROM contracts
UNION ALL SELECT 'ticket',      id, title FROM service_tickets
UNION ALL SELECT 'document',    id, title FROM documents
UNION ALL SELECT 'product',     id, name  FROM products;

-- ---------------------------------------------------------------------------
-- v_document_current: one row per lineage - the newest version. Every
-- consumer asks "which one is current", and answering it by hand means
-- re-walking supersedes_id chains in application code.
-- ---------------------------------------------------------------------------
CREATE VIEW v_document_current AS
WITH ranked AS (
    SELECT d.id, d.kind, d.title, d.occurred_at, d.captured_at, d.review_at,
           d.lineage_id, d.supersedes_id, d.change_note, d.source, d.author_name,
           d.canonical_url, d.user_note, d.version, d.created_at, d.updated_at,
           ROW_NUMBER() OVER (PARTITION BY d.lineage_id ORDER BY d.created_at DESC, d.id DESC) AS rn,
           COUNT(*)     OVER (PARTITION BY d.lineage_id)                                       AS lineage_size
      FROM documents d
)
SELECT r.id AS document_id, r.kind, r.title, r.occurred_at, r.captured_at,
       r.review_at, r.lineage_id, r.supersedes_id, r.change_note,
       r.source, r.author_name, r.canonical_url, r.user_note,
       r.lineage_size AS version_count,
       r.version, r.created_at, r.updated_at,
       (SELECT f.rel_path FROM document_files f
         WHERE f.doc_id = r.id AND f.role = 'original')          AS original_rel_path,
       (SELECT COUNT(*) FROM document_files f WHERE f.doc_id = r.id) AS file_count,
       (SELECT COUNT(*) FROM doc_links l WHERE l.doc_id = r.id)      AS link_count
  FROM ranked r
 WHERE r.rn = 1;

-- ---------------------------------------------------------------------------
-- v_opportunity_full: the deal with everything a person needs to judge it -
-- who it is with, who decides, when we last spoke, and how long it has sat
-- where it sits.
--
-- days_in_stage comes from the `stage_changed` audit events, not from a
-- column. The alternative would be a second copy of the same fact that the
-- write path has to remember to update.
-- ---------------------------------------------------------------------------
CREATE VIEW v_opportunity_full AS
WITH stage_since AS (
    SELECT entity_id AS opportunity_id, MAX(occurred_at) AS at
      FROM events
     WHERE entity_type = 'opportunity' AND event_type = 'stage_changed'
     GROUP BY entity_id
),
last_touch AS (
    SELECT subject_id AS opportunity_id, occurred_at, channel, summary
      FROM (SELECT i.subject_id, i.occurred_at, i.channel, i.summary,
                   ROW_NUMBER() OVER (PARTITION BY i.subject_id
                                          ORDER BY i.occurred_at DESC, i.id DESC) AS rn
              FROM interactions i
             WHERE i.subject_type = 'opportunity')
     WHERE rn = 1
)
SELECT o.id AS opportunity_id, o.name, o.stage,
       (o.stage NOT IN ('won','lost')) AS is_open,
       o.source, o.source_batch, o.est_amount, o.win_probability,
       o.est_amount * COALESCE(o.win_probability, 0) AS weighted_amount,
       o.expected_sign_date, o.owner, o.next_step, o.lost_reason, o.closed_at,
       o.account_id, a.name AS account_name, a.short_name AS account_short_name,
       a.account_type, a.industry, a.region, a.owner AS account_owner,
       o.area_id, ar.name AS area_name,
       o.primary_contact_id,
       c.name      AS primary_contact_name,
       c.title     AS primary_contact_title,
       c.deal_role AS primary_contact_role,
       c.phone     AS primary_contact_phone,
       c.email     AS primary_contact_email,
       lt.occurred_at AS last_interaction_at,
       lt.channel     AS last_interaction_channel,
       lt.summary     AS last_interaction_summary,
       CASE WHEN lt.occurred_at IS NULL THEN NULL
            ELSE CAST(julianday((SELECT today FROM v_clock))
                    - julianday(date(lt.occurred_at)) AS INTEGER) END AS days_since_interaction,
       COALESCE(ss.at, o.created_at) AS stage_since,
       CAST(julianday((SELECT today FROM v_clock))
          - julianday(date(COALESCE(ss.at, o.created_at))) AS INTEGER) AS days_in_stage,
       (SELECT COUNT(*) FROM contracts k WHERE k.opportunity_id = o.id) AS contract_count,
       o.version, o.created_at, o.updated_at
  FROM opportunities o
  LEFT JOIN accounts    a  ON a.id  = o.account_id
  LEFT JOIN areas       ar ON ar.id = o.area_id
  LEFT JOIN contacts    c  ON c.id  = o.primary_contact_id
  LEFT JOIN stage_since ss ON ss.opportunity_id = o.id
  LEFT JOIN last_touch  lt ON lt.opportunity_id = o.id;

-- ---------------------------------------------------------------------------
-- v_pipeline: the funnel, grouped by business line and stage.
--
-- There is deliberately no grand total row. A consulting contract, a prize and
-- an impression count are not comparable quantities, and adding them would
-- produce a number that means nothing.
-- ---------------------------------------------------------------------------
CREATE VIEW v_pipeline AS
SELECT o.area_id,
       ar.name AS area_name,
       o.stage,
       (o.stage NOT IN ('won','lost')) AS is_open,
       COUNT(*)                                                          AS opportunity_count,
       COALESCE(SUM(o.est_amount), 0)                                    AS est_amount_total,
       COALESCE(SUM(o.est_amount * COALESCE(o.win_probability, 0)), 0)   AS weighted_amount_total,
       SUM(CASE WHEN o.est_amount IS NULL THEN 1 ELSE 0 END)             AS without_amount_count,
       MIN(o.expected_sign_date)                                         AS earliest_expected_sign_date
  FROM opportunities o
  LEFT JOIN areas ar ON ar.id = o.area_id
 GROUP BY o.area_id, ar.name, o.stage;

-- ---------------------------------------------------------------------------
-- v_account_360: everything about one counterparty on one row, across every
-- business line. The same organisation can be a customer, a competition
-- organiser and a distribution channel at once, which is exactly why this is
-- not a "customer" view.
-- ---------------------------------------------------------------------------
CREATE VIEW v_account_360 AS
WITH contract_rollup AS (
    SELECT k.account_id,
           COUNT(*)                                                            AS contract_count,
           SUM(CASE WHEN k.status IN ('signed','active') THEN 1 ELSE 0 END)    AS active_contract_count,
           SUM(CASE WHEN k.status <> 'draft' THEN k.amount ELSE 0 END)         AS contract_amount_total
      FROM contracts k GROUP BY k.account_id
),
receipt_rollup AS (
    SELECT k.account_id, SUM(r.amount) AS received_total, MAX(r.received_at) AS last_receipt_at
      FROM receipts r JOIN contracts k ON k.id = r.contract_id
     GROUP BY k.account_id
),
-- A project reaches an account either directly or through its contract; the
-- direct column wins when both are set (see the applications comment).
project_rollup AS (
    SELECT COALESCE(p.account_id, k.account_id) AS account_id,
           COUNT(*) AS project_count,
           SUM(CASE WHEN p.status NOT IN ('done','cancelled','archived')
                    THEN 1 ELSE 0 END) AS open_project_count
      FROM projects p
      LEFT JOIN contracts k ON k.id = p.contract_id
     WHERE COALESCE(p.account_id, k.account_id) IS NOT NULL
     GROUP BY COALESCE(p.account_id, k.account_id)
),
last_touch AS (
    SELECT subject_id AS account_id, MAX(occurred_at) AS at
      FROM interactions WHERE subject_type = 'account' GROUP BY subject_id
)
SELECT a.id AS account_id, a.name, a.short_name, a.account_type, a.industry,
       a.region, a.status, a.owner,
       (SELECT COUNT(*) FROM contacts c
         WHERE c.account_id = a.id AND c.status = 'active')                    AS active_contact_count,
       (SELECT COUNT(*) FROM contacts c
         WHERE c.account_id = a.id AND c.status = 'active'
           AND c.deal_role = 'decider')                                        AS decider_count,
       (SELECT COUNT(*) FROM opportunities o
         WHERE o.account_id = a.id AND o.stage NOT IN ('won','lost'))          AS open_opportunity_count,
       (SELECT COALESCE(SUM(o.est_amount), 0) FROM opportunities o
         WHERE o.account_id = a.id AND o.stage NOT IN ('won','lost'))          AS open_opportunity_amount,
       COALESCE(cr.contract_count, 0)                                          AS contract_count,
       COALESCE(cr.active_contract_count, 0)                                   AS active_contract_count,
       COALESCE(cr.contract_amount_total, 0)                                   AS contract_amount_total,
       COALESCE(rr.received_total, 0)                                          AS received_total,
       COALESCE(cr.contract_amount_total, 0) - COALESCE(rr.received_total, 0)   AS receivable_balance,
       rr.last_receipt_at,
       COALESCE(pr.project_count, 0)                                           AS project_count,
       COALESCE(pr.open_project_count, 0)                                       AS open_project_count,
       (SELECT COUNT(*) FROM service_tickets t
         WHERE t.account_id = a.id AND t.status <> 'closed')                    AS open_ticket_count,
       (SELECT COUNT(*) FROM applications ap
         WHERE ap.account_id = a.id
           AND ap.stage NOT IN ('won','rejected','withdrawn'))                  AS open_application_count,
       (SELECT COUNT(*) FROM doc_links l
         WHERE l.entity_type = 'account' AND l.entity_id = a.id
           AND l.link_type = 'dossier')                                         AS dossier_count,
       lt.at AS last_interaction_at,
       a.version, a.created_at, a.updated_at
  FROM accounts a
  LEFT JOIN contract_rollup cr ON cr.account_id = a.id
  LEFT JOIN receipt_rollup  rr ON rr.account_id = a.id
  LEFT JOIN project_rollup  pr ON pr.account_id = a.id
  LEFT JOIN last_touch      lt ON lt.account_id = a.id;

-- ---------------------------------------------------------------------------
-- v_contract_receivable: contract amount, planned amount, received amount,
-- outstanding, collection rate - and, beside them, whether the declared amount
-- and the plan total agree.
--
-- plan_mismatch is a FACT, not an error. The contract amount is authoritative
-- and is never adjusted to match the plans, exactly as v_metric_rollup states
-- a rollup without overwriting a declared value. Money is compared with a
-- half-cent tolerance because REAL is not exact.
-- ---------------------------------------------------------------------------
CREATE VIEW v_contract_receivable AS
WITH plan_rollup AS (
    SELECT contract_id,
           COUNT(*)      AS plan_count,
           SUM(amount)   AS planned_amount,
           SUM(CASE WHEN status = 'waived' THEN amount ELSE 0 END) AS waived_amount
      FROM receivable_plans GROUP BY contract_id
),
receipt_rollup AS (
    SELECT contract_id, COUNT(*) AS receipt_count, SUM(amount) AS received_amount,
           MAX(received_at) AS last_receipt_at
      FROM receipts GROUP BY contract_id
)
SELECT k.id AS contract_id, k.contract_no, k.name, k.kind, k.status, k.currency,
       k.sign_date, k.start_date, k.end_date, k.payment_terms,
       k.account_id, a.name AS account_name,
       k.opportunity_id, k.application_id,
       k.amount AS declared_amount,
       COALESCE(pr.planned_amount, 0)  AS planned_amount,
       COALESCE(pr.plan_count, 0)      AS plan_count,
       COALESCE(pr.waived_amount, 0)   AS waived_amount,
       COALESCE(rr.received_amount, 0) AS received_amount,
       COALESCE(rr.receipt_count, 0)   AS receipt_count,
       rr.last_receipt_at,
       k.amount - COALESCE(rr.received_amount, 0) AS outstanding_amount,
       ROUND(COALESCE(rr.received_amount, 0) / k.amount, 4) AS received_ratio,
       COALESCE(pr.planned_amount, 0) - k.amount AS plan_gap,
       (pr.plan_count IS NOT NULL
        AND ABS(COALESCE(pr.planned_amount, 0) - k.amount) > 0.005) AS plan_mismatch,
       k.unit_price, k.quantity,
       CASE WHEN k.unit_price IS NOT NULL AND k.quantity IS NOT NULL
            THEN k.unit_price * k.quantity END AS line_amount,
       (k.unit_price IS NOT NULL AND k.quantity IS NOT NULL
        AND ABS(k.unit_price * k.quantity - k.amount) > 0.005) AS line_mismatch,
       (COALESCE(rr.received_amount, 0) - k.amount > 0.005)    AS over_received,
       k.version, k.created_at, k.updated_at
  FROM contracts k
  LEFT JOIN accounts       a  ON a.id = k.account_id
  LEFT JOIN plan_rollup    pr ON pr.contract_id = k.id
  LEFT JOIN receipt_rollup rr ON rr.contract_id = k.id;

-- ---------------------------------------------------------------------------
-- v_receivable_aging: one row per still-open plan instalment, bucketed by how
-- far past due it is. A receipt that names its plan reduces that plan's open
-- amount, so a part payment ages only its remainder.
-- ---------------------------------------------------------------------------
CREATE VIEW v_receivable_aging AS
WITH plan_receipts AS (
    SELECT plan_id, SUM(amount) AS received_amount
      FROM receipts WHERE plan_id IS NOT NULL GROUP BY plan_id
),
open_plans AS (
    SELECT rp.id AS plan_id, rp.contract_id, rp.seq, rp.due_date, rp.amount,
           rp.status, rp.condition_note,
           rp.amount - COALESCE(pr.received_amount, 0) AS open_amount,
           CAST(julianday((SELECT today FROM v_clock)) - julianday(rp.due_date) AS INTEGER) AS days_overdue
      FROM receivable_plans rp
      LEFT JOIN plan_receipts pr ON pr.plan_id = rp.id
     WHERE rp.status NOT IN ('received','waived')
)
SELECT p.plan_id, p.contract_id, k.contract_no, k.name AS contract_name,
       k.kind AS contract_kind, k.status AS contract_status, k.currency,
       k.account_id, a.name AS account_name,
       p.seq, p.due_date, p.amount AS planned_amount, p.open_amount,
       p.status, p.condition_note, p.days_overdue,
       CASE WHEN p.days_overdue <= 0  THEN 'not_due'
            WHEN p.days_overdue <= 30 THEN '1_30'
            WHEN p.days_overdue <= 60 THEN '31_60'
            WHEN p.days_overdue <= 90 THEN '61_90'
            ELSE '90_plus' END AS aging_bucket
  FROM open_plans p
  JOIN contracts k ON k.id = p.contract_id
  LEFT JOIN accounts a ON a.id = k.account_id;

CREATE VIEW v_receivable_overdue AS
SELECT * FROM v_receivable_aging WHERE days_overdue > 0;

-- ---------------------------------------------------------------------------
-- v_project_business: a delivery project with the chain behind it - contract,
-- account, and the opportunity or application it came from. Only projects that
-- actually have a business counterparty appear; the rest are just projects and
-- the existing project views already cover them.
--
-- The application is read with a scalar subquery rather than a join so that a
-- project linked from two applications still yields one row.
-- ---------------------------------------------------------------------------
CREATE VIEW v_project_business AS
SELECT p.id AS project_id, p.name AS project_name, p.kind AS project_kind,
       p.status, p.stage, p.importance, p.target_date, p.hard_due_at,
       p.next_review_at, p.completed_at,
       p.contract_id, k.contract_no, k.name AS contract_name, k.kind AS contract_kind,
       k.status AS contract_status, k.amount AS contract_amount, k.currency,
       k.sign_date, k.start_date AS contract_start_date, k.end_date AS contract_end_date,
       COALESCE(cr.received_amount, 0)     AS received_amount,
       cr.outstanding_amount,
       COALESCE(p.account_id, k.account_id) AS account_id,
       COALESCE(pa.name, ka.name)           AS account_name,
       (p.account_id IS NOT NULL AND k.account_id IS NOT NULL
        AND p.account_id <> k.account_id)   AS account_link_conflict,
       k.opportunity_id, o.name AS opportunity_name, o.stage AS opportunity_stage,
       (SELECT ap.id    FROM applications ap
         WHERE ap.project_id = p.id ORDER BY ap.created_at, ap.id LIMIT 1) AS application_id,
       (SELECT ap.name  FROM applications ap
         WHERE ap.project_id = p.id ORDER BY ap.created_at, ap.id LIMIT 1) AS application_name,
       (SELECT ap.stage FROM applications ap
         WHERE ap.project_id = p.id ORDER BY ap.created_at, ap.id LIMIT 1) AS application_stage,
       p.initiative_id, i.name AS initiative_name, i.area_id, ar.name AS area_name,
       (SELECT COUNT(*) FROM service_tickets t
         WHERE t.project_id = p.id AND t.status <> 'closed')  AS open_ticket_count,
       (SELECT COUNT(*) FROM tasks t
         WHERE t.project_id = p.id
           AND t.status NOT IN ('done','cancelled','archived')) AS open_task_count,
       p.version, p.created_at, p.updated_at
  FROM projects p
  LEFT JOIN contracts             k  ON k.id  = p.contract_id
  LEFT JOIN v_contract_receivable cr ON cr.contract_id = p.contract_id
  LEFT JOIN accounts              pa ON pa.id = p.account_id
  LEFT JOIN accounts              ka ON ka.id = k.account_id
  LEFT JOIN opportunities         o  ON o.id  = k.opportunity_id
  LEFT JOIN initiatives           i  ON i.id  = p.initiative_id
  LEFT JOIN areas                 ar ON ar.id = i.area_id
 WHERE p.contract_id IS NOT NULL
    OR p.account_id  IS NOT NULL
    OR EXISTS (SELECT 1 FROM applications ap WHERE ap.project_id = p.id);

-- ---------------------------------------------------------------------------
-- v_service_load: open tickets by counterparty and severity - who is currently
-- carrying unresolved problems, and how old the oldest one is.
-- ---------------------------------------------------------------------------
CREATE VIEW v_service_load AS
SELECT t.account_id, a.name AS account_name, a.short_name AS account_short_name,
       t.severity,
       COUNT(*)                                                        AS open_ticket_count,
       SUM(CASE WHEN t.status = 'open'     THEN 1 ELSE 0 END)          AS untriaged_count,
       SUM(CASE WHEN t.status = 'waiting'  THEN 1 ELSE 0 END)          AS waiting_count,
       SUM(CASE WHEN t.status = 'resolved' THEN 1 ELSE 0 END)          AS resolved_not_closed_count,
       SUM(CASE WHEN t.assignee IS NULL OR t.assignee = ''
                THEN 1 ELSE 0 END)                                     AS unassigned_count,
       MIN(t.opened_at)                                                AS oldest_opened_at,
       CAST(julianday((SELECT today FROM v_clock))
          - julianday(date(MIN(t.opened_at))) AS INTEGER)              AS oldest_age_days
  FROM service_tickets t
  LEFT JOIN accounts a ON a.id = t.account_id
 WHERE t.status <> 'closed'
 GROUP BY t.account_id, a.name, a.short_name, t.severity;

-- ---------------------------------------------------------------------------
-- v_application_pipeline: the same funnel treatment for mode D, grouped by
-- business line, kind and stage. Prize money is totalled here and nowhere near
-- v_pipeline: a prize and a contract are not the same currency of confidence.
-- ---------------------------------------------------------------------------
CREATE VIEW v_application_pipeline AS
SELECT ap.area_id, ar.name AS area_name, ap.kind, ap.stage,
       (ap.stage NOT IN ('won','rejected','withdrawn')) AS is_open,
       COUNT(*)                                                    AS application_count,
       COALESCE(SUM(ap.prize_amount), 0)                           AS prize_amount_total,
       SUM(CASE WHEN ap.next_step IS NULL OR ap.next_step = ''
                THEN 1 ELSE 0 END)                                 AS without_next_step_count,
       SUM(CASE WHEN ap.stage IN ('submitted','under_review')
                 AND ap.submitted_at IS NOT NULL
                 AND julianday((SELECT today FROM v_clock)) - julianday(date(ap.submitted_at))
                     > (SELECT CAST(value AS INTEGER) FROM ops_settings
                         WHERE key = 'biz_application_stale_days')
                THEN 1 ELSE 0 END)                                 AS stalled_count,
       MIN(ap.submitted_at)                                        AS earliest_submitted_at,
       MAX(ap.decided_at)                                          AS latest_decided_at
  FROM applications ap
  LEFT JOIN areas ar ON ar.id = ap.area_id
 GROUP BY ap.area_id, ar.name, ap.kind, ap.stage;

-- ---------------------------------------------------------------------------
-- v_doc_review_due: documents whose "when to re-evaluate" date has arrived.
-- A version that something else supersedes is excluded - re-evaluating a
-- replaced draft is busywork.
-- ---------------------------------------------------------------------------
CREATE VIEW v_doc_review_due AS
SELECT d.id AS document_id, d.kind, d.title, d.occurred_at, d.review_at,
       CAST(julianday((SELECT today FROM v_clock)) - julianday(d.review_at) AS INTEGER) AS days_overdue,
       d.lineage_id, d.change_note,
       (SELECT COUNT(*) FROM doc_links l WHERE l.doc_id = d.id) AS link_count,
       d.version, d.created_at, d.updated_at
  FROM documents d
 WHERE d.review_at IS NOT NULL
   AND d.review_at <= (SELECT today FROM v_clock)
   AND NOT EXISTS (SELECT 1 FROM documents s WHERE s.supersedes_id = d.id);

-- ---------------------------------------------------------------------------
-- v_metric_trend: any subject, any metric, in order, with the change against
-- the previous sample.
--
-- No subject title is resolved here on purpose: half the subject types this
-- series can carry (channel, content_piece, release, campaign) do not have
-- tables until 006, and a view cannot reference a table that does not exist.
-- Callers already know which subject they asked about.
-- ---------------------------------------------------------------------------
CREATE VIEW v_metric_trend AS
SELECT m.subject_type, m.subject_id, m.metric_name, m.unit, m.sampled_at,
       m.value, m.source, m.note,
       LAG(m.value)      OVER w AS previous_value,
       LAG(m.sampled_at) OVER w AS previous_sampled_at,
       m.value - LAG(m.value) OVER w AS delta,
       CASE WHEN LAG(m.value) OVER w IS NOT NULL AND LAG(m.value) OVER w <> 0
            THEN ROUND((m.value - LAG(m.value) OVER w) / ABS(LAG(m.value) OVER w), 4)
       END AS delta_ratio,
       CASE WHEN LAG(m.sampled_at) OVER w IS NOT NULL
            THEN CAST(julianday(date(m.sampled_at))
                    - julianday(date(LAG(m.sampled_at) OVER w)) AS INTEGER)
       END AS days_since_previous,
       ROW_NUMBER() OVER w AS sample_seq,
       m.id AS sample_id
  FROM metric_samples m
WINDOW w AS (PARTITION BY m.subject_type, m.subject_id, m.metric_name
                 ORDER BY m.sampled_at);

-- ---------------------------------------------------------------------------
-- v_biz_quality_issues: the business-side counterpart of
-- v_data_quality_issues, in the same shape (entity_type, entity_id, title,
-- issue, detail).
--
-- Everything here is a cross-row aggregate or a cross-table absence, which is
-- precisely what a CHECK cannot express. Every row STATES a fact. Nothing is
-- blocked, nothing is auto-corrected, and no row suggests what to do about it.
-- Rules that need the 006 tables (content scheduled but unpublished, product
-- released without a release row, campaign ended without metrics) join this
-- view when those tables exist.
-- ---------------------------------------------------------------------------
CREATE VIEW v_biz_quality_issues AS
-- A deal nobody has touched, sitting in the same stage.
SELECT 'opportunity' AS entity_type, f.opportunity_id AS entity_id, f.name AS title,
       'opportunity_stalled' AS issue,
       'open opportunity has not changed stage or been contacted within the stale window' AS detail
  FROM v_opportunity_full f
 WHERE f.is_open
   AND f.days_in_stage > (SELECT CAST(value AS INTEGER) FROM ops_settings
                           WHERE key = 'biz_opportunity_stale_days')
   AND (f.last_interaction_at IS NULL
        OR f.days_since_interaction > (SELECT CAST(value AS INTEGER) FROM ops_settings
                                        WHERE key = 'biz_opportunity_stale_days'))
UNION ALL
SELECT 'opportunity', o.id, o.name, 'won_opportunity_without_contract',
       'opportunity is won but no contract records what was agreed'
  FROM opportunities o
 WHERE o.stage = 'won'
   AND NOT EXISTS (SELECT 1 FROM contracts k WHERE k.opportunity_id = o.id)
UNION ALL
SELECT 'opportunity', o.id, o.name, 'open_opportunity_without_next_step',
       'open opportunity records no next step'
  FROM opportunities o
 WHERE o.stage NOT IN ('won','lost')
   AND (o.next_step IS NULL OR o.next_step = '')
UNION ALL
-- Signed work with nowhere to execute it.
SELECT 'contract', k.id, k.name, 'contract_without_delivery_project',
       'contract is signed or active but no project delivers it'
  FROM contracts k
 WHERE k.status IN ('signed','active')
   AND k.kind IN ('sales','piecework')
   AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.contract_id = k.id)
UNION ALL
-- The headline rule: the declared amount and the receivable plan disagree.
-- Stated, never repaired. contracts.amount stays exactly as it was written.
SELECT 'contract', cr.contract_id, cr.name, 'contract_amount_plan_mismatch',
       'contract amount and the sum of its receivable plans disagree'
  FROM v_contract_receivable cr
 WHERE cr.plan_mismatch
UNION ALL
SELECT 'contract', cr.contract_id, cr.name, 'receipts_exceed_contract_amount',
       'receipts recorded against this contract exceed the contract amount'
  FROM v_contract_receivable cr
 WHERE cr.over_received
UNION ALL
SELECT 'contract', cr.contract_id, cr.name, 'piecework_line_mismatch',
       'unit price times quantity does not equal the contract amount'
  FROM v_contract_receivable cr
 WHERE cr.line_mismatch
UNION ALL
SELECT 'contract', k.id, k.name, 'signed_contract_without_plan',
       'contract is signed or active but has no receivable plan at all'
  FROM contracts k
 WHERE k.status IN ('signed','active')
   AND NOT EXISTS (SELECT 1 FROM receivable_plans rp WHERE rp.contract_id = k.id)
UNION ALL
SELECT 'contract', ag.contract_id, ag.contract_name, 'receivable_plan_overdue',
       'a receivable instalment is past its due date and not marked received'
  FROM v_receivable_overdue ag
UNION ALL
-- Mode D: the announced prize and the receivable prize are separate facts.
SELECT 'application', ap.id, ap.name, 'won_application_without_prize_contract',
       'application is won but no contract records the prize or the award'
  FROM applications ap
 WHERE ap.stage = 'won'
   AND NOT EXISTS (SELECT 1 FROM contracts k WHERE k.application_id = ap.id)
UNION ALL
SELECT 'application', ap.id, ap.name, 'prize_amount_contract_mismatch',
       'announced prize amount and the contracted amount disagree'
  FROM applications ap
 WHERE ap.prize_amount IS NOT NULL
   AND EXISTS (SELECT 1 FROM contracts k WHERE k.application_id = ap.id)
   AND ABS(ap.prize_amount
           - (SELECT SUM(k.amount) FROM contracts k WHERE k.application_id = ap.id)) > 0.005
UNION ALL
SELECT 'application', ap.id, ap.name, 'application_submitted_without_result',
       'application has been submitted past the stale window with no decision and no next step'
  FROM applications ap
 WHERE ap.stage IN ('submitted','under_review')
   AND ap.submitted_at IS NOT NULL
   AND julianday((SELECT today FROM v_clock)) - julianday(date(ap.submitted_at))
       > (SELECT CAST(value AS INTEGER) FROM ops_settings WHERE key = 'biz_application_stale_days')
   AND (ap.next_step IS NULL OR ap.next_step = '')
UNION ALL
-- Relationship hygiene.
SELECT 'contact', c.id, c.name, 'contact_without_channel',
       'active contact has no phone, email or wechat, so there is no way to reach them'
  FROM contacts c
 WHERE c.status = 'active'
   AND COALESCE(c.phone, '')  = ''
   AND COALESCE(c.email, '')  = ''
   AND COALESCE(c.wechat, '') = ''
UNION ALL
SELECT 'account', a.id, a.name, 'account_without_dossier',
       'customer account has no dossier document'
  FROM accounts a
 WHERE a.account_type = 'customer' AND a.status = 'active'
   AND NOT EXISTS (SELECT 1 FROM doc_links l
                    WHERE l.entity_type = 'account' AND l.entity_id = a.id
                      AND l.link_type = 'dossier')
UNION ALL
SELECT 'project', vb.project_id, vb.project_name, 'account_link_conflict',
       'project names one account while its contract names a different one'
  FROM v_project_business vb
 WHERE vb.account_link_conflict
UNION ALL
SELECT 'project', vb.project_id, vb.project_name, 'delivered_with_open_tickets',
       'delivery project is finished but service tickets on it are still open'
  FROM v_project_business vb
 WHERE vb.status IN ('done','archived') AND vb.open_ticket_count > 0
UNION ALL
SELECT 'document', d.document_id, d.title, 'document_review_overdue',
       'document is past its re-evaluation date and has not been superseded'
  FROM v_doc_review_due d
UNION ALL
SELECT 'document', d.id, d.title, 'document_without_file',
       'document has no file on disk at all'
  FROM documents d
 WHERE NOT EXISTS (SELECT 1 FROM document_files f WHERE f.doc_id = d.id)
UNION ALL
-- Dangling soft edges and links. Only ends whose type v_entity_index knows are
-- checked; a type it does not yet carry is left to `doctor` rather than
-- reported as a false positive.
SELECT 'context_edge', e.id, e.from_type || ':' || e.from_id || ' -> ' || e.to_type || ':' || e.to_id,
       'dangling_context_edge',
       'one end of this soft relation no longer exists'
  FROM context_edges e
 WHERE (e.from_type IN (SELECT entity_type FROM v_entity_types)
        AND NOT EXISTS (SELECT 1 FROM v_entity_index x
                         WHERE x.entity_type = e.from_type AND x.id = e.from_id))
    OR (e.to_type IN (SELECT entity_type FROM v_entity_types)
        AND NOT EXISTS (SELECT 1 FROM v_entity_index x
                         WHERE x.entity_type = e.to_type AND x.id = e.to_id))
UNION ALL
SELECT 'doc_link', l.id, l.entity_type || ':' || l.entity_id, 'dangling_doc_link',
       'document is linked to an object that no longer exists'
  FROM doc_links l
 WHERE l.entity_type IN (SELECT entity_type FROM v_entity_types)
   AND NOT EXISTS (SELECT 1 FROM v_entity_index x
                    WHERE x.entity_type = l.entity_type AND x.id = l.entity_id)
UNION ALL
SELECT 'task', t.id, t.title, 'dangling_task_subject',
       'task points at a business subject that no longer exists'
  FROM tasks t
 WHERE t.subject_type IS NOT NULL
   AND t.subject_type IN (SELECT entity_type FROM v_entity_types)
   AND NOT EXISTS (SELECT 1 FROM v_entity_index x
                    WHERE x.entity_type = t.subject_type AND x.id = t.subject_id);
