import type { AgendaEntry } from "../types";

const REASON_LABEL: Record<string, string> = {
  scheduled: "plan",
  hard_due: "due",
  review: "review",
  milestone: "checkpoint",
  unscheduled: "—",
};

// Order is exactly what the CLI already returns (planned schedule order) —
// this table never re-ranks by "importance" or applies any AI judgement
// (B+ design §10.1: "按用户设定的计划顺序显示，不做 AI 重排").
export function AgendaTable({ entries, emptyLabel }: { entries: AgendaEntry[]; emptyLabel: string }) {
  if (entries.length === 0) {
    return <div className="empty">{emptyLabel}</div>;
  }
  return (
    <table className="rows">
      <thead>
        <tr>
          <th>why</th>
          <th>imp</th>
          <th>title</th>
          <th>project</th>
          <th>min</th>
        </tr>
      </thead>
      <tbody>
        {entries.map((e) => (
          <tr key={`${e.entity_id}-${e.reason}`}>
            <td>{REASON_LABEL[e.reason] ?? e.reason}</td>
            <td><span className={`badge ${e.importance}`}>{e.importance}</span></td>
            <td>
              {e.entity_type === "milestone" && <span className="kind-tag">milestone</span>}
              {e.title}
            </td>
            <td>{e.project_name ?? "—"}</td>
            <td>{e.effective_minutes ?? "—"}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
