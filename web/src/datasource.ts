// The DataSource contract (technical design §16.1). Components only ever
// call `query`; they never know whether that call went to the localhost
// adapter, a Snapshot file, or (later) the Minis Bridge. Swapping adapters
// later means writing a new file that implements this interface — nothing
// in the components changes.
import type { Capabilities, Envelope } from "./types";

export class DataSourceError extends Error {
  code: string;
  details?: unknown;

  constructor(code: string, message: string, details?: unknown) {
    super(message);
    this.code = code;
    this.details = details;
  }
}

export interface DataSource {
  query<T>(operation: string, input?: unknown): Promise<T>;
  capabilities(): Promise<Capabilities>;
}

function newRequestId(): string {
  // Not a real ULID — this only needs to be unique per browser tab, since
  // this adapter is read-only and has no idempotent-write path to key on.
  return "req_" + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

// HttpDataSource is the localhost adapter (§16.3): same-origin POST to
// /api/v1/invoke, session token from the URL the CLI printed, carried on a
// custom header the way the design requires (no cookies, no wildcard CORS).
export class HttpDataSource implements DataSource {
  private token: string;

  constructor(token: string) {
    this.token = token;
  }

  async query<T>(operation: string, input: unknown = {}): Promise<T> {
    const res = await fetch("/api/v1/invoke", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Mycontext-Token": this.token,
      },
      body: JSON.stringify({
        protocol: "mycontext-invoke/v1",
        operation,
        request_id: newRequestId(),
        actor: "ui",
        input,
      }),
    });
    const envelope = (await res.json()) as Envelope<T>;
    if (!envelope.ok) {
      throw new DataSourceError(
        envelope.error?.code ?? "UNKNOWN",
        envelope.error?.message ?? `request failed (${res.status})`,
        envelope.error?.details,
      );
    }
    return envelope.data as T;
  }

  async capabilities(): Promise<Capabilities> {
    const res = await fetch("/api/v1/capabilities", {
      headers: { "X-Mycontext-Token": this.token },
    });
    if (!res.ok) {
      throw new DataSourceError("UNKNOWN", `capabilities request failed (${res.status})`);
    }
    return (await res.json()) as Capabilities;
  }
}

// tokenFromUrl reads the session token the CLI printed, then strips it from
// the visible URL/history so it doesn't sit in plaintext in browser history.
export function tokenFromUrl(): string {
  const url = new URL(window.location.href);
  const token = url.searchParams.get("token") ?? "";
  if (token) {
    url.searchParams.delete("token");
    window.history.replaceState({}, "", url.toString());
  }
  return token;
}
