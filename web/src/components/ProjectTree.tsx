import type { ProjectSummary, TreeArea } from "../types";

// A sprint is a project with a parent, so the tree has to nest rather than
// list: rendering a sprint beside the project it lives inside would lose the
// only thing that makes it a sprint.
function ProjectBranch({
  projects,
  present,
  parentId,
}: {
  projects: ProjectSummary[];
  present: Set<string>;
  parentId: string | null;
}) {
  // A child whose parent sits in another branch would otherwise never be
  // drawn, so treat it as a root here rather than dropping it.
  const rows = projects.filter((p) => {
    const parent = p.parent_project_id && present.has(p.parent_project_id)
      ? p.parent_project_id
      : null;
    return parent === parentId;
  });
  if (rows.length === 0) return null;

  return (
    <ul>
      {rows.map((p) => (
        <li key={p.id}>
          <div className="project-row">
            <span className={`badge ${p.importance}`}>{p.importance}</span>
            <span className="project-name">{p.name}</span>
            {p.kind === "sprint" && (
              <span className="kind-tag">sprint{sprintWindow(p) ? ` ${sprintWindow(p)}` : ""}</span>
            )}
            <span>{p.open_tasks} open</span>
            {p.status === "paused" && <span className="project-flag">paused</span>}
            {p.status === "active" && p.open_tasks === 0 && (
              <span className="project-flag">no next action</span>
            )}
          </div>
          <ProjectBranch projects={projects} present={present} parentId={p.id} />
        </li>
      ))}
    </ul>
  );
}

function sprintWindow(p: ProjectSummary): string {
  if (p.start_date && p.end_date) return `${p.start_date}~${p.end_date}`;
  if (p.end_date) return `→${p.end_date}`;
  if (p.start_date) return `${p.start_date}→`;
  return "";
}

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
                  <ProjectBranch
                    projects={init.projects}
                    present={new Set(init.projects.map((p) => p.id))}
                    parentId={null}
                  />
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
    </div>
  );
}
