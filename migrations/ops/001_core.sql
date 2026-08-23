-- ops.db v2 core: the single source of truth for goals, projects, tasks,
-- plans and execution events (B+ design §7).
--
-- Conventions:
--   * ids            TEXT, prefixed ULID ("task_01J...") - always strings
--   * timestamps     TEXT, RFC 3339 with timezone
--   * calendar days  TEXT, YYYY-MM-DD
--   * version        INTEGER, optimistic concurrency on editable objects

PRAGMA application_id = 1296649779;  -- 'Mnis'

-- ---------------------------------------------------------------------------
-- Instance settings that deterministic views need to read.
-- Kept in the database (not config.json) so a view can be self-contained.
-- ---------------------------------------------------------------------------
CREATE TABLE ops_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO ops_settings (key, value, updated_at) VALUES
    ('default_weekday_minutes', '240', '1970-01-01T00:00:00Z'),
    ('default_weekend_minutes', '120', '1970-01-01T00:00:00Z');

-- ---------------------------------------------------------------------------
-- Stable four-level hierarchy: Area > Initiative > Project > Task.
-- Objectives / KRs sit beside it as an outcome system, never as a task tree.
-- ---------------------------------------------------------------------------
CREATE TABLE areas (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'active'
               CHECK (status IN ('active','archived')),
    sort_order INTEGER NOT NULL DEFAULT 0,
    note       TEXT,
    version    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE initiatives (
    id          TEXT PRIMARY KEY,
    area_id     TEXT NOT NULL REFERENCES areas(id),
    name        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'active'
                CHECK (status IN ('active','paused','done','archived')),
    start_date  TEXT CHECK (start_date IS NULL OR start_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    review_date TEXT CHECK (review_date IS NULL OR review_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    description TEXT,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    version     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE INDEX ix_initiatives_area ON initiatives(area_id, status);

CREATE TABLE objectives (
    id         TEXT PRIMARY KEY,
    area_id    TEXT REFERENCES areas(id),
    name       TEXT NOT NULL,
    horizon    TEXT,                       -- e.g. 2026Q3
    status     TEXT NOT NULL DEFAULT 'active'
               CHECK (status IN ('active','done','dropped','archived')),
    version    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- A KR carries exactly one measurement definition.
CREATE TABLE key_results (
    id            TEXT PRIMARY KEY,
    objective_id  TEXT NOT NULL REFERENCES objectives(id),
    name          TEXT NOT NULL,
    metric_name   TEXT NOT NULL,
    metric_unit   TEXT,
    target_value  REAL,
    current_value REAL,
    horizon       TEXT,
    status        TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','done','dropped','archived')),
    version       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
CREATE INDEX ix_key_results_objective ON key_results(objective_id);

-- ---------------------------------------------------------------------------
-- Projects: bounded work with an explicit outcome.
-- target_date is an intention; hard_due_at is an external constraint.
-- ---------------------------------------------------------------------------
CREATE TABLE projects (
    id                  TEXT PRIMARY KEY,
    initiative_id       TEXT REFERENCES initiatives(id),
    name                TEXT NOT NULL,
    description         TEXT,
    status              TEXT NOT NULL DEFAULT 'planned'
                        CHECK (status IN ('planned','active','waiting','paused','done','cancelled','archived')),
    stage               TEXT CHECK (stage IS NULL OR stage IN ('discover','plan','build','deliver','operate','close')),
    importance          TEXT NOT NULL DEFAULT 'P2'
                        CHECK (importance IN ('P0','P1','P2','P3')),
    target_date         TEXT CHECK (target_date IS NULL OR target_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    hard_due_at         TEXT,
    next_review_at      TEXT CHECK (next_review_at IS NULL OR next_review_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    outcome             TEXT,
    completion_criteria TEXT,
    legacy_ref          TEXT,
    version             INTEGER NOT NULL DEFAULT 1,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    completed_at        TEXT
);
CREATE INDEX ix_projects_initiative ON projects(initiative_id, status);
CREATE INDEX ix_projects_status ON projects(status, importance);

-- ---------------------------------------------------------------------------
-- Tasks: executable actions only. Never objectives, never projects.
-- ---------------------------------------------------------------------------
CREATE TABLE tasks (
    id                  TEXT PRIMARY KEY,
    project_id          TEXT REFERENCES projects(id),
    parent_task_id      TEXT REFERENCES tasks(id),
    title               TEXT NOT NULL,
    detail              TEXT,
    completion_criteria TEXT,
    status              TEXT NOT NULL DEFAULT 'todo'
                        CHECK (status IN ('inbox','todo','doing','waiting','done','cancelled','archived')),
    importance          TEXT NOT NULL DEFAULT 'P2'
                        CHECK (importance IN ('P0','P1','P2','P3')),
    hard_due_at         TEXT,
    earliest_start_at   TEXT,
    next_review_at      TEXT CHECK (next_review_at IS NULL OR next_review_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    estimate_minutes    INTEGER CHECK (estimate_minutes IS NULL OR estimate_minutes > 0),
    waiting_for         TEXT,
    legacy_ref          TEXT,
    legacy_due_date     TEXT,   -- unclassified date carried over from the old model
    version             INTEGER NOT NULL DEFAULT 1,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    completed_at        TEXT,
    CHECK (parent_task_id IS NULL OR parent_task_id <> id)
);
CREATE INDEX ix_tasks_project ON tasks(project_id, status);
CREATE INDEX ix_tasks_status ON tasks(status, importance);
CREATE INDEX ix_tasks_hard_due ON tasks(hard_due_at) WHERE hard_due_at IS NOT NULL;
CREATE INDEX ix_tasks_review ON tasks(next_review_at) WHERE next_review_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Intent to work on a day, kept separate from the task itself.
-- Rescheduling supersedes a row and inserts a new one; it never overwrites.
-- ---------------------------------------------------------------------------
CREATE TABLE task_schedules (
    id              TEXT PRIMARY KEY,
    task_id         TEXT NOT NULL REFERENCES tasks(id),
    planned_date    TEXT NOT NULL CHECK (planned_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    time_slot       TEXT CHECK (time_slot IS NULL OR time_slot IN ('morning','afternoon','evening')),
    start_at        TEXT,
    end_at          TEXT,
    planned_minutes INTEGER CHECK (planned_minutes IS NULL OR planned_minutes > 0),
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','completed','superseded','cancelled')),
    superseded_by   TEXT REFERENCES task_schedules(id),
    created_by      TEXT NOT NULL DEFAULT 'user_ui'
                    CHECK (created_by IN ('user_ui','agent','cli','migration')),
    note            TEXT,
    created_at      TEXT NOT NULL
);
-- At most one live plan per task: a second one would make "which day" ambiguous.
CREATE UNIQUE INDEX ux_task_schedules_active ON task_schedules(task_id) WHERE status = 'active';
CREATE INDEX ix_task_schedules_date ON task_schedules(planned_date, status);

-- ---------------------------------------------------------------------------
-- Capacity is declared by the user, never inferred by the system.
-- ---------------------------------------------------------------------------
CREATE TABLE daily_capacity (
    date             TEXT PRIMARY KEY CHECK (date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    available_minutes INTEGER NOT NULL CHECK (available_minutes >= 0),
    note             TEXT,
    updated_at       TEXT NOT NULL
);

CREATE TABLE project_kr_links (
    project_id    TEXT NOT NULL REFERENCES projects(id),
    key_result_id TEXT NOT NULL REFERENCES key_results(id),
    note          TEXT,
    created_at    TEXT NOT NULL,
    PRIMARY KEY (project_id, key_result_id)
);

CREATE TABLE task_dependencies (
    id              TEXT PRIMARY KEY,
    from_task_id    TEXT NOT NULL REFERENCES tasks(id),
    to_task_id      TEXT NOT NULL REFERENCES tasks(id),
    dependency_type TEXT NOT NULL CHECK (dependency_type IN ('blocks','requires','related')),
    note            TEXT,
    created_at      TEXT NOT NULL,
    CHECK (from_task_id <> to_task_id)
);
CREATE UNIQUE INDEX ux_task_dependencies ON task_dependencies(from_task_id, to_task_id, dependency_type);
CREATE INDEX ix_task_dependencies_to ON task_dependencies(to_task_id);

-- ---------------------------------------------------------------------------
-- Audit log. Read for history, never as the source of current state.
-- ---------------------------------------------------------------------------
CREATE TABLE events (
    id             TEXT PRIMARY KEY,
    entity_type    TEXT NOT NULL,
    entity_id      TEXT NOT NULL,
    event_type     TEXT NOT NULL
                   CHECK (event_type IN ('created','updated','status_changed','rescheduled',
                                         'importance_changed','deadline_changed','review_set',
                                         'linked','unlinked','completed','note','migrated')),
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
CREATE INDEX ix_events_entity ON events(entity_type, entity_id, occurred_at);
CREATE INDEX ix_events_occurred ON events(occurred_at);
CREATE INDEX ix_events_correlation ON events(correlation_id) WHERE correlation_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Idempotency ledger: the same request_id + payload replays its first result.
-- ---------------------------------------------------------------------------
CREATE TABLE command_requests (
    request_id   TEXT PRIMARY KEY,
    command_name TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    actor        TEXT NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('started','completed','failed')),
    result_json  TEXT,
    error_code   TEXT,
    started_at   TEXT NOT NULL,
    completed_at TEXT
);
