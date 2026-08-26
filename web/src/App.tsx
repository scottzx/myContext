import { useEffect, useState } from "react";
import type { DataSource } from "./datasource";
import { DataSourceError } from "./datasource";
import type { Status, TreeArea } from "./types";
import { CapacityBar } from "./components/CapacityBar";
import { AgendaTable } from "./components/AgendaTable";
import { WeekGrid } from "./components/WeekGrid";
import { MilestoneTable } from "./components/MilestoneTable";
import { OverdueTable } from "./components/OverdueTable";
import { QualityList } from "./components/QualityList";
import { ProjectTree } from "./components/ProjectTree";

interface Loaded {
  status: Status;
  tree: TreeArea[];
}

export function App({ ds }: { ds: DataSource }) {
  const [state, setState] = useState<Loaded | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const [status, tree] = await Promise.all([
          ds.query<Status>("ops.status"),
          ds.query<TreeArea[]>("project.tree"),
        ]);
        if (!cancelled) setState({ status, tree });
      } catch (err) {
        if (cancelled) return;
        setError(err instanceof DataSourceError ? `${err.code}: ${err.message}` : String(err));
      }
    }
    load();
    return () => { cancelled = true; };
  }, [ds]);

  if (error) {
    return <div className="error-banner">{error}</div>;
  }
  if (!state) {
    return <div className="loading">loading…</div>;
  }

  const { status, tree } = state;

  return (
    <>
      <div className="header">
        <h1>{status.today}</h1>
        <span className="meta">projection v{status.projection_version}</span>
      </div>

      <section className="section">
        <h2>today</h2>
        <CapacityBar load={status.today_load} />
        <AgendaTable entries={status.today_agenda} emptyLabel="nothing today" />
      </section>

      <section className="section">
        <h2>tomorrow</h2>
        <AgendaTable entries={status.tomorrow_agenda} emptyLabel="nothing tomorrow" />
      </section>

      <section className="section">
        <h2>next 7 days</h2>
        <WeekGrid days={status.week} />
      </section>

      <section className="section">
        <h2>milestones through this week ({status.milestones.length})</h2>
        <MilestoneTable entries={status.milestones} />
      </section>

      <section className="section">
        <h2>overdue ({status.totals.overdue})</h2>
        <OverdueTable entries={status.overdue} />
      </section>

      <section className="section">
        <h2>due for review ({status.totals.review_due})</h2>
        <AgendaTable entries={status.review_due} emptyLabel="none" />
      </section>

      <section className="section">
        <h2>important but unscheduled ({status.totals.unscheduled_important})</h2>
        <AgendaTable entries={status.unscheduled_important} emptyLabel="none" />
      </section>

      <section className="section">
        <h2>data quality ({status.totals.quality_issues})</h2>
        <QualityList
          issues={status.quality_issues}
          truncated={status.totals.truncated}
          total={status.totals.quality_issues}
        />
      </section>

      <section className="section">
        <h2>projects</h2>
        <ProjectTree areas={tree} />
      </section>
    </>
  );
}
