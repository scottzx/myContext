import type { ContractReceivable } from "../types";
import { fmtAmount } from "../format";

// Declared amount and the planned/received/outstanding numbers always sit in
// their own columns, side by side — never merged into one "the" number. A
// mismatch is flagged, never silently resolved (the product's one hard rule).
export function ReceivableTable({ rows }: { rows: ContractReceivable[] }) {
  if (rows.length === 0) return <div className="empty">no contracts with receivable data</div>;
  return (
    <table className="rows">
      <thead>
        <tr>
          <th>contract</th>
          <th>status</th>
          <th>declared</th>
          <th>planned</th>
          <th>received</th>
          <th>outstanding</th>
          <th>flags</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.contract_id}>
            <td>{r.name}</td>
            <td>{r.status}</td>
            <td className="num">{fmtAmount(r.declared_amount)} {r.currency}</td>
            <td className="num">{fmtAmount(r.planned_amount)} {r.currency}</td>
            <td className="num">{fmtAmount(r.received_amount)} {r.currency}</td>
            <td className="num">{fmtAmount(r.outstanding_amount)} {r.currency}</td>
            <td>
              {r.plan_mismatch && <span className="badge stale">plan≠declared</span>}{" "}
              {r.line_mismatch && <span className="badge stale">unit×qty≠declared</span>}{" "}
              {r.over_received && <span className="badge stale">over-received</span>}
              {!r.plan_mismatch && !r.line_mismatch && !r.over_received && (
                <span className="badge ok">in sync</span>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
