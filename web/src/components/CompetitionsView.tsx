import { useQuery } from "../useQuery";
import type { DataSource } from "../datasource";
import type { Application } from "../types";
import { NotAvailable } from "./NotAvailable";
import { fmtAmount, fmtDate } from "../format";

// 比赛: applications filtered to kind=competition — stage, decided, prize.
// No dedicated detail page, by design (the product spec calls for a filtered
// list only). Other application kinds (job, partnership, ...) have no home
// in this IA and are simply not shown here.
export function CompetitionsView({ ds, ops }: { ds: DataSource; ops: Set<string> }) {
  const available = ops.has("application.list");
  const { data, error, loading } = useQuery<Application[]>(
    ds,
    available ? "application.list" : null,
    { kind: "competition" },
  );

  if (!available) return <NotAvailable operation="application.list" />;
  if (error) return <div className="error-banner">{error}</div>;
  if (loading || !data) return <div className="loading">loading…</div>;
  if (data.length === 0) return <div className="empty">no competition applications yet</div>;

  return (
    <table className="rows">
      <thead>
        <tr><th>name</th><th>stage</th><th>decided</th><th>prize</th><th>owner</th></tr>
      </thead>
      <tbody>
        {data.map((a) => (
          <tr key={a.id}>
            <td>{a.name}</td>
            <td>
              <span
                className={`badge ${
                  a.stage === "won" ? "ok" : a.stage === "rejected" || a.stage === "withdrawn" ? "stale" : ""
                }`}
              >
                {a.stage}
              </span>
            </td>
            <td className="num">{fmtDate(a.decided_at)}</td>
            <td className="num">{a.prize_amount !== null ? fmtAmount(a.prize_amount) : "—"}</td>
            <td>{a.owner ?? "—"}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
