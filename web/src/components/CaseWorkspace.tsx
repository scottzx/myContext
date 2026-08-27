import { useState } from "react";
import type { DataSource } from "../datasource";
import { useQuery } from "../useQuery";
import { fmtDate } from "../format";
import { STAGE_LABEL, eventTitle, fieldLabel } from "../labels";
import type { CaseDetail, CaseNextActions, CaseTimeline, CaseTimelineItem } from "../types";

// 经营事项工作台 — the approved wireframe, in one page.
//
// The shape is deliberate and comes from the design: 下一推进节点 sits ABOVE the
// timeline (and on mobile, above the title), because the question a person
// opens this page with is "what do I do next", not "what happened". Everything
// below it is the evidence that the answer is right.
//
// No ordering or membership logic lives here. Which rows belong to this case
// and in what order is v_case_timeline's job; this file only decides how they
// look.

const ITEM_LABEL: Record<string, string> = {
  event: "状态变化",
  interaction: "交互",
  document: "文档",
  milestone: "里程碑",
  receipt: "回款",
  contract: "合同",
};

type Tab = "timeline" | "evidence" | "facts";

function TimelineRow({ item }: { item: CaseTimelineItem }) {
  return (
    <li className="tl-item">
      <div className="tl-when">{item.occurred_at.replace("T", " ").slice(0, 16)}</div>
      <div className="tl-dot" aria-hidden="true" />
      <div className="tl-body">
        <div className="tl-title">
          <span className="tl-kind">{ITEM_LABEL[item.item_type] ?? item.item_type}</span>
          {item.item_type === "event" ? eventTitle(item.title ?? "") : item.title}
        </div>
        {item.summary && <div className="tl-meta">{item.summary}</div>}
        {item.source_count > 0 && (
          <div className="tl-meta">{item.source_count} 处来源引用</div>
        )}
      </div>
    </li>
  );
}

