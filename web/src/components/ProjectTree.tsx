import type { TreeArea } from "../types";

export function ProjectTree({ areas }: { areas: TreeArea[] }) {
  if (areas.length === 0) return <div className="empty">no areas yet</div>;
  return (
    <div className="tree">
      <ul>
        {areas.map((a) => (
          <li key={a.area.id}>
            <div className="area-name">{a.area.name}</div>
            <ul>
              {a.initiatives.map((init) => (
                <li key={init.initiative.id}>
                  <div className="initiative-name">{init.initiative.name}</div>
                  <ul>
                    {init.projects.map((p) => (
                      <li key={p.id} className="project-row">
                        <span className={`badge ${p.importance}`}>{p.importance}</span>
                        <span className="project-name">{p.name}</span>
                        <span>{p.open_tasks} open</span>
                        {p.status === "active" && p.open_tasks === 0 && (
                          <span className="project-flag">no next action</span>
                        )}
                      </li>
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
    </div>
  );
}
