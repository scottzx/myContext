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
import { InboxView } from "./components/InboxView";
import { CasesView } from "./components/CasesView";

// 收件箱 and 经营事项 lead: evidence in, business item out. The seven
// business-line tabs stay exactly as they were — the design defers replacing
// the default navigation until the new workspace has been used against real
// data (§12.9), so this adds entry points rather than removing any.
//
// There is no router; `tab` is plain useState and each view mounts (and
// fetches) only while it is the active one.
const TABS: readonly TabDef[] = [
  { id: "inbox", label: "收件箱" },
  { id: "cases", label: "经营事项" },
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
  // Which case the workspace is showing, held here so confirming an inbox item
  // can hand the user straight to it without a route.
  const [caseSel, setCaseSel] = useState<{ rootType: string; rootID: string } | null>(null);
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

      {tab === "inbox" && (
        <InboxView
          ds={ds}
          ops={ops}
          canWrite={caps?.write ?? false}
          onOpenCase={(rootType, rootID) => {
            setCaseSel({ rootType, rootID });
            setTab("cases");
          }}
        />
      )}
      {tab === "cases" && (
        <CasesView ds={ds} ops={ops} selected={caseSel} onSelect={setCaseSel} />
      )}
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
