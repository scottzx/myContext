// Mirrors the Go DTOs in internal/ops/status.go and internal/ops/hierarchy.go
// field-for-field. Keep these in sync by hand until schemas/catalog.json
// grows a data-shape section (tracked as a follow-up, not attempted here —
// the catalog today only describes operations, not response shapes).

export interface AgendaEntry {
  date: string;
  reason: "scheduled" | "hard_due" | "review" | "unscheduled" | "milestone";
  entity_type: "task" | "milestone";
  entity_id: string;
  title: string;
  status: string;
  importance: "P0" | "P1" | "P2" | "P3";
  project_id: string | null;
  project_name: string | null;
  area_name: string | null;
  hard_due_at: string | null;
  next_review_at: string | null;
  effective_minutes: number | null;
  schedule_id: string | null;
  time_slot: string | null;
}

export interface DayLoad {
  date: string;
  available_minutes: number;
  is_default_capacity: boolean;
  planned_minutes: number;
  task_count: number;
  tasks_without_estimate: number;
  overload_minutes: number;
}

export interface OverdueEntry {
  entity_type: "task" | "milestone";
  entity_id: string;
  title: string;
  importance: string;
  project_name: string | null;
  due_at: string;
  days_overdue: number;
}

export interface MilestoneProgress {
  milestone_id: string;
  name: string;
  status: string;
  importance: string;
  target_date: string;
  reached_at: string | null;
  metric_name: string | null;
  metric_unit: string | null;
  target_value: number | null;
  current_value: number | null;
  project_id: string | null;
  project_name: string | null;
  area_name: string | null;
  key_result_id: string | null;
  key_result_name: string | null;
  days_left: number | null;
  task_count: number;
  done_count: number;
  open_tasks: number;
  open_minutes: number;
}

export interface QualityIssue {
  entity_type: string;
  entity_id: string;
  title: string;
  issue: string;
  detail: string;
}

export interface Totals {
  today_entries: number;
  overdue: number;
  review_due: number;
  unscheduled_important: number;
  overloaded_days: number;
  quality_issues: number;
  truncated: boolean;
}

export interface Status {
  generated_at: string;
  today: string;
  today_load: DayLoad;
  today_agenda: AgendaEntry[];
  tomorrow_agenda: AgendaEntry[];
  week: DayLoad[];
  overdue: OverdueEntry[];
  milestones: MilestoneProgress[];
  review_due: AgendaEntry[];
  unscheduled_important: AgendaEntry[];
  overloaded_days: DayLoad[];
  quality_issues: QualityIssue[];
  totals: Totals;
  projection_version: number;
}

export interface Area {
  id: string;
  name: string;
  status: string;
  sort_order: number;
  note: string | null;
}

export interface Initiative {
  id: string;
  area_id: string;
  name: string;
  status: string;
}

export interface ProjectSummary {
  id: string;
  name: string;
  kind: "project" | "sprint";
  parent_project_id: string | null;
  status: string;
  importance: "P0" | "P1" | "P2" | "P3";
  stage: string | null;
  start_date: string | null;
  end_date: string | null;
  next_review_at: string | null;
  target_date: string | null;
  initiative_name: string | null;
  area_name: string | null;
  open_tasks: number;
  next_planned_date: string | null;
}

export interface TreeInitiative {
  initiative: Initiative;
  projects: ProjectSummary[];
}

export interface TreeArea {
  area: Area;
  initiatives: TreeInitiative[];
}

// --- protocol envelope (protocol/protocol.go) --------------------------

export interface EnvelopeError {
  code: string;
  message: string;
  details?: unknown;
  retryable: boolean;
}

export interface Envelope<T> {
  protocol: string;
  ok: boolean;
  command: string;
  data?: T;
  error?: EnvelopeError;
  meta: {
    root: string;
    cli_version: string;
    duration_ms: number;
  };
}

export interface Capabilities {
  protocol: string;
  read: boolean;
  write: boolean;
  operations: string[];
  root: string;
  cli_version: string;
}
