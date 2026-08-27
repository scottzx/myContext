import { useCallback, useEffect, useState } from "react";
import type { DataSource } from "../datasource";
import { DataSourceError, newWriteRequestId } from "../datasource";
import { NotAvailable } from "./NotAvailable";
import { InboxReview } from "./InboxReview";
import type { CaptureTextResult, ConfirmResult, InboxDetail, InboxPending } from "../types";

// 收件箱: paste evidence, then review what an extractor made of it.
//
// The list and the review screen are one view rather than two routes because
// the round trip is the point — capture, review, confirm, and you are looking
// at the business item. The design's success criterion is two minutes from a
// pasted transcript to a confirmable page, and a router would only add places
// to lose the item.

function CaptureBox({
  ds,
  onCaptured,
}: {
  ds: DataSource;
  onCaptured: (result: CaptureTextResult) => void;
}) {
  const [text, setText] = useState("");
  const [title, setTitle] = useState("");
  const [sourceRef, setSourceRef] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function capture() {
    if (!text.trim()) return;
    setBusy(true);
    setError(null);
    try {
      const result = await ds.write<CaptureTextResult>(
        "inbox.capture-text",
        newWriteRequestId(),
        { schema_version: 1, title, source_ref: sourceRef, text },
      );
      setText("");
      setTitle("");
      setSourceRef("");
      onCaptured(result);
    } catch (err) {
      setError(err instanceof DataSourceError ? `${err.code}: ${err.message}` : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="capture-box">
      <h2>粘贴新证据</h2>
      <textarea
        className="capture-text"
        rows={6}
        placeholder="群聊记录、会议逐字稿、客户反馈、活动推文…"
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      <div className="capture-fields">
        <input
          type="text"
          placeholder="标题（留空用第一行）"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
        />
        <input
          type="text"
          placeholder="来源链接（仅作记录，不会抓取）"
          value={sourceRef}
          onChange={(e) => setSourceRef(e.target.value)}
        />
        <button type="button" className="primary-btn" disabled={busy || !text.trim()} onClick={capture}>
          {busy ? "封存中…" : "封存证据"}
        </button>
      </div>
      {error && <div className="error-banner">{error}</div>}
      <p className="capture-note">
        原文会以不可变字节封存进 Library，之后所有事实都能指回这段原文的具体位置。
      </p>
    </section>
  );
}

export function InboxView({
  ds,
  ops,
  canWrite,
  onOpenCase,
}: {
  ds: DataSource;
  ops: Set<string>;
  canWrite: boolean;
  onOpenCase: (rootType: string, rootID: string) => void;
}) {
  const [items, setItems] = useState<InboxPending[] | null>(null);
  const [detail, setDetail] = useState<InboxDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const available = ops.has("inbox.list");

  const reload = useCallback(async () => {
    if (!available) return;
    try {
      setItems(await ds.query<InboxPending[]>("inbox.list", { limit: 50 }));
    } catch (err) {
      setError(err instanceof DataSourceError ? `${err.code}: ${err.message}` : String(err));
    }
  }, [ds, available]);

  useEffect(() => {
    void reload();
  }, [reload]);

  async function open(id: string) {
    setError(null);
    try {
      setDetail(await ds.query<InboxDetail>("inbox.get", { id }));
    } catch (err) {
      setError(err instanceof DataSourceError ? `${err.code}: ${err.message}` : String(err));
    }
  }

  function confirmed(result: ConfirmResult) {
    setDetail(null);
    void reload();
    if (result.root_id) {
      onOpenCase(result.root_type, result.root_id);
      return;
    }
    setNotice(`已写入 ${result.materializations.length} 个对象`);
  }

  if (!available) return <NotAvailable operation="inbox.list" />;

  if (detail) {
    return (
      <InboxReview
        ds={ds}
        detail={detail}
        canWrite={canWrite}
        onConfirmed={confirmed}
        onBack={() => setDetail(null)}
      />
    );
  }

  return (
    <>
      {canWrite && (
        <CaptureBox
          ds={ds}
          onCaptured={(r) => {
            void reload();
            void open(r.inbox_id);
          }}
        />
      )}
      {error && <div className="error-banner">{error}</div>}
      {notice && <div className="notice-banner">{notice}</div>}

      <section className="section">
        <h2>待处理</h2>
        {items === null && <div className="loading">读取中…</div>}
        {items !== null && items.length === 0 && (
          <div className="empty-note">收件箱是空的。</div>
        )}
        <div className="inbox-list">
          {(items ?? []).map((item) => {
            const undecided =
              item.undecided_entities +
              item.undecided_facts +
              item.undecided_relations +
              item.undecided_actions;
            return (
              <button
                type="button"
                className="inbox-item"
                key={item.inbox_id}
                onClick={() => void open(item.inbox_id)}
              >
                <span className="inbox-title">{item.title || item.inbox_id}</span>
                <span className="inbox-meta">
                  <span className={`status-chip status-${item.status}`}>{item.status}</span>
                  {undecided > 0 && <span>{undecided} 项待审核</span>}
                  {item.error_code && <span className="late">{item.error_code}</span>}
                </span>
              </button>
            );
          })}
        </div>
      </section>
    </>
  );
}
