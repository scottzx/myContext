import { useEffect, useState } from "react";
import type { DataSource } from "../datasource";
import { DataSourceError } from "../datasource";
import type { DocumentSearchResult } from "../types";

// Queries document.search (internal/ops/search.go). Debounced locally since
// this is the one input that fires on every keystroke; a document.search
// request can be too short for the trigram index and fall back to a scan
// (Mode reflects that — it is never hidden from the result).
export function GlobalSearch({ ds, available }: { ds: DataSource; available: boolean }) {
  const [q, setQ] = useState("");
  const [result, setResult] = useState<DocumentSearchResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!available || q.trim() === "") {
      setResult(null);
      setError(null);
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    const handle = setTimeout(() => {
      ds.query<DocumentSearchResult>("document.search", { query: q.trim() })
        .then((r) => {
          if (cancelled) return;
          setResult(r);
          setError(null);
        })
        .catch((err) => {
          if (cancelled) return;
          setError(err instanceof DataSourceError ? `${err.code}: ${err.message}` : String(err));
        })
        .finally(() => {
          if (!cancelled) setLoading(false);
        });
    }, 300);
    return () => {
      cancelled = true;
      clearTimeout(handle);
    };
  }, [ds, q, available]);

  return (
    <div className="search-box">
      <input
        type="search"
        value={q}
        disabled={!available}
        onChange={(e) => setQ(e.target.value)}
        placeholder={available ? "search documents…" : "search not available yet"}
        aria-label="search documents"
      />
      {available && q.trim() !== "" && (
        <div className="search-results" role="listbox">
          {loading && <div className="empty">searching…</div>}
          {!loading && error && <div className="error-banner">{error}</div>}
          {!loading && !error && result && result.hits.length === 0 && (
            <div className="empty">no matches</div>
          )}
          {!loading && !error && result && result.hits.length > 0 && (
            <ul>
              {result.hits.map((h) => (
                <li key={h.doc_id} className={h.is_current ? undefined : "is-superseded"}>
                  <span className="kind-tag">{h.kind}</span>
                  {h.title}
                  {/* A replaced version stays in the list — it is still
                      evidence of what was decided when — but it must never
                      look like the current answer. */}
                  {!h.is_current && <span className="search-flag">superseded</span>}
                  {h.review_due && <span className="search-flag">review due {h.review_at}</span>}
                  {h.snippet && <div className="search-snippet">{h.snippet}</div>}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
