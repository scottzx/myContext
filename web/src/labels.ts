// Display labels for the vocabularies the intake and case surfaces share.
//
// The review screen and the workspace both show `opportunity.next_step`; they
// must call it the same thing, or a user confirming 下一步 and then not finding
// it on the workspace has no way to tell whether the write failed. One map,
// two consumers.
//
// Anything unmapped falls through to its raw key on purpose: a new registry
// field should show up as itself rather than disappear.

export const ENTITY_LABEL: Record<string, string> = {
  account: "客户",
  contact: "联系人",
  opportunity: "商机",
  interaction: "交互",
  document: "文档",
  project: "项目",
  milestone: "里程碑",
  task: "任务",
  contract: "合同",
};

export const ACTION_LABEL: Record<string, string> = {
  project: "项目",
  milestone: "里程碑",
  task: "任务",
};

export const RELATION_LABEL: Record<string, string> = {
  belongs_to: "隶属于",
  primary_contact: "主要联系人",
  advances: "推进",
  about: "关于",
  documented_by: "证据文档",
  evidence_for: "作为证据",
};

export const STAGE_LABEL: Record<string, string> = {
  lead: "线索",
  qualified: "已验证",
  proposal: "方案阶段",
  negotiation: "商务谈判",
  won: "已赢单",
  lost: "已失单",
};

// Every field in the Go Field Registry, plus the draft keys the action
// registry allows. Keep this in step with internal/ops/intake_registry.go.
export const FIELD_LABEL: Record<string, string> = {
  name: "名称",
  title: "标题",
  short_name: "简称",
  industry: "行业",
  region: "地区",
  note: "备注",
  owner: "负责人",
  phone: "电话",
  email: "邮箱",
  wechat: "微信",
  deal_role: "决策角色",
  source: "来源",
  stage: "阶段",
  next_step: "下一步",
  est_amount: "预计金额",
  win_probability: "赢率",
  expected_sign_date: "预计签约",
  occurred_at: "发生时间",
  channel: "渠道",
  summary: "纪要",
  participants: "参与人",
  description: "说明",
  detail: "细节",
  completion_criteria: "完成标准",
  waiting_for: "等待",
  outcome: "结果",
  target_date: "目标日期",
  start_date: "开始日期",
  end_date: "结束日期",
  next_review_at: "下次复盘",
  hard_due_at: "硬截止",
  earliest_start_at: "最早开始",
  planned_date: "计划日期",
  planned_minutes: "计划分钟",
  estimate_minutes: "预估分钟",
  importance: "优先级",
  status: "状态",
  time_slot: "时段",
};

export function fieldLabel(key: string): string {
  return FIELD_LABEL[key] ?? key;
}

// Event titles arrive from v_case_timeline as "<entity_type> <event_type>",
// which is the database's vocabulary rather than the user's.
const EVENT_VERB: Record<string, string> = {
  created: "已创建",
  updated: "已更新",
  status_changed: "状态变化",
  stage_changed: "阶段变化",
  rescheduled: "已改期",
  importance_changed: "优先级变化",
  deadline_changed: "截止日变化",
  review_set: "设置复盘",
  linked: "已关联",
  unlinked: "已解除关联",
  completed: "已完成",
  note: "记录",
  migrated: "已迁移",
  won: "已赢单",
  lost: "已失单",
};

export function eventTitle(raw: string): string {
  const [entity, ...rest] = raw.split(" ");
  const verb = EVENT_VERB[rest.join(" ")];
  const noun = ENTITY_LABEL[entity];
  if (!noun || !verb) return raw;
  return `${noun}${verb}`;
}
