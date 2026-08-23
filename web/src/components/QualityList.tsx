import type { QualityIssue } from "../types";

export function QualityList({ issues, truncated, total }: { issues: QualityIssue[]; truncated: boolean; total: number }) {
  if (issues.length === 0) return <div className="empty">none</div>;
  return (
    <>
      <table className="rows">
        <thead><tr><th>issue</th><th>title</th></tr></thead>
        <tbody>
          {issues.map((q, i) => (
            <tr key={`${q.entity_id}-${i}`}>
              <td>{q.issue}</td>
              <td>{q.title}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {truncated && <div className="truncated-note">showing {issues.length} of {total} — counts above are the real totals</div>}
    </>
  );
}
