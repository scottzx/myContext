-- ops.db v3: project and task carry execution; a milestone is a third thing.
-- A milestone is not work - it is the dated point work is aiming at, so it
-- owns the tasks that reach it rather than sitting among them.
--
--   parent/child project   projects.parent_project_id
--   sprint                 projects.kind = 'sprint' + start_date/end_date
--   parent/child task      tasks.parent_task_id
--   milestone              milestones, with tasks.milestone_id pointing at it
--   related / dependency   dependencies (an edge is a relation, not an element)
--
-- Shaped by measured data in the legacy tree, not by speculation:
--   * 10 SUB nodes are time boxes with a parent and a window -> sprints.
--   * 37 live milestones carry a date and a metric, 4 without a project.
--   * metric is a three-level rollup and 9 of 11 project metrics differ from
--     their KR's -> metric columns at every level, plus a view that states
--     the rolled-up sum without overwriting the declared one.
--   * 8 nodes are `frozen`: parked on purpose, not abandoned -> `paused`.
--   * 82 nodes carry tags; 13 of 18 edges are not task-to-task; 7 use
--     `supports`.

DROP VIEW IF EXISTS v_data_quality_issues;
DROP VIEW IF EXISTS v_projects_without_next_action;
DROP VIEW IF EXISTS v_blocked;
DROP VIEW IF EXISTS v_review_due;
DROP VIEW IF EXISTS v_unscheduled_important;
DROP VIEW IF EXISTS v_overloaded_days;
DROP VIEW IF EXISTS v_day_load;
DROP VIEW IF EXISTS v_day_capacity;
DROP VIEW IF EXISTS v_overdue;
DROP VIEW IF EXISTS v_next_7_days;
DROP VIEW IF EXISTS v_tomorrow;
DROP VIEW IF EXISTS v_today;
DROP VIEW IF EXISTS v_day_agenda;
DROP VIEW IF EXISTS v_task_full;

-- The outcome system gains the two fields the legacy tree fills for every
-- row: an objective's long-form vision, and a KR's weight inside it.
ALTER TABLE objectives  ADD COLUMN description TEXT;
ALTER TABLE key_results ADD COLUMN weight REAL
      CHECK (weight IS NULL OR (weight >= 0 AND weight <= 1));

