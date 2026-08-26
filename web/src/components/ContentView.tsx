import { NotAvailable } from "./NotAvailable";

// 内容: content pieces by status board, channel list. Neither
// content_piece.list nor channel.list is part of this rollout's whitelist
// (the seven operations the backend agent is adding do not include them),
// so this tab has nothing it can call yet — it states that plainly instead
// of pretending to load.
export function ContentView() {
  return (
    <section className="section">
      <h2>content</h2>
      <NotAvailable operation="content_piece.list" />
    </section>
  );
}
