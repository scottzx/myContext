import { useEffect, useMemo, useState } from "react";
import type { DataSource } from "./datasource";
import { DataSourceError } from "./datasource";
import type { Capabilities } from "./types";
import { Tabs, type TabDef } from "./components/Tabs";
import { GlobalSearch } from "./components/GlobalSearch";
import { TodayView } from "./components/TodayView";
import { PipelineView } from "./components/PipelineView";
import { AccountsView } from "./components/AccountsView";
import { ContractsView } from "./components/ContractsView";
import { ContentView } from "./components/ContentView";
import { ProductsView } from "./components/ProductsView";
import { CompetitionsView } from "./components/CompetitionsView";

// Seven business-line entry points (the agreed IA), organised by business
// line rather than by database table. The tab row sits directly under the
// title; there is no router — `tab` is plain useState and each view mounts
// (and fetches) only while it is the active one.
const TABS: readonly TabDef[] = [
  { id: "today", label: "今天" },
  { id: "pipeline", label: "咨询交付" },
  { id: "accounts", label: "客户" },
  { id: "contracts", label: "合同回款" },
  { id: "content", label: "内容" },
  { id: "products", label: "产品" },
  { id: "competitions", label: "比赛" },
];

export function App({ ds }: { ds: DataSource }) {
  const [tab, setTab] = useState<string>("today");
  const [caps, setCaps] = useState<Capabilities | null>(null);
  const [capsError, setCapsError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    ds.capabilities()
      .then((c) => { if (!cancelled) setCaps(c); })
      .catch((err) => {
        if (cancelled) return;
        setCapsError(err instanceof DataSourceError ? `${err.code}: ${err.message}` : String(err));
      });
    return () => { cancelled = true; };
  }, [ds]);

  // Drives which of the six new tabs can query at all — an operation not in
  // this set renders "not available" instead of calling the backend (§16.2).
  const ops = useMemo(() => new Set(caps?.operations ?? []), [caps]);

  return (
    <>
      <div className="header">
        <h1>mycontext</h1>
        <GlobalSearch ds={ds} available={ops.has("document.search")} />
      </div>

      <Tabs tabs={TABS} active={tab} onChange={setTab} />

      {capsError && <div className="error-banner">{capsError}</div>}

      {tab === "today" && <TodayView ds={ds} />}
      {tab === "pipeline" && <PipelineView ds={ds} ops={ops} />}
      {tab === "accounts" && <AccountsView ds={ds} ops={ops} />}
      {tab === "contracts" && <ContractsView ds={ds} ops={ops} />}
      {tab === "content" && <ContentView />}
      {tab === "products" && <ProductsView ds={ds} ops={ops} />}
      {tab === "competitions" && <CompetitionsView ds={ds} ops={ops} />}
    </>
  );
}
