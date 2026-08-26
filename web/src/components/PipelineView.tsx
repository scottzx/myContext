import { useQuery } from "../useQuery";
import type { DataSource } from "../datasource";
import type { Opportunity, PipelineSummary } from "../types";
import { NotAvailable } from "./NotAvailable";
import { fmtAmount, fmtDate, daysSince } from "../format";

// 咨询交付: the funnel, the opportunity list, and stalled warnings.
//
// The funnel comes from biz.pipeline (ops.BizPipeline over the v_pipeline
// view), not from summing opportunity.list here. Two reasons, and the second
// is the important one:
//   - the weighted amount is est_amount x win_probability, and having a second
//     implementation of that in TypeScript is a drift waiting to happen; the
//     view is the authority.
//   - v_pipeline groups by area. Summing client-side flattens every business
//     line into one number, and contract money, prize money and impressions
//     are not comparable - a cross-line total is exactly what this product
//     must not display.
const STAGES = ["lead", "qualified", "proposal", "negotiation", "won", "lost"];

// A stated fact, not advice: an open deal nobody has touched in two weeks.
// No priority is implied and nothing is ranked by it beyond sorting the list.
const STALL_DAYS = 14;

// Group the flat per-(area, stage) rows into one section per business line,
// preserving the funnel order within each.
function byArea(rows: PipelineSummary[]): { area: string; stages: PipelineSummary[] }[] {
  const groups = new Map<string, PipelineSummary[]>();
  for (const r of rows) {
    const key = r.area_name ?? "未归入业务线";
    const list = groups.get(key);
    if (list) list.push(r);
    else groups.set(key, [r]);
  }
  return [...groups.entries()].map(([area, stages]) => ({
    area,
    stages: [...stages].sort((a, b) => STAGES.indexOf(a.stage) - STAGES.indexOf(b.stage)),
  }));
}

export function PipelineView({ ds, ops }: { ds: DataSource; ops: Set<string> }) {
  const available = ops.has("opportunity.list");
  const hasFunnel = ops.has("biz.pipeline");
  const { data, error, loading } = useQuery<Opportunity[]>(ds, available ? "opportunity.list" : null, {});
  const funnelQuery = useQuery<PipelineSummary[]>(ds, hasFunnel ? "biz.pipeline" : null, {});

  if (!available) return <NotAvailable operation="opportunity.list" />;
  if (error) return <div className="error-banner">{error}</div>;
  if (loading || !data) return <div className="loading">loading…</div>;

  const stalled = data
    .filter((o) => o.stage !== "won" && o.stage !== "lost")
    .map((o) => ({ ...o, days: daysSince(o.updated_at) }))
    .filter((o) => o.days >= STALL_DAYS)
    .sort((a, b) => b.days - a.days);

  return (
    <>
      <section className="section">
        <h2>pipeline by stage</h2>
        {!hasFunnel ? (
          <NotAvailable operation="biz.pipeline" />
        ) : funnelQuery.error ? (
          <div className="error-banner">{funnelQuery.error}</div>
        ) : funnelQuery.loading || !funnelQuery.data ? (
          <div className="loading">loading…</div>
        ) : funnelQuery.data.length === 0 ? (
          <div className="empty">no opportunities yet</div>
        ) : (
          byArea(funnelQuery.data).map((g) => (
            <div key={g.area} className="funnel-group">
              <h3 className="funnel-area">{g.area}</h3>
              <table className="rows">
                <thead>
                  <tr><th>stage</th><th>count</th><th>amount</th><th>weighted</th></tr>
                </thead>
                <tbody>
                  {g.stages.map((f) => (
                    <tr key={f.stage}>
                      <td>{f.stage}</td>
                      <td className="num">{f.opportunity_count}</td>
                      <td className="num">{fmtAmount(f.est_amount_total)}</td>
                      <td className="num">{fmtAmount(f.weighted_amount_total)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ))
        )}
      </section>

      <section className="section">
        <h2>stalled ({stalled.length}) — no update in {STALL_DAYS}+ days</h2>
        {stalled.length === 0 ? (
          <div className="empty">none</div>
        ) : (
          <table className="rows">
            <thead>
              <tr><th>name</th><th>stage</th><th>days since update</th><th>owner</th></tr>
            </thead>
            <tbody>
              {stalled.map((o) => (
                <tr key={o.id}>
                  <td>{o.name}</td>
                  <td>{o.stage}</td>
                  <td className="num"><span className="badge stale">{o.days}d</span></td>
                  <td>{o.owner ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="section">
        <h2>opportunities ({data.length})</h2>
        {data.length === 0 ? (
          <div className="empty">no opportunities yet</div>
        ) : (
          <table className="rows">
            <thead>
              <tr><th>name</th><th>stage</th><th>amount</th><th>win %</th><th>expected sign</th><th>owner</th></tr>
            </thead>
            <tbody>
              {data.map((o) => (
                <tr key={o.id}>
                  <td>{o.name}</td>
                  <td>{o.stage}</td>
                  <td className="num">{o.est_amount !== null ? fmtAmount(o.est_amount) : "—"}</td>
                  <td className="num">
                    {o.win_probability !== null ? `${Math.round(o.win_probability * 100)}%` : "—"}
                  </td>
                  <td className="num">{fmtDate(o.expected_sign_date)}</td>
                  <td>{o.owner ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </>
  );
}