-- ---------------------------------------------------------------------------
-- projects: gains nesting, a sprint mode, a window and its own measurement.
-- A sprint is a project with a parent and dates - not a separate entity, so
-- every view, filter and command that already understands projects
-- understands sprints for free.
-- ---------------------------------------------------------------------------
CREATE TABLE projects_new (
    id                  TEXT PRIMARY KEY,
    initiative_id       TEXT REFERENCES initiatives(id),
    parent_project_id   TEXT REFERENCES projects_new(id),
    kind                TEXT NOT NULL DEFAULT 'project'
                        CHECK (kind IN ('project','sprint')),
    name                TEXT NOT NULL,
    description         TEXT,
    status              TEXT NOT NULL DEFAULT 'planned'
                        CHECK (status IN ('planned','active','waiting','paused','done','cancelled','archived')),
    stage               TEXT CHECK (stage IS NULL OR stage IN ('discover','plan','build','iterate','deliver','operate','close')),
    importance          TEXT NOT NULL DEFAULT 'P2'
                        CHECK (importance IN ('P0','P1','P2','P3')),
    start_date          TEXT CHECK (start_date  IS NULL OR start_date  GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    end_date            TEXT CHECK (end_date    IS NULL OR end_date    GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    target_date         TEXT CHECK (target_date IS NULL OR target_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    hard_due_at         TEXT,
    next_review_at      TEXT CHECK (next_review_at IS NULL OR next_review_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    outcome             TEXT,
    completion_criteria TEXT,
    metric_name         TEXT,
    metric_unit         TEXT,
    target_value        REAL,
    current_value       REAL,
    legacy_ref          TEXT,
    sort_order          INTEGER NOT NULL DEFAULT 0,
    version             INTEGER NOT NULL DEFAULT 1,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    completed_at        TEXT,
    CHECK (parent_project_id IS NULL OR parent_project_id <> id),
    -- A sprint is a time box inside something; without a parent it is just a
    -- project, and calling it a sprint would be a lie.
    CHECK (kind <> 'sprint' OR parent_project_id IS NOT NULL),
    CHECK (start_date IS NULL OR end_date IS NULL OR start_date <= end_date)
);
INSERT INTO projects_new (id, initiative_id, name, description, status, stage, importance,
                          target_date, hard_due_at, next_review_at, outcome, completion_criteria,
                          legacy_ref, version, created_at, updated_at, completed_at)
     SELECT id, initiative_id, name, description, status, stage, importance,
            target_date, hard_due_at, next_review_at, outcome, completion_criteria,
            legacy_ref, version, created_at, updated_at, completed_at
       FROM projects;
DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;
CREATE INDEX ix_projects_initiative ON projects(initiative_id, status);
CREATE INDEX ix_projects_status ON projects(status, importance);
CREATE INDEX ix_projects_parent ON projects(parent_project_id) WHERE parent_project_id IS NOT NULL;
CREATE INDEX ix_projects_sprint_window ON projects(end_date, start_date)
    WHERE kind = 'sprint' AND status NOT IN ('done','cancelled','archived');

-- ---------------------------------------------------------------------------
-- milestones: the dated point a body of work is aiming at. Not a task - it is
-- not executable and consumes no capacity - and not a project - it has no
-- lifecycle of its own. It owns the tasks that reach it, and it is what a key
-- result is actually made of.
--
-- target_date is NOT NULL on purpose: a milestone without a date is a goal.
-- ---------------------------------------------------------------------------
CREATE TABLE milestones (
    id            TEXT PRIMARY KEY,
    project_id    TEXT REFERENCES projects(id),
    key_result_id TEXT REFERENCES key_results(id),
    name          TEXT NOT NULL,
    description   TEXT,
    target_date   TEXT NOT NULL
                  CHECK (target_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','at_risk','hit','missed','cancelled')),
    importance    TEXT NOT NULL DEFAULT 'P2'
                  CHECK (importance IN ('P0','P1','P2','P3')),
    metric_name   TEXT,
    metric_unit   TEXT,
    target_value  REAL,
    current_value REAL,
    note          TEXT,
    legacy_ref    TEXT,
    sort_order    INTEGER NOT NULL DEFAULT 0,
    version       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    reached_at    TEXT,
    CHECK (status <> 'hit' OR reached_at IS NOT NULL)
);
CREATE INDEX ix_milestones_date ON milestones(target_date, status);
CREATE INDEX ix_milestones_project ON milestones(project_id) WHERE project_id IS NOT NULL;
CREATE INDEX ix_milestones_kr ON milestones(key_result_id) WHERE key_result_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- tasks: gains a parked status, its own measurement, and the milestone it is
-- working towards. A task never *is* a milestone; it points at one.
-- ---------------------------------------------------------------------------
CREATE TABLE tasks_new (
    id                  TEXT PRIMARY KEY,
    project_id          TEXT REFERENCES projects(id),
    parent_task_id      TEXT REFERENCES tasks_new(id),
    milestone_id        TEXT REFERENCES milestones(id),
    title               TEXT NOT NULL,
    detail              TEXT,
    completion_criteria TEXT,
    status              TEXT NOT NULL DEFAULT 'todo'
                        CHECK (status IN ('inbox','todo','doing','waiting','paused','done','cancelled','archived')),
    importance          TEXT NOT NULL DEFAULT 'P2'
                        CHECK (importance IN ('P0','P1','P2','P3')),
    hard_due_at         TEXT,
    earliest_start_at   TEXT,
    next_review_at      TEXT CHECK (next_review_at IS NULL OR next_review_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    estimate_minutes    INTEGER CHECK (estimate_minutes IS NULL OR estimate_minutes > 0),
    metric_name         TEXT,
    metric_unit         TEXT,
    target_value        REAL,
    current_value       REAL,
    waiting_for         TEXT,
    legacy_ref          TEXT,
    legacy_due_date     TEXT,
    version             INTEGER NOT NULL DEFAULT 1,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    completed_at        TEXT,
    CHECK (parent_task_id IS NULL OR parent_task_id <> id)
);
INSERT INTO tasks_new (id, project_id, parent_task_id, title, detail, completion_criteria,
                       status, importance, hard_due_at, earliest_start_at, next_review_at,
                       estimate_minutes, waiting_for, legacy_ref, legacy_due_date,
                       version, created_at, updated_at, completed_at)
     SELECT id, project_id, parent_task_id, title, detail, completion_criteria,
            status, importance, hard_due_at, earliest_start_at, next_review_at,
            estimate_minutes, waiting_for, legacy_ref, legacy_due_date,
            version, created_at, updated_at, completed_at
       FROM tasks;
DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;
CREATE INDEX ix_tasks_project ON tasks(project_id, status);
CREATE INDEX ix_tasks_parent ON tasks(parent_task_id) WHERE parent_task_id IS NOT NULL;
CREATE INDEX ix_tasks_status ON tasks(status, importance);
CREATE INDEX ix_tasks_hard_due ON tasks(hard_due_at) WHERE hard_due_at IS NOT NULL;
CREATE INDEX ix_tasks_review ON tasks(next_review_at) WHERE next_review_at IS NOT NULL;
CREATE INDEX ix_tasks_milestone ON tasks(milestone_id, status) WHERE milestone_id IS NOT NULL;
CREATE INDEX ix_tasks_open ON tasks(importance, status)
    WHERE status NOT IN ('done','cancelled','archived');
CREATE INDEX ix_tasks_project_open ON tasks(project_id, status)
    WHERE status IN ('inbox','todo','doing','waiting','paused');
CREATE INDEX ix_tasks_review_open ON tasks(next_review_at)
    WHERE next_review_at IS NOT NULL AND status NOT IN ('done','cancelled','archived');

-- ---------------------------------------------------------------------------
-- tags: one row per (entity, tag). The legacy tree keeps them as a delimited
-- string on the node; splitting them is the whole point - a string cannot be
-- filtered on.
-- ---------------------------------------------------------------------------
CREATE TABLE tags (
    entity_type TEXT NOT NULL
                CHECK (entity_type IN ('objective','key_result','initiative','project','milestone','task')),
    entity_id   TEXT NOT NULL,
    tag         TEXT NOT NULL CHECK (tag <> '' AND tag = trim(tag)),
    created_at  TEXT NOT NULL,
    PRIMARY KEY (entity_type, entity_id, tag)
);
CREATE INDEX ix_tags_tag ON tags(tag, entity_type);

-- ---------------------------------------------------------------------------
-- dependencies: replaces the task-only table. A relation between two things
-- cannot be a column on either of them, so this stays a table - but it holds
-- edges, not elements. Both ends name their type, so an edge crosses levels:
-- 13 of the 18 legacy edges are not task-to-task, and `supports` (a weak
-- contribution edge that states influence without gating anything) is used 7
-- times. `related` is how two projects are simply associated.
-- ---------------------------------------------------------------------------
CREATE TABLE dependencies (
    id              TEXT PRIMARY KEY,
    from_type       TEXT NOT NULL
                    CHECK (from_type IN ('objective','key_result','initiative','project','milestone','task')),
    from_id         TEXT NOT NULL,
    to_type         TEXT NOT NULL
                    CHECK (to_type IN ('objective','key_result','initiative','project','milestone','task')),
    to_id           TEXT NOT NULL,
    dependency_type TEXT NOT NULL
                    CHECK (dependency_type IN ('blocks','requires','related','supports')),
    lag_days        INTEGER CHECK (lag_days IS NULL OR lag_days >= 0),
    weight          REAL CHECK (weight IS NULL OR (weight >= 0 AND weight <= 1)),
    note            TEXT,
    created_at      TEXT NOT NULL,
    CHECK (NOT (from_type = to_type AND from_id = to_id))
);
CREATE UNIQUE INDEX ux_dependencies ON dependencies(from_type, from_id, to_type, to_id, dependency_type);
CREATE INDEX ix_dependencies_to ON dependencies(to_type, to_id);

INSERT INTO dependencies (id, from_type, from_id, to_type, to_id, dependency_type, note, created_at)
     SELECT id, 'task', from_task_id, 'task', to_task_id, dependency_type, note, created_at
       FROM task_dependencies;
DROP TABLE task_dependencies;

-- ---------------------------------------------------------------------------
-- `metric_updated` joins the event vocabulary. With a measurement at every
-- level, moving one is a first-class act - 41 of the 159 legacy events are
-- exactly that, and folding them into `updated` would make the metric history
-- unreadable. Other legacy verbs map onto the canonical set with the original
-- verb preserved verbatim in `reason`.
-- ---------------------------------------------------------------------------
CREATE TABLE events_new (
    id             TEXT PRIMARY KEY,
    entity_type    TEXT NOT NULL,
    entity_id      TEXT NOT NULL,
    event_type     TEXT NOT NULL
                   CHECK (event_type IN ('created','updated','status_changed','rescheduled',
                                         'importance_changed','deadline_changed','review_set',
                                         'linked','unlinked','completed','metric_updated',
                                         'note','migrated')),
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


-- ---------------------------------------------------------------------------
-- Views rebuilt on the v3 shape. Two things changed shape here:
--   * a task reports its owning project through one more link, because the
--     project it sits in may be a sprint;
--   * the day boards carry `entity_type` / `entity_id` rather than `task_id`,
--     because a milestone lands on a day too and calling it a task would be
--     a lie.
-- ---------------------------------------------------------------------------
CREATE VIEW v_task_full AS
SELECT t.id                AS task_id,
       t.title,
       t.detail,
       t.status,
       t.importance,
       t.hard_due_at,
       date(t.hard_due_at,'localtime')  AS hard_due_date,
       t.earliest_start_at,
       t.next_review_at,
       t.estimate_minutes,
       t.waiting_for,
       t.completion_criteria,
       t.parent_task_id,
       t.metric_name,
       t.metric_unit,
       t.target_value,
       t.current_value,
       t.version,
       t.created_at,
       t.updated_at,
       t.completed_at,
       t.milestone_id,
       m.name              AS milestone_name,
       m.target_date       AS milestone_date,
       t.project_id,
       p.name              AS project_name,
       p.status            AS project_status,
       p.kind              AS project_kind,
       CASE WHEN p.kind = 'sprint' THEN p.id         END AS sprint_id,
       CASE WHEN p.kind = 'sprint' THEN p.name       END AS sprint_name,
       CASE WHEN p.kind = 'sprint' THEN p.start_date END AS sprint_start_date,
       CASE WHEN p.kind = 'sprint' THEN p.end_date   END AS sprint_end_date,
       COALESCE(pp.id, p.id)                       AS owning_project_id,
       COALESCE(pp.name, p.name)                   AS owning_project_name,
       COALESCE(p.initiative_id, pp.initiative_id) AS initiative_id,
       i.name              AS initiative_name,
       i.area_id,
       a.name              AS area_name,
       s.id                AS schedule_id,
       s.planned_date,
       s.time_slot,
       s.planned_minutes,
       COALESCE(s.planned_minutes, t.estimate_minutes) AS effective_minutes,
       (t.status NOT IN ('done','cancelled','archived')) AS is_open
  FROM tasks t
  LEFT JOIN milestones  m  ON m.id = t.milestone_id
  LEFT JOIN projects    p  ON p.id = t.project_id
  LEFT JOIN projects    pp ON pp.id = p.parent_project_id
  LEFT JOIN initiatives i  ON i.id = COALESCE(p.initiative_id, pp.initiative_id)
  LEFT JOIN areas       a  ON a.id = i.area_id
  LEFT JOIN task_schedules s ON s.task_id = t.id AND s.status = 'active';

-- One row per milestone with the work aimed at it. `open_tasks` is the honest
-- answer to "is this date going to be met".
CREATE VIEW v_milestones AS
SELECT m.id AS milestone_id, m.name, m.status, m.importance,
       m.target_date, m.reached_at, m.note,
       m.metric_name, m.metric_unit, m.target_value, m.current_value,
       m.project_id, p.name AS project_name, p.kind AS project_kind,
       COALESCE(pp.name, p.name) AS owning_project_name,
       a.name AS area_name,
       m.key_result_id, k.name AS key_result_name,
       CAST(julianday(m.target_date) - julianday((SELECT today FROM v_clock)) AS INTEGER) AS days_left,
       COUNT(t.id)                                                     AS task_count,
       SUM(CASE WHEN t.status = 'done' THEN 1 ELSE 0 END)              AS done_count,
       SUM(CASE WHEN t.status NOT IN ('done','cancelled','archived')
                THEN 1 ELSE 0 END)                                     AS open_tasks,
       SUM(CASE WHEN t.status NOT IN ('done','cancelled','archived')
                THEN COALESCE(t.estimate_minutes, 0) ELSE 0 END)       AS open_minutes
  FROM milestones m
  LEFT JOIN projects    p  ON p.id = m.project_id
  LEFT JOIN projects    pp ON pp.id = p.parent_project_id
  LEFT JOIN initiatives i  ON i.id = COALESCE(p.initiative_id, pp.initiative_id)
  LEFT JOIN areas       a  ON a.id = i.area_id
  LEFT JOIN key_results k  ON k.id = m.key_result_id
  LEFT JOIN tasks       t  ON t.milestone_id = m.id
 GROUP BY m.id, m.name, m.status, m.importance, m.target_date, m.reached_at, m.note,
          m.metric_name, m.metric_unit, m.target_value, m.current_value,
          m.project_id, p.name, p.kind, pp.name, a.name, m.key_result_id, k.name;

-- Everything landing on a day, tagged by why it is there. Tasks and
-- milestones both land; each says which it is.
CREATE VIEW v_day_agenda AS
SELECT planned_date AS date, 'scheduled' AS reason, 'task' AS entity_type,
       task_id AS entity_id, title, status, importance,
       project_id, project_name, area_name, hard_due_at, next_review_at,
       effective_minutes, schedule_id, time_slot
  FROM v_task_full
 WHERE is_open AND planned_date IS NOT NULL
UNION ALL
SELECT hard_due_date, 'hard_due', 'task', task_id, title, status, importance,
       project_id, project_name, area_name, hard_due_at, next_review_at,
       effective_minutes, schedule_id, time_slot
  FROM v_task_full
 WHERE is_open AND hard_due_date IS NOT NULL
UNION ALL
SELECT next_review_at, 'review', 'task', task_id, title, status, importance,
       project_id, project_name, area_name, hard_due_at, next_review_at,
       effective_minutes, schedule_id, time_slot
  FROM v_task_full
 WHERE is_open AND next_review_at IS NOT NULL
UNION ALL
SELECT target_date, 'milestone', 'milestone', milestone_id, name, status, importance,
       project_id, project_name, area_name, target_date, NULL,
       NULL, NULL, NULL
  FROM v_milestones
 WHERE status NOT IN ('hit','cancelled');

CREATE VIEW v_today AS
SELECT * FROM v_day_agenda WHERE date = (SELECT today FROM v_clock);

CREATE VIEW v_tomorrow AS
SELECT * FROM v_day_agenda WHERE date = (SELECT tomorrow FROM v_clock);

CREATE VIEW v_next_7_days AS
SELECT * FROM v_day_agenda
 WHERE date BETWEEN (SELECT today FROM v_clock) AND (SELECT week_end FROM v_clock);

-- Past a real date and not finished, whichever kind of thing it is.
CREATE VIEW v_overdue AS
SELECT 'task' AS entity_type, task_id AS entity_id, title, status, importance,
       project_id, project_name, area_name,
       hard_due_at AS due_at, hard_due_date AS due_date, planned_date, effective_minutes,
       CAST(julianday((SELECT today FROM v_clock)) - julianday(hard_due_date) AS INTEGER) AS days_overdue
  FROM v_task_full
 WHERE is_open AND hard_due_at IS NOT NULL
   AND hard_due_at < (SELECT now_utc FROM v_clock) || 'Z'
UNION ALL
SELECT 'milestone', milestone_id, name, status, importance,
       project_id, project_name, area_name,
       target_date, target_date, NULL, open_minutes,
       -days_left
  FROM v_milestones
 WHERE status NOT IN ('hit','cancelled') AND days_left < 0;

CREATE VIEW v_day_capacity AS
WITH days AS (
    SELECT DISTINCT planned_date AS date FROM task_schedules WHERE status = 'active'
    UNION
    SELECT date FROM daily_capacity
)
SELECT d.date,
       COALESCE(dc.available_minutes,
                CASE WHEN strftime('%w', d.date) IN ('0','6')
                     THEN (SELECT CAST(value AS INTEGER) FROM ops_settings WHERE key='default_weekend_minutes')
                     ELSE (SELECT CAST(value AS INTEGER) FROM ops_settings WHERE key='default_weekday_minutes')
                END) AS available_minutes,
       (dc.date IS NULL) AS is_default,
       dc.note
  FROM days d
  LEFT JOIN daily_capacity dc ON dc.date = d.date;

-- Only work consumes capacity. A milestone is a date, not an afternoon.
CREATE VIEW v_day_load AS
SELECT c.date,
       c.available_minutes,
       c.is_default,
       COALESCE(SUM(f.effective_minutes), 0) AS planned_minutes,
       COUNT(f.task_id)                      AS task_count,
       SUM(CASE WHEN f.effective_minutes IS NULL THEN 1 ELSE 0 END) AS tasks_without_estimate,
       COALESCE(SUM(f.effective_minutes), 0) - c.available_minutes  AS overload_minutes
  FROM v_day_capacity c
  LEFT JOIN v_task_full f ON f.planned_date = c.date AND f.is_open
 GROUP BY c.date, c.available_minutes, c.is_default;

CREATE VIEW v_overloaded_days AS
SELECT * FROM v_day_load WHERE overload_minutes > 0;

-- A paused task is parked on purpose, so it is not silently disappearing.
CREATE VIEW v_unscheduled_important AS
SELECT task_id, title, status, importance, project_id, project_name, area_name,
       estimate_minutes, next_review_at, waiting_for
  FROM v_task_full
 WHERE is_open
   AND importance IN ('P0','P1')
   AND planned_date IS NULL
   AND hard_due_at IS NULL
   AND status NOT IN ('waiting','paused');

CREATE VIEW v_review_due AS
SELECT task_id, title, status, importance, project_id, project_name, area_name,
       next_review_at, waiting_for
  FROM v_task_full
 WHERE is_open AND next_review_at IS NOT NULL
   AND next_review_at <= (SELECT today FROM v_clock);

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

CREATE VIEW v_projects_without_next_action AS
SELECT p.id AS project_id, p.name, p.kind, p.status, p.importance, p.stage,
       p.next_review_at, p.target_date, i.name AS initiative_name, a.name AS area_name
  FROM projects p
  LEFT JOIN initiatives i ON i.id = p.initiative_id
  LEFT JOIN areas       a ON a.id = i.area_id
 WHERE p.status = 'active'
   AND NOT EXISTS (
        SELECT 1 FROM tasks t
         LEFT JOIN task_schedules s ON s.task_id = t.id AND s.status = 'active'
         WHERE t.project_id = p.id
           AND (t.status IN ('todo','doing')
                OR s.planned_date >= (SELECT today FROM v_clock)))
   -- A parent whose sprints hold the work is not actionless.
   AND NOT EXISTS (SELECT 1 FROM projects c WHERE c.parent_project_id = p.id
                    AND c.status NOT IN ('done','cancelled','archived'));

-- One row per sprint: where it sits in the calendar and what is inside it.
CREATE VIEW v_sprint_progress AS
SELECT s.id AS sprint_id, s.name, s.status, s.stage, s.importance,
       s.start_date, s.end_date,
       s.metric_name, s.metric_unit, s.target_value, s.current_value,
       s.parent_project_id, pp.name AS parent_project_name, a.name AS area_name,
       CASE WHEN s.end_date IS NULL THEN NULL
            ELSE CAST(julianday(s.end_date) - julianday((SELECT today FROM v_clock)) AS INTEGER)
       END AS days_left,
       COUNT(t.id)                                        AS task_count,
       SUM(CASE WHEN t.status = 'done' THEN 1 ELSE 0 END)  AS done_count,
       SUM(CASE WHEN t.status NOT IN ('done','cancelled','archived')
                THEN 1 ELSE 0 END)                         AS open_count
  FROM projects s
  LEFT JOIN projects    pp ON pp.id = s.parent_project_id
  LEFT JOIN initiatives i  ON i.id = COALESCE(s.initiative_id, pp.initiative_id)
  LEFT JOIN areas       a  ON a.id = i.area_id
  LEFT JOIN tasks       t  ON t.project_id = s.id
 WHERE s.kind = 'sprint'
 GROUP BY s.id, s.name, s.status, s.stage, s.importance, s.start_date, s.end_date,
          s.metric_name, s.metric_unit, s.target_value, s.current_value,
          s.parent_project_id, pp.name, a.name;

-- ---------------------------------------------------------------------------
-- Metric rollup. The system states the arithmetic; it never overwrites the
-- number the user declared. `declared_value` is what was entered at this
-- level, `rolled_up_value` is what its children carrying the SAME metric_name
-- add up to, and a gap between them is a fact to look at, not an error.
-- ---------------------------------------------------------------------------
CREATE VIEW v_metric_rollup AS
SELECT 'milestone' AS entity_type, m.id AS entity_id, m.name AS title,
       m.metric_name, m.metric_unit, m.target_value,
       m.current_value AS declared_value,
       (SELECT SUM(t.current_value) FROM tasks t
         WHERE t.milestone_id = m.id AND t.metric_name = m.metric_name) AS rolled_up_value,
       (SELECT COUNT(*) FROM tasks t
         WHERE t.milestone_id = m.id AND t.metric_name = m.metric_name
           AND t.current_value IS NOT NULL) AS source_count
  FROM milestones m
 WHERE m.metric_name IS NOT NULL AND m.metric_name <> ''
UNION ALL
SELECT p.kind, p.id, p.name,
       p.metric_name, p.metric_unit, p.target_value,
       p.current_value,
       (SELECT SUM(t.current_value) FROM tasks t
         WHERE t.project_id = p.id AND t.metric_name = p.metric_name)
     + COALESCE((SELECT SUM(c.current_value) FROM projects c
                  WHERE c.parent_project_id = p.id AND c.metric_name = p.metric_name), 0),
       (SELECT COUNT(*) FROM tasks t
         WHERE t.project_id = p.id AND t.metric_name = p.metric_name
           AND t.current_value IS NOT NULL)
  FROM projects p
 WHERE p.metric_name IS NOT NULL AND p.metric_name <> ''
UNION ALL
SELECT 'key_result', k.id, k.name,
       k.metric_name, k.metric_unit, k.target_value,
       k.current_value,
       (SELECT SUM(p.current_value) FROM projects p
          JOIN project_kr_links l ON l.project_id = p.id
         WHERE l.key_result_id = k.id AND p.metric_name = k.metric_name),
       (SELECT COUNT(*) FROM projects p
          JOIN project_kr_links l ON l.project_id = p.id
         WHERE l.key_result_id = k.id AND p.metric_name = k.metric_name
           AND p.current_value IS NOT NULL)
  FROM key_results k
 WHERE k.metric_name IS NOT NULL AND k.metric_name <> '';

CREATE VIEW v_data_quality_issues AS
SELECT 'project' AS entity_type, p.id AS entity_id, p.name AS title,
       'project_missing_completion_criteria' AS issue,
       'active project has no completion criteria and no review date' AS detail
  FROM projects p
 WHERE p.status = 'active' AND p.kind = 'project'
   AND (p.completion_criteria IS NULL OR p.completion_criteria = '')
   AND p.next_review_at IS NULL
UNION ALL
SELECT 'project', project_id, name, 'project_without_next_action',
       'active project has no todo/doing task and no upcoming plan'
  FROM v_projects_without_next_action
UNION ALL
SELECT 'project', p.id, p.name, 'paused_without_review',
       'paused project has no next review date'
  FROM projects p
 WHERE p.status IN ('paused','waiting') AND p.next_review_at IS NULL
UNION ALL
SELECT 'sprint', p.id, p.name, 'sprint_without_window',
       'sprint has no end date, so it is not actually a time box'
  FROM projects p
 WHERE p.kind = 'sprint' AND p.end_date IS NULL
   AND p.status NOT IN ('done','cancelled','archived')
UNION ALL
SELECT 'sprint', p.id, p.name, 'sprint_ended_still_open',
       'sprint is past its end date but is neither done nor cancelled'
  FROM projects p
 WHERE p.kind = 'sprint' AND p.status NOT IN ('done','cancelled','archived')
   AND p.end_date IS NOT NULL AND p.end_date < (SELECT today FROM v_clock)
UNION ALL
SELECT 'milestone', milestone_id, name, 'milestone_overdue',
       'dated checkpoint is past its date and has not been marked hit or missed'
  FROM v_milestones
 WHERE status NOT IN ('hit','cancelled') AND days_left < 0
UNION ALL
-- A date with no work behind it will not arrive by itself.
SELECT 'milestone', milestone_id, name, 'milestone_without_tasks',
       'milestone has no task working towards it'
  FROM v_milestones
 WHERE status NOT IN ('hit','cancelled') AND task_count = 0
UNION ALL
SELECT 'milestone', milestone_id, name, 'milestone_due_without_open_work',
       'milestone is still ahead but every task under it is already closed'
  FROM v_milestones
 WHERE status NOT IN ('hit','cancelled') AND days_left >= 0
   AND task_count > 0 AND open_tasks = 0
UNION ALL
SELECT 'task', task_id, title, 'important_without_commitment',
       'P0/P1 task has no schedule, hard due date or review date'
  FROM v_task_full
 WHERE is_open AND importance IN ('P0','P1')
   AND status NOT IN ('waiting','paused')
   AND planned_date IS NULL AND hard_due_at IS NULL AND next_review_at IS NULL
   AND milestone_id IS NULL
UNION ALL
SELECT 'task', task_id, title, 'waiting_without_condition',
       'waiting task records neither waiting_for nor a review date'
  FROM v_task_full
 WHERE status = 'waiting'
   AND (waiting_for IS NULL OR waiting_for = '') AND next_review_at IS NULL
UNION ALL
SELECT 'task', t.id, t.title, 'paused_without_review',
       'paused task has no next review date, so nothing will bring it back'
  FROM tasks t
 WHERE t.status = 'paused' AND t.next_review_at IS NULL
UNION ALL
SELECT 'task', task_id, title, 'scheduled_without_estimate',
       'task is planned for a day but has no estimated minutes'
  FROM v_task_full
 WHERE is_open AND planned_date IS NOT NULL AND effective_minutes IS NULL
UNION ALL
SELECT 'task', t.id, t.title, 'legacy_due_date_unclassified',
       'carried-over due date has not been classified as hard due, target or plan'
  FROM tasks t
 WHERE t.legacy_due_date IS NOT NULL
   AND t.status NOT IN ('done','cancelled','archived')
UNION ALL
SELECT 'task', t.id, t.title, 'orphan_task',
       'task belongs to no project'
  FROM tasks t
 WHERE t.project_id IS NULL AND t.status NOT IN ('inbox','done','cancelled','archived');
