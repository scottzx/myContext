-- Indexes supporting the deterministic views at scale. Added after measuring
-- `ops status` against 10k tasks, not speculatively.

-- Most views filter to open tasks and then to importance; a partial index
-- keeps the scan proportional to open work rather than to all history.
CREATE INDEX ix_tasks_open ON tasks(importance, status)
    WHERE status NOT IN ('done','cancelled','archived');

-- v_projects_without_next_action and the project list both count open tasks
-- per project.
CREATE INDEX ix_tasks_project_open ON tasks(project_id, status)
    WHERE status IN ('inbox','todo','doing','waiting');

-- The day and week boards look up active plans by date.
CREATE INDEX ix_task_schedules_active_date ON task_schedules(planned_date, task_id)
    WHERE status = 'active';

-- Review sweeps scan only open tasks that actually carry a review date.
CREATE INDEX ix_tasks_review_open ON tasks(next_review_at)
    WHERE next_review_at IS NOT NULL AND status NOT IN ('done','cancelled','archived');
