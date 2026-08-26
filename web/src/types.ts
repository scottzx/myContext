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

// --- business core (internal/ops/model.go, 005_business_core.sql) ---------
// Mirrored field-for-field from the Go structs the way the block above
// mirrors status/hierarchy. These back the six business-line tabs; a field
// the Go side does not send must not appear here.

export interface Account {
  id: string;
  name: string;
  short_name: string | null;
  account_type: string;
  industry: string | null;
  region: string | null;
  status: string;
  owner: string | null;
  note: string | null;
  legacy_ref: string | null;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface Opportunity {
  id: string;
  account_id: string;
  area_id: string | null;
  primary_contact_id: string | null;
  name: string;
  source: string | null;
  source_batch: string | null;
  stage: "lead" | "qualified" | "proposal" | "negotiation" | "won" | "lost";
  est_amount: number | null;
  win_probability: number | null;
  expected_sign_date: string | null;
  owner: string | null;
  next_step: string | null;
  lost_reason: string | null;
  closed_at: string | null;
  note: string | null;
  legacy_ref: string | null;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface Application {
  id: string;
  area_id: string | null;
  account_id: string | null;
  project_id: string | null;
  name: string;
  kind: string;
  stage:
    | "discovered"
    | "preparing"
    | "submitted"
    | "under_review"
    | "shortlisted"
    | "won"
    | "rejected"
    | "withdrawn";
  submitted_at: string | null;
  decided_at: string | null;
  prize_amount: number | null;
  outcome_note: string | null;
  reject_reason: string | null;
  owner: string | null;
  next_step: string | null;
  legacy_ref: string | null;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface Contract {
  id: string;
  account_id: string;
  opportunity_id: string | null;
  application_id: string | null;
  kind: string;
  contract_no: string | null;
  name: string;
  sign_date: string | null;
  start_date: string | null;
  end_date: string | null;
  amount: number;
  unit_price: number | null;
  quantity: number | null;
  currency: string;
  status: string;
  payment_terms: string | null;
  note: string | null;
  legacy_ref: string | null;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface ServiceTicket {
  id: string;
  account_id: string;
  contract_id: string | null;
  project_id: string | null;
  title: string;
  opened_at: string;
  kind: string;
  severity: "S1" | "S2" | "S3" | "S4";
  status: string;
  assignee: string | null;
  closed_at: string | null;
  resolution: string | null;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface Product {
  id: string;
  name: string;
  kind: string;
  status: string;
  positioning: string | null;
  repo_url: string | null;
  owner: string | null;
  current_release_id: string | null;
  launch_date: string | null;
  version: number;
  created_at: string;
  updated_at: string;
}

// v_contract_receivable (internal/cli/revenue_cmd.go contractReceivableRow).
// declared_amount is always shown next to planned/received/outstanding —
// never reconciled into one number — per the product's one hard display rule.
export interface ContractReceivable {
  contract_id: string;
  contract_no: string | null;
  name: string;
  kind: string;
  status: string;
  currency: string;
  sign_date: string | null;
  start_date: string | null;
  end_date: string | null;
  payment_terms: string | null;
  account_id: string;
  account_name: string | null;
  opportunity_id: string | null;
  application_id: string | null;
  declared_amount: number;
  planned_amount: number;
  plan_count: number;
  waived_amount: number;
  received_amount: number;
  receipt_count: number;
  last_receipt_at: string | null;
  outstanding_amount: number;
  received_ratio: number;
  plan_gap: number;
  plan_mismatch: boolean;
  unit_price: number | null;
  quantity: number | null;
  line_amount: number | null;
  line_mismatch: boolean;
  over_received: boolean;
  version: number;
  created_at: string;
  updated_at: string;
}

// v_receivable_aging / v_receivable_overdue (internal/cli/revenue_cmd.go
// receivableAgingRow): one still-open instalment.
export interface ReceivableAging {
  plan_id: string;
  contract_id: string;
  contract_no: string | null;
  contract_name: string;
  contract_kind: string;
  contract_status: string;
  currency: string;
  account_id: string;
  account_name: string | null;
  seq: number;
  due_date: string;
  planned_amount: number;
  open_amount: number;
  status: string;
  condition_note: string | null;
  days_overdue: number;
  aging_bucket: string;
}

// internal/ops/search.go DocumentHit / DocumentSearchResult.
export interface DocumentHit {
  doc_id: string;
  title: string;
  kind: string;
  rel_path?: string;
  snippet?: string;
}

export interface DocumentSearchResult {
  query: string;
  mode: "index" | "scan";
  hits: DocumentHit[];
}

// Mirrors ops.PipelineSummary (internal/ops/biz_query.go), which reads the
// v_pipeline view. The funnel is grouped by area on purpose: contract money,
// prize money and impressions are not comparable, so there is no total across
// business lines and none should be computed here either.
export interface PipelineSummary {
  area_id: string | null;
  area_name: string | null;
  stage: string;
  is_open: boolean;
  opportunity_count: number;
  est_amount_total: number;
  weighted_amount_total: number;
}
