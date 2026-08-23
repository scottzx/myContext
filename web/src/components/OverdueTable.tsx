import type { OverdueEntry } from "../types";

export function OverdueTable({ entries }: { entries: OverdueEntry[] }) {
  if (entries.length === 0) return <div className="empty">none</div>;
  return (
    <table className="rows">
      <thead>
        <tr><th>imp</th><th>title</th><th>project</th><th>due</th><th>late</th></tr>
      </thead>
      <tbody>
        {entries.map((e) => (
          <tr key={e.task_id}>
            <td><span className={`badge ${e.importance}`}>{e.importance}</span></td>
            <td>{e.title}</td>
            <td>{e.project_name ?? "—"}</td>
            <td>{e.hard_due_at.slice(0, 10)}</td>
            <td>{e.days_overdue}d</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
