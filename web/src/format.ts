// Small display helpers shared by the business-line views. Kept separate
// from components so a table cell reads as `fmtAmount(x)` rather than
// re-deriving the same rounding/placeholder rules in five places.

export function fmtDate(v: string | null | undefined): string {
  if (!v) return "—";
  return v.slice(0, 10);
}

export function fmtAmount(v: number | null | undefined): string {
  if (v === null || v === undefined) return "—";
  return v.toLocaleString(undefined, { maximumFractionDigits: 2 });
}

// Whole days between an ISO timestamp and now. Used only to state a fact
// ("no update in Nd") — never to rank or recommend anything.
export function daysSince(iso: string): number {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return 0;
  return Math.floor((Date.now() - then) / 86400000);
}
