import { useMemo, useState } from "react";
import type { DataSource } from "../datasource";
import { DataSourceError, newWriteRequestId } from "../datasource";
import type {
  ActionCandidateView,
  CandidateDecision,
  CandidateKind,
  CandidateValue,
  ConfirmResult,
  EntityCandidateView,
  FactCandidateView,
  InboxDetail,
  RelationCandidateView,
} from "../types";
import { ACTION_LABEL, ENTITY_LABEL, RELATION_LABEL, fieldLabel } from "../labels";

// The review screen: 原始证据 → 候选事实/关系 → 候选动作与确认摘要.
//
// Three semantic columns on desktop, stacked in that order on a narrow screen
// (design §8). Everything on this page is a proposal; the only thing that
// writes is the confirm button at the bottom, and it refuses to submit until
// every candidate carries a verdict — because the backend refuses too, and
// finding that out after a round trip would be a worse way to learn it.

type Verdicts = Record<string, "accept" | "reject">;

function key(kind: CandidateKind, id: string): string {
  return `${kind}:${id}`;
}

// Renders a typed value the way the registry parsed it, rather than dumping
// JSON: "20000 CNY (approx)" is a different claim from "20000", and the user is
// deciding whether to believe it.
function showValue(v: CandidateValue): string {
  switch (v.type) {
    case "text":
      return v.text ?? "";
    case "number":
      return `${v.number}${v.qualifier === "approx" ? " (约)" : ""}`;
    case "date":
      return v.iso ?? "";
    case "timestamp":
      return (v.rfc3339 ?? "").replace("T", " ").slice(0, 16);
    case "boolean":
      return v.boolean ? "是" : "否";
    case "money":
      return `${(v.amount ?? 0).toLocaleString()} ${v.currency ?? "CNY"}${
        v.qualifier === "approx" ? " (约)" : ""
      }`;
    default:
      return "";
  }
}

const INTENT_LABEL: Record<string, string> = {
  create: "新建",
  update: "更新",
  link_existing: "关联已有",
};

function Verdict({
  value,
  onChange,
}: {
  value: "accept" | "reject" | undefined;
  onChange: (v: "accept" | "reject") => void;
}) {
  return (
    <span className="verdict">
      <button
        type="button"
        className={`verdict-btn${value === "accept" ? " on" : ""}`}
        aria-pressed={value === "accept"}
        onClick={() => onChange("accept")}
      >
        采纳
      </button>
      <button
        type="button"
        className={`verdict-btn${value === "reject" ? " off" : ""}`}
        aria-pressed={value === "reject"}
        onClick={() => onChange("reject")}
      >
        拒绝
      </button>
    </span>
  );
}

function Quote({ text }: { text: string }) {
  if (!text) return null;
  return <div className="candidate-quote">“{text}”</div>;
}

