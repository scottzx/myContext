import type { ReceivableAging } from "../types";
import { fmtAmount } from "../format";

export function ReceivableAgingTable({ rows }: { rows: ReceivableAging[] }) {
  if (rows.length === 0) return <div className="empty">no open instalments</div>;
  return (
    <table className="rows">
      <thead>
        <tr>
          <th>contract</th>
          <th>seq</th>
          <th>due</th>
          <th>open</th>
          <th>days overdue</th>
          <th>bucket</th>
          <th>status</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.plan_id}>
            <td>{r.contract_name}</td>
            <td className="num">{r.seq}</td>
            <td className="num">{r.due_date}</td>
            <td className="num">{fmtAmount(r.open_amount)} {r.currency}</td>
            <td className={`num${r.days_overdue > 0 ? " late" : ""}`}>{r.days_overdue}d</td>
            <td><span className="kind-tag">{r.aging_bucket}</span></td>
            <td>{r.status}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
