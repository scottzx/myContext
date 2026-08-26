import { useQuery } from "../useQuery";
import type { DataSource } from "../datasource";
import type { ContractReceivable, ReceivableAging } from "../types";
import { NotAvailable } from "./NotAvailable";
import { ReceivableTable } from "./ReceivableTable";
import { ReceivableAgingTable } from "./ReceivableAgingTable";

// v_receivable_aging rows carry plan_id (one row per open instalment);
// v_contract_receivable rows never do (one row per contract). Verified
// live: `{aging: true}` is currently accepted but ignored by receivable.list
// and returns the same per-contract rows as the bare call, so this guards
// against rendering a receivableAgingRow table out of contractReceivableRow
// data instead of guessing the request ever worked.
function looksLikeAging(rows: unknown[]): rows is ReceivableAging[] {
  return rows.length === 0 || (typeof rows[0] === "object" && rows[0] !== null && "plan_id" in rows[0]);
}

// 合同回款: contract list with declared/planned/received/outstanding side by
// side, plus receivable aging. Both come from receivable.list
// (v_contract_receivable / v_receivable_aging, internal/cli/revenue_cmd.go).
export function ContractsView({ ds, ops }: { ds: DataSource; ops: Set<string> }) {
  const available = ops.has("receivable.list");
  const balances = useQuery<ContractReceivable[]>(ds, available ? "receivable.list" : null, {});
  const aging = useQuery<ReceivableAging[]>(ds, available ? "receivable.list" : null, { aging: true });
  const agingUsable = aging.data !== null && looksLikeAging(aging.data);

  if (!available) return <NotAvailable operation="receivable.list" />;

  return (
    <>
      <section className="section">
        <h2>contracts — declared vs planned vs received</h2>
        {balances.loading && <div className="loading">loading…</div>}
        {balances.error && <div className="error-banner">{balances.error}</div>}
        {balances.data && <ReceivableTable rows={balances.data} />}
      </section>

      <section className="section">
        <h2>receivable aging</h2>
        {aging.loading && <div className="loading">loading…</div>}
        {aging.error && <div className="error-banner">{aging.error}</div>}
        {aging.data && agingUsable && <ReceivableAgingTable rows={aging.data} />}
        {aging.data && !agingUsable && (
          <div className="empty">
            this build's receivable.list does not distinguish an aging view yet — showing per-contract
            balances above only
          </div>
        )}
      </section>
    </>
  );
}
