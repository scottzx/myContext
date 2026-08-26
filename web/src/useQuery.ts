import { useEffect, useState } from "react";
import type { DataSource } from "./datasource";
import { DataSourceError } from "./datasource";

export interface QueryState<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
}

// A small shared fetch helper for the business-line tabs: each view queries
// its own operation independently, so one tab's failure (or an operation the
// backend hasn't shipped yet) never reaches another tab's render tree.
//
// Pass `operation: null` to skip the query entirely (used when a capability
// is not in the whitelist yet) — the view decides what to render instead.
export function useQuery<T>(ds: DataSource, operation: string | null, input?: unknown): QueryState<T> {
  const [state, setState] = useState<QueryState<T>>({ data: null, error: null, loading: !!operation });
  const inputKey = input === undefined ? "" : JSON.stringify(input);

  useEffect(() => {
    if (!operation) {
      setState({ data: null, error: null, loading: false });
      return;
    }
    let cancelled = false;
    setState({ data: null, error: null, loading: true });
    ds.query<T>(operation, input)
      .then((data) => {
        if (!cancelled) setState({ data, error: null, loading: false });
      })
      .catch((err) => {
        if (cancelled) return;
        const message = err instanceof DataSourceError ? `${err.code}: ${err.message}` : String(err);
        setState({ data: null, error: message, loading: false });
      });
    return () => {
      cancelled = true;
    };
    // input is represented by inputKey below; a fresh object literal each
    // render must not retrigger the fetch when its contents are unchanged.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ds, operation, inputKey]);

  return state;
}
