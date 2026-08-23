import type { DayLoad } from "../types";

// Renders exactly what the design insists on: facts, not a recommendation.
// The system never suggests what to cut when a day is overloaded (B+ §7.3).
export function CapacityBar({ load }: { load: DayLoad }) {
  const over = load.overload_minutes > 0;
  const pct = load.available_minutes > 0
    ? Math.min(100, (load.planned_minutes / load.available_minutes) * 100)
    : load.planned_minutes > 0 ? 100 : 0;

  return (
    <div className="capacity-bar">
      <div className="capacity-track">
        <div className={`capacity-fill${over ? " over" : ""}`} style={{ width: `${pct}%` }} />
      </div>
      <div className="capacity-numbers">
        {load.planned_minutes} / {load.available_minutes} min
        {load.is_default_capacity && <span> (default)</span>}
        {over && <span className="over"> · over by {load.overload_minutes}</span>}
      </div>
    </div>
  );
}
