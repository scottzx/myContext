import type { MilestoneProgress } from "../types";

// A milestone is a date, not work, so it gets its own table: what it is, when
// it lands, and whether anything is actually pointed at it.
export function MilestoneTable({ entries }: { entries: MilestoneProgress[] }) {
  if (entries.length === 0) return <div className="empty">no milestones ahead</div>;
  return (
    <table className="rows">
      <thead>
        <tr>
          <th>imp</th>
          <th>date</th>
          <th>left</th>
          <th>name</th>
          <th>tasks</th>
          <th>project</th>
        </tr>
      </thead>
      <tbody>
        {entries.map((m) => (
          <tr key={m.milestone_id}>
            <td><span className={`badge ${m.importance}`}>{m.importance}</span></td>
            <td className="nowrap">{m.target_date}</td>
            <td className={`nowrap${m.days_left !== null && m.days_left < 0 ? " late" : ""}`}>
              {m.days_left === null ? "—" : `${m.days_left}d`}
            </td>
            <td>{m.name}</td>
            <td>
              {m.task_count === 0 ? (
                <span className="project-flag">no work behind it</span>
              ) : (
                `${m.done_count}/${m.task_count}`
              )}
            </td>
            <td>{m.project_name ?? "—"}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
