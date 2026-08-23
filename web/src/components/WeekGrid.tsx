import type { DayLoad } from "../types";

export function WeekGrid({ days }: { days: DayLoad[] }) {
  if (days.length === 0) return <div className="empty">no data</div>;
  return (
    <div className="week-grid">
      {days.map((d) => {
        const over = d.overload_minutes > 0;
        return (
          <div key={d.date} className={`week-day${over ? " over" : ""}`}>
            <div className="date">{d.date.slice(5)}</div>
            <div>{d.task_count} task{d.task_count === 1 ? "" : "s"}</div>
            <div className="minutes">
              {d.planned_minutes}/{d.available_minutes}
            </div>
          </div>
        );
      })}
    </div>
  );
}
