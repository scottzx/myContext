-- Deterministic views (B+ design §7.3). Every board in the UI is one of
-- these; nothing here depends on a model's judgement.
--
-- "Today" is the local calendar day. The CLI sets the process timezone from
-- the instance config before opening the database, so 'localtime' means the
-- user's declared timezone.

CREATE VIEW v_clock AS
SELECT date('now','localtime')              AS today,
       date('now','localtime','+1 day')     AS tomorrow,
       date('now','localtime','+6 day')     AS week_end,
       datetime('now')                      AS now_utc;

-- One row per task with its project chain and its single live plan.
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
       t.version,
       t.created_at,
       t.updated_at,
       t.completed_at,
       t.project_id,
       p.name              AS project_name,
       p.status            AS project_status,
       p.initiative_id,
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
  LEFT JOIN projects    p ON p.id = t.project_id
  LEFT JOIN initiatives i ON i.id = p.initiative_id
  LEFT JOIN areas       a ON a.id = i.area_id
  LEFT JOIN task_schedules s ON s.task_id = t.id AND s.status = 'active';

-- Everything landing on a given day, tagged by why it is there. A task can
-- appear more than once (planned today *and* due today) - that is the point.
CREATE VIEW v_day_agenda AS
SELECT planned_date AS date, 'scheduled' AS reason, task_id, title, status, importance,
       project_id, project_name, area_name, hard_due_at, next_review_at,
       effective_minutes, schedule_id, time_slot
  FROM v_task_full
 WHERE is_open AND planned_date IS NOT NULL
UNION ALL
SELECT hard_due_date, 'hard_due', task_id, title, status, importance,
       project_id, project_name, area_name, hard_due_at, next_review_at,
       effective_minutes, schedule_id, time_slot
  FROM v_task_full
 WHERE is_open AND hard_due_date IS NOT NULL
UNION ALL
SELECT next_review_at, 'review', task_id, title, status, importance,
       project_id, project_name, area_name, hard_due_at, next_review_at,
       effective_minutes, schedule_id, time_slot
  FROM v_task_full
 WHERE is_open AND next_review_at IS NOT NULL;

CREATE VIEW v_today AS
SELECT * FROM v_day_agenda WHERE date = (SELECT today FROM v_clock);

CREATE VIEW v_tomorrow AS
SELECT * FROM v_day_agenda WHERE date = (SELECT tomorrow FROM v_clock);

CREATE VIEW v_next_7_days AS
SELECT * FROM v_day_agenda
 WHERE date BETWEEN (SELECT today FROM v_clock) AND (SELECT week_end FROM v_clock);

-- Past a real deadline and not finished.
CREATE VIEW v_overdue AS
SELECT task_id, title, status, importance, project_id, project_name, area_name,
       hard_due_at, hard_due_date, planned_date, effective_minutes,
       CAST(julianday((SELECT today FROM v_clock)) - julianday(hard_due_date) AS INTEGER) AS days_overdue
  FROM v_task_full
 WHERE is_open AND hard_due_at IS NOT NULL
   AND hard_due_at < (SELECT now_utc FROM v_clock) || 'Z';

-- Declared capacity per day, falling back to the configured weekday/weekend
-- default. is_default tells the UI to label the number as an assumption.
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

-- Planned load per day. The system states the overload; it never picks what
-- to drop.
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

-- Important, but with no plan, no deadline and no review date: the classic
-- way a commitment silently disappears.
CREATE VIEW v_unscheduled_important AS
SELECT task_id, title, status, importance, project_id, project_name, area_name,
       estimate_minutes, next_review_at, waiting_for
  FROM v_task_full
 WHERE is_open
   AND importance IN ('P0','P1')
   AND planned_date IS NULL
   AND hard_due_at IS NULL
   AND status <> 'waiting';

CREATE VIEW v_review_due AS
SELECT task_id, title, status, importance, project_id, project_name, area_name,
       next_review_at, waiting_for
  FROM v_task_full
 WHERE is_open AND next_review_at IS NOT NULL
   AND next_review_at <= (SELECT today FROM v_clock);

CREATE VIEW v_blocked AS
SELECT f.task_id, f.title, f.status, f.importance, f.project_id, f.project_name,
       f.waiting_for, f.next_review_at,
       d.from_task_id AS blocked_by_task_id,
       b.title        AS blocked_by_title,
       b.status       AS blocked_by_status
  FROM v_task_full f
  LEFT JOIN task_dependencies d
         ON d.to_task_id = f.task_id AND d.dependency_type IN ('blocks','requires')
  LEFT JOIN tasks b
         ON b.id = d.from_task_id AND b.status NOT IN ('done','cancelled','archived')
 WHERE f.is_open AND (f.status = 'waiting' OR b.id IS NOT NULL);

-- Active projects with nothing to actually do next.
CREATE VIEW v_projects_without_next_action AS
SELECT p.id AS project_id, p.name, p.status, p.importance, p.stage,
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
                OR s.planned_date >= (SELECT today FROM v_clock)));

-- Every rule from B+ design §15 that a query can check, in one place.
CREATE VIEW v_data_quality_issues AS
SELECT 'project' AS entity_type, p.id AS entity_id, p.name AS title,
       'project_missing_completion_criteria' AS issue,
       'active project has no completion criteria and no review date' AS detail
  FROM projects p
 WHERE p.status = 'active'
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
SELECT 'task', task_id, title, 'important_without_commitment',
       'P0/P1 task has no schedule, hard due date or review date'
  FROM v_task_full
 WHERE is_open AND importance IN ('P0','P1')
   AND planned_date IS NULL AND hard_due_at IS NULL AND next_review_at IS NULL
UNION ALL
SELECT 'task', task_id, title, 'waiting_without_condition',
       'waiting task records neither waiting_for nor a review date'
  FROM v_task_full
 WHERE status = 'waiting'
   AND (waiting_for IS NULL OR waiting_for = '') AND next_review_at IS NULL
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
