import { useQuery } from "../useQuery";
import type { DataSource } from "../datasource";
import type { Product } from "../types";
import { NotAvailable } from "./NotAvailable";
import { fmtDate } from "../format";

const ACHIEVED = new Set(["released", "maintained"]);

// 产品: product cards with current release and status. current_release_id is
// shown as-is (an id, not a resolved name) — release.list is not part of
// this rollout, so there is nothing to join it against yet.
export function ProductsView({ ds, ops }: { ds: DataSource; ops: Set<string> }) {
  const available = ops.has("product.list");
  const { data, error, loading } = useQuery<Product[]>(ds, available ? "product.list" : null, {});

  if (!available) return <NotAvailable operation="product.list" />;
  if (error) return <div className="error-banner">{error}</div>;
  if (loading || !data) return <div className="loading">loading…</div>;
  if (data.length === 0) return <div className="empty">no products yet</div>;

  return (
    <div className="product-grid">
      {data.map((p) => (
        <div className="product-card" key={p.id}>
          <div className="product-card-title">{p.name}</div>
          <div className="product-card-meta">
            <span className="kind-tag">{p.kind}</span>
            <span className={`badge ${p.status === "sunset" ? "stale" : ACHIEVED.has(p.status) ? "ok" : ""}`}>
              {p.status}
            </span>
          </div>
          <div className="product-card-row">release: {p.current_release_id ?? "none yet"}</div>
          {p.launch_date && <div className="product-card-row">launched {fmtDate(p.launch_date)}</div>}
          {p.owner && <div className="product-card-row">owner: {p.owner}</div>}
        </div>
      ))}
    </div>
  );
}
