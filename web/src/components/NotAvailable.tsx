// Shown instead of erroring when a view's operation is not (yet) in
// GET /api/v1/capabilities. This is the UI half of "an entry whose operation
// is not yet available should render a clear 'not available' state" — never
// a stuck spinner, never a thrown error.
export function NotAvailable({ operation }: { operation: string }) {
  return (
    <div className="empty">
      not available in this build yet — needs <code>{operation}</code>
    </div>
  );
}
