import { useState } from "react";
import { useQuery } from "../useQuery";
import type { DataSource } from "../datasource";
import type { Account } from "../types";
import { NotAvailable } from "./NotAvailable";
import { AccountDetail } from "./AccountDetail";

// 客户: account list; clicking a row opens the Account 360 detail view.
// Master/detail lives entirely in this component's own state — no router.
export function AccountsView({ ds, ops }: { ds: DataSource; ops: Set<string> }) {
  const available = ops.has("account.list");
  const { data, error, loading } = useQuery<Account[]>(ds, available ? "account.list" : null, {});
  const [selected, setSelected] = useState<Account | null>(null);

  if (!available) return <NotAvailable operation="account.list" />;
  if (selected) {
    return <AccountDetail account={selected} ds={ds} ops={ops} onBack={() => setSelected(null)} />;
  }
  if (error) return <div className="error-banner">{error}</div>;
  if (loading || !data) return <div className="loading">loading…</div>;
  if (data.length === 0) return <div className="empty">no accounts yet</div>;

  return (
    <table className="rows">
      <thead>
        <tr><th>name</th><th>type</th><th>status</th><th>owner</th></tr>
      </thead>
      <tbody>
        {data.map((a) => (
          <tr
            key={a.id}
            className="row-link"
            tabIndex={0}
            onClick={() => setSelected(a)}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                setSelected(a);
              }
            }}
          >
            <td>{a.name}</td>
            <td>{a.account_type}</td>
            <td>{a.status}</td>
            <td>{a.owner ?? "—"}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