export function InboxReview({
  ds,
  detail,
  canWrite,
  onConfirmed,
  onBack,
}: {
  ds: DataSource;
  detail: InboxDetail;
  canWrite: boolean;
  onConfirmed: (result: ConfirmResult) => void;
  onBack: () => void;
}) {
  const [verdicts, setVerdicts] = useState<Verdicts>({});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const set = (kind: CandidateKind, id: string, v: "accept" | "reject") =>
    setVerdicts((prev) => ({ ...prev, [key(kind, id)]: v }));

  const all: Array<[CandidateKind, string]> = useMemo(
    () => [
      ...detail.entities.map((e) => ["entity", e.candidate_id] as [CandidateKind, string]),
      ...detail.facts.map((f) => ["fact", f.candidate_id] as [CandidateKind, string]),
      ...detail.relations.map((r) => ["relation", r.candidate_id] as [CandidateKind, string]),
      ...detail.actions.map((a) => ["action", a.candidate_id] as [CandidateKind, string]),
    ],
    [detail],
  );

  const undecided = all.filter(([kind, id]) => !verdicts[key(kind, id)]);
  const accepted = all.filter(([kind, id]) => verdicts[key(kind, id)] === "accept").length;

  const acceptAll = () => {
    const next: Verdicts = {};
    for (const [kind, id] of all) next[key(kind, id)] = "accept";
    setVerdicts(next);
  };

  // Which entity a fact belongs to, so the middle column groups by object
  // instead of listing thirty loose fields.
  const factsByGroup = useMemo(() => {
    const out = new Map<string, FactCandidateView[]>();
    for (const f of detail.facts) {
      const list = out.get(f.entity_group_id) ?? [];
      list.push(f);
      out.set(f.entity_group_id, list);
    }
    return out;
  }, [detail.facts]);

  async function confirm() {
    if (undecided.length > 0 || !detail.active_run_id) return;
    setBusy(true);
    setError(null);
    const decisions: CandidateDecision[] = all.map(([kind, id]) => ({
      candidate_type: kind,
      candidate_id: id,
      decision: verdicts[key(kind, id)],
    }));
    try {
      // The grant is minted from THIS click, over exactly these decisions. If
      // anything changes between here and the confirm below, the server
      // refuses rather than writing something the user did not see.
      const nonce = await ds.confirmationGrant({
        inbox_id: detail.item.id,
        active_run_id: detail.active_run_id,
        expected_version: detail.item.version,
        decisions,
      });
      const result = await ds.write<ConfirmResult>("inbox.confirm", newWriteRequestId(), {
        schema_version: 1,
        inbox_id: detail.item.id,
        expected_version: detail.item.version,
        active_run_id: detail.active_run_id,
        confirmation_nonce: nonce,
        decisions,
      });
      onConfirmed(result);
    } catch (err) {
      setError(err instanceof DataSourceError ? `${err.code}: ${err.message}` : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="review">
      <div className="review-head">
        <button type="button" className="back-btn" onClick={onBack}>
          ← 收件箱
        </button>
        <h2>{detail.item.title || detail.item.id}</h2>
        {detail.item.source_ref && <div className="review-source">{detail.item.source_ref}</div>}
      </div>

      {!detail.active_run_id && (
        <div className="empty-note">
          还没有提取结果。用 <code>mycontext inbox propose</code> 提交候选，或让 Agent 提交。
        </div>
      )}

      <div className="review-columns">
        {/* 1 — the immutable original, always readable next to the claims */}
        <section className="review-col review-evidence">
          <h3>原始证据</h3>
          <pre className="evidence-text">{detail.original_text}</pre>
        </section>

        {/* 2 — candidate facts, grouped by the object they describe */}
        <section className="review-col">
          <h3>候选事实与关系</h3>
          {detail.entities.map((e: EntityCandidateView) => (
            <div className="candidate-group" key={e.candidate_id}>
              <div className="candidate-head">
                <span className="candidate-title">
                  {INTENT_LABEL[e.intent]} {ENTITY_LABEL[e.entity_type] ?? e.entity_type}
                  {e.target_label ? ` · ${e.target_label}` : ""}
                </span>
                <Verdict
                  value={verdicts[key("entity", e.candidate_id)]}
                  onChange={(v) => set("entity", e.candidate_id, v)}
                />
              </div>
              {(factsByGroup.get(e.group_id) ?? []).map((f) => (
                <div className="candidate-row" key={f.candidate_id}>
                  <div className="candidate-main">
                    <span className="field-name">{fieldLabel(f.field_name)}</span>
                    <span className="field-value">{showValue(f.value)}</span>
                    <Quote text={f.source.quote} />
                  </div>
                  <Verdict
                    value={verdicts[key("fact", f.candidate_id)]}
                    onChange={(v) => set("fact", f.candidate_id, v)}
                  />
                </div>
              ))}
            </div>
          ))}

          {detail.relations.length > 0 && (
            <div className="candidate-group">
              <div className="candidate-head">
                <span className="candidate-title">关系</span>
              </div>
              {detail.relations.map((r: RelationCandidateView) => (
                <div className="candidate-row" key={r.candidate_id}>
                  <div className="candidate-main">
                    <span className="field-value">
                      {ENTITY_LABEL[r.from_type] ?? r.from_type}{" "}
                      {RELATION_LABEL[r.relation_type] ?? r.relation_type}{" "}
                      {ENTITY_LABEL[r.to_type] ?? r.to_type}
                    </span>
                    <Quote text={r.source.quote} />
                  </div>
                  <Verdict
                    value={verdicts[key("relation", r.candidate_id)]}
                    onChange={(v) => set("relation", r.candidate_id, v)}
                  />
                </div>
              ))}
            </div>
          )}
        </section>

        {/* 3 — proposed actions and the confirm summary */}
        <section className="review-col">
          <h3>候选动作</h3>
          {detail.actions.map((a: ActionCandidateView) => (
            <div className="candidate-row" key={a.candidate_id}>
              <div className="candidate-main">
                <span className="field-name">{ACTION_LABEL[a.action_type] ?? a.action_type}</span>
                <span className="field-value">
                  {String(a.draft.name ?? a.draft.title ?? "")}
                </span>
                <div className="candidate-meta">
                  {Object.entries(a.draft)
                    .filter(([k]) => k !== "name" && k !== "title")
                    .map(([k, v]) => `${k} ${String(v)}`)
                    .join(" · ")}
                </div>
              </div>
              <Verdict
                value={verdicts[key("action", a.candidate_id)]}
                onChange={(v) => set("action", a.candidate_id, v)}
              />
            </div>
          ))}
          {detail.actions.length === 0 && <div className="empty-note">没有提议的动作。</div>}
        </section>
      </div>

      {error && <div className="error-banner">{error}</div>}

      {/* Sticky on mobile so the CTA is never below the fold or under the
          keyboard; safe-area padding keeps it clear of the home indicator. */}
      <div className="confirm-bar">
        <div className="confirm-summary">
          {undecided.length > 0
            ? `还有 ${undecided.length} 项待决定`
            : `将写入 ${accepted} 项`}
        </div>
        <div className="confirm-actions">
          <button type="button" className="ghost-btn" onClick={acceptAll}>
            全部采纳
          </button>
          <button
            type="button"
            className="primary-btn"
            disabled={busy || undecided.length > 0 || !detail.active_run_id || !canWrite}
            onClick={confirm}
          >
            {busy ? "写入中…" : "确认写入"}
          </button>
        </div>
      </div>
      {!canWrite && (
        <div className="empty-note">
          这个实例以只读方式启动，无法确认写入。用 <code>mycontext ui serve</code>（不带
          <code>--read-only</code>）重新打开。
        </div>
      )}
    </div>
  );
}