export function CaseWorkspace({
  ds,
  rootType,
  rootID,
  onBack,
}: {
  ds: DataSource;
  rootType: string;
  rootID: string;
  onBack: () => void;
}) {
  const [tab, setTab] = useState<Tab>("timeline");

  const detail = useQuery<CaseDetail>(ds, "case.get", { root_type: rootType, root_id: rootID });
  const timeline = useQuery<CaseTimeline>(ds, "case.timeline", {
    root_type: rootType,
    root_id: rootID,
    limit: 50,
  });
  const next = useQuery<CaseNextActions>(ds, "case.next-actions", {
    root_type: rootType,
    root_id: rootID,
  });

  const error = detail.error ?? timeline.error ?? next.error;
  if (error) return <div className="error-banner">{error}</div>;
  if (!detail.data) return <div className="loading">读取中…</div>;

  const c = detail.data.index;
  const n = next.data;

  return (
    <div className="case-ws">
      <header className="case-top">
        <div className="case-ident">
          <button type="button" className="back-btn" onClick={onBack}>
            ← 经营事项
          </button>
          <h2 className="case-title">{c.title}</h2>
          <div className="case-chips">
            <span className="chip">商机：{STAGE_LABEL[c.stage] ?? c.stage}</span>
            <span className="chip">客户：{c.counterparty_name}</span>
            {c.primary_project_id && <span className="chip">主项目已建立</span>}
            <span className="chip">{c.importance}</span>
          </div>
        </div>

        {/* order:-1 on narrow screens — the first thing on the first screen. */}
        <aside className="case-next">
          <b>下一推进节点</b>
          {n?.next_milestone_name ? (
            <>
              <div className="case-next-main">
                {fmtDate(n.next_milestone_at)} {n.next_milestone_name}
              </div>
              <div className="case-next-meta">
                准备任务 {n.open_task_count} 项
                {n.overdue_count > 0 ? ` · ${n.overdue_count} 项逾期` : ""}
              </div>
            </>
          ) : (
            <div className="case-next-main">还没有下一个里程碑</div>
          )}
        </aside>
      </header>

      <div className="case-layout">
        <section className="panel">
          <div className="panel-tabs" role="tablist">
            {(
              [
                ["timeline", "时间线"],
                ["evidence", "证据"],
                ["facts", "确认事实"],
              ] as Array<[Tab, string]>
            ).map(([id, label]) => (
              <button
                key={id}
                type="button"
                role="tab"
                aria-selected={tab === id}
                className={`panel-tab${tab === id ? " sel" : ""}`}
                onClick={() => setTab(id)}
              >
                {label}
              </button>
            ))}
          </div>

          {tab === "timeline" && (
            <ul className="tl">
              {(timeline.data?.items ?? []).map((item) => (
                <TimelineRow key={`${item.item_type}:${item.item_id}`} item={item} />
              ))}
              {timeline.data?.items.length === 0 && (
                <li className="empty-note">还没有发生任何事。</li>
              )}
            </ul>
          )}

          {tab === "evidence" && (
            <ul className="evidence-list">
              {detail.data.evidence.map((e, i) => (
                <li key={`${e.entity_id}:${e.field_name}:${i}`}>
                  <span className={`evidence-mark${e.is_current ? " current" : ""}`}>
                    {e.is_current ? "当前" : "历史"}
                  </span>
                  <span className="field-name">{fieldLabel(e.field_name)}</span>
                  <span className="evidence-doc">{e.document_title ?? e.document_id}</span>
                </li>
              ))}
              {detail.data.evidence.length === 0 && (
                <li className="empty-note">这些字段还没有来源记录。</li>
              )}
            </ul>
          )}

          {tab === "facts" && (
            <dl className="fact-list">
              {Object.entries(detail.data.facts).map(([k, v]) => (
                <div className="fact-row" key={k}>
                  <dt>{fieldLabel(k)}</dt>
                  <dd>{k === "stage" ? (STAGE_LABEL[v] ?? v) : v}</dd>
                </div>
              ))}
            </dl>
          )}
        </section>

        <aside className="case-side">
          {/* Next actions come before the summary on narrow screens (§9). */}
          <section className="panel">
            <h3>下一步动作</h3>
            {(n?.tasks ?? []).map((t) => (
              <div className="task-row" key={t.task_id}>
                <span className="task-box" aria-hidden="true" />
                <span className="task-title">{t.title}</span>
                <b className="task-imp">{t.importance}</b>
              </div>
            ))}
            {(n?.tasks.length ?? 0) === 0 && <div className="empty-note">没有待办任务。</div>}
          </section>

          <section className="panel">
            <h3>里程碑</h3>
            {(n?.milestones ?? []).map((m) => {
              const pct = m.total_tasks === 0 ? 0 : (1 - m.open_tasks / m.total_tasks) * 100;
              return (
                <div className="mile" key={m.milestone_id}>
                  <b>
                    {m.reached_at ? "✓ " : ""}
                    {m.name}
                  </b>
                  <div className="tl-meta">
                    {m.target_date} · {m.status}
                  </div>
                  <div className="bar">
                    <i style={{ width: `${m.reached_at ? 100 : pct}%` }} />
                  </div>
                </div>
              );
            })}
            {(n?.milestones.length ?? 0) === 0 && <div className="empty-note">还没有里程碑。</div>}
          </section>

          <section className="panel">
            <h3>事项概览</h3>
            <div className="summary-grid">
              <div className="cell">
                商机金额
                <b>{detail.data.facts.est_amount ?? "待确认"}</b>
              </div>
              <div className="cell">
                最近交互
                <b>{fmtDate(c.last_interaction_at)}</b>
              </div>
              <div className="cell">
                预计签约
                <b>{fmtDate(c.next_review_at)}</b>
              </div>
              <div className="cell">
                未完成任务
                <b>{c.open_task_count}</b>
              </div>
            </div>
            {detail.data.warnings.map((w) => (
              <div className="warn" key={`${w.issue}:${w.entity_id}`}>
                {w.detail}
              </div>
            ))}
          </section>
        </aside>
      </div>
    </div>
  );
}
