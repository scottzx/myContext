import { useState } from "react";
import type { DataSource } from "../datasource";
import { useQuery } from "../useQuery";
import { fmtDate } from "../format";
import { NotAvailable } from "./NotAvailable";
import { CaseWorkspace } from "./CaseWorkspace";
import type { CaseIndexRow } from "../types";

// 经营事项 — the list of real business items, and the workspace for one.
//
// The list is a worklist, not a directory: v_case_index already orders it by
// what is overdue and what is due next, so this component does not sort.

export function CasesView({
  ds,
  ops,
  selected,
  onSelect,
}: {
  ds: DataSource;
  ops: Set<string>;
  selected: { rootType: string; rootID: string } | null;
  onSelect: (sel: { rootType: string; rootID: string } | null) => void;
}) {
  const [openOnly, setOpenOnly] = useState(true);
  const available = ops.has("case.list");
  const { data, error, loading } = useQuery<CaseIndexRow[]>(
    ds,
    available && !selected ? "case.list" : null,
    { open_only: openOnly, limit: 100 },
  );

  if (!available) return <NotAvailable operation="case.list" />;

  if (selected) {
    return (
      <CaseWorkspace
        ds={ds}
        rootType={selected.rootType}
        rootID={selected.rootID}
        onBack={() => onSelect(null)}
      />
    );
  }

  return (
    <section className="section">
      <div className="section-head">
        <h2>经营事项</h2>
        <label className="toggle">
          <input
            type="checkbox"
            checked={openOnly}
            onChange={(e) => setOpenOnly(e.target.checked)}
          />
          只看进行中
        </label>
      </div>

      {error && <div className="error-banner">{error}</div>}
      {loading && <div className="loading">读取中…</div>}
      {data?.length === 0 && (
        <div className="empty-note">
          还没有经营事项。先在收件箱粘贴一份证据，确认后这里就会出现。
        </div>
      )}

      <div className="case-list">
        {(data ?? []).map((c) => (
          <button
            type="button"
            className="case-card"
            key={`${c.root_type}:${c.root_id}`}
            onClick={() => onSelect({ rootType: c.root_type, rootID: c.root_id })}
          >
            <span className="case-card-title">{c.title}</span>
            <span className="case-card-sub">
              {c.counterparty_name} · {c.stage}
            </span>
            <span className="case-card-next">
              {c.next_milestone_name
                ? `下一节点 ${fmtDate(c.next_milestone_at)} ${c.next_milestone_name}`
                : "没有下一个里程碑"}
            </span>
            <span className="case-card-flags">
              {c.overdue_count > 0 && <span className="late">{c.overdue_count} 逾期</span>}
              {c.warning_count > 0 && <span className="warn-chip">{c.warning_count} 提醒</span>}
              <span>{c.open_task_count} 待办</span>
            </span>
          </button>
        ))}
      </div>
    </section>
  );
}
