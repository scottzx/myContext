import { useQuery } from "../useQuery";
import type { DataSource } from "../datasource";
import type { Account, Contract, ContractReceivable, Opportunity, ServiceTicket } from "../types";
import { NotAvailable } from "./NotAvailable";
import { ReceivableTable } from "./ReceivableTable";
import { fmtAmount, fmtDate } from "../format";

// The Account 360: contacts, open opportunities, contracts, receivable
// balance and tickets, each fetched and gated independently so one missing
// or failing operation never blanks the rest of the page. contact.list is
// not part of this rollout's whitelist at all (it's absent from the seven
// operations the backend agent is adding), so that section renders
// "not available" unconditionally unless it shows up in capabilities later.
export function AccountDetail({
  account,
  ds,
  ops,
  onBack,
}: {
  account: Account;
  ds: DataSource;
  ops: Set<string>;
  onBack: () => void;
}) {
  const input = { account_id: account.id };
  const canContacts = ops.has("contact.list");
  const canOpps = ops.has("opportunity.list");
  const canContracts = ops.has("contract.list");
  const canTickets = ops.has("ticket.list");
  const canReceivable = ops.has("receivable.list");

  const contacts = useQuery<unknown[]>(ds, canContacts ? "contact.list" : null, input);
  const opps = useQuery<Opportunity[]>(ds, canOpps ? "opportunity.list" : null, input);
  const contracts = useQuery<Contract[]>(ds, canContracts ? "contract.list" : null, input);
  const tickets = useQuery<ServiceTicket[]>(ds, canTickets ? "ticket.list" : null, input);
  const receivable = useQuery<ContractReceivable[]>(ds, canReceivable ? "receivable.list" : null, input);

  const openOpps = opps.data?.filter((o) => o.stage !== "won" && o.stage !== "lost") ?? null;

  return (
    <div>
      <button type="button" className="back-btn" onClick={onBack}>← accounts</button>

      <div className="header">
        <h1>{account.name}</h1>
        <span className="meta">{account.account_type} · {account.status}</span>
      </div>

      <section className="section">
        <h2>contacts</h2>
        {!canContacts && <NotAvailable operation="contact.list" />}
        {canContacts && contacts.loading && <div className="loading">loading…</div>}
        {canContacts && contacts.error && <div className="error-banner">{contacts.error}</div>}
        {canContacts && contacts.data && contacts.data.length === 0 && <div className="empty">no contacts yet</div>}
        {canContacts && contacts.data && contacts.data.length > 0 && (
          <div className="empty">{contacts.data.length} contact(s) on file</div>
        )}
      </section>

      <section className="section">
        <h2>open opportunities</h2>
        {!canOpps && <NotAvailable operation="opportunity.list" />}
        {canOpps && opps.loading && <div className="loading">loading…</div>}
        {canOpps && opps.error && <div className="error-banner">{opps.error}</div>}
        {canOpps && openOpps && openOpps.length === 0 && <div className="empty">none</div>}
        {canOpps && openOpps && openOpps.length > 0 && (
          <table className="rows">
            <thead>
              <tr><th>name</th><th>stage</th><th>amount</th><th>expected sign</th></tr>
            </thead>
            <tbody>
              {openOpps.map((o) => (
                <tr key={o.id}>
                  <td>{o.name}</td>
                  <td>{o.stage}</td>
                  <td className="num">{o.est_amount !== null ? fmtAmount(o.est_amount) : "—"}</td>
                  <td className="num">{fmtDate(o.expected_sign_date)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="section">
        <h2>contracts</h2>
        {!canContracts && <NotAvailable operation="contract.list" />}
        {canContracts && contracts.loading && <div className="loading">loading…</div>}
        {canContracts && contracts.error && <div className="error-banner">{contracts.error}</div>}
        {canContracts && contracts.data && contracts.data.length === 0 && <div className="empty">none</div>}
        {canContracts && contracts.data && contracts.data.length > 0 && (
          <table className="rows">
            <thead>
              <tr><th>name</th><th>kind</th><th>status</th><th>amount</th></tr>
            </thead>
            <tbody>
              {contracts.data.map((c) => (
                <tr key={c.id}>
                  <td>{c.name}</td>
                  <td>{c.kind}</td>
                  <td>{c.status}</td>
                  <td className="num">{fmtAmount(c.amount)} {c.currency}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="section">
        <h2>receivable balance</h2>
        {!canReceivable && <NotAvailable operation="receivable.list" />}
        {canReceivable && receivable.loading && <div className="loading">loading…</div>}
        {canReceivable && receivable.error && <div className="error-banner">{receivable.error}</div>}
        {canReceivable && receivable.data && <ReceivableTable rows={receivable.data} />}
      </section>

      <section className="section">
        <h2>tickets</h2>
        {!canTickets && <NotAvailable operation="ticket.list" />}
        {canTickets && tickets.loading && <div className="loading">loading…</div>}
        {canTickets && tickets.error && <div className="error-banner">{tickets.error}</div>}
        {canTickets && tickets.data && tickets.data.length === 0 && <div className="empty">none</div>}
        {canTickets && tickets.data && tickets.data.length > 0 && (
          <table className="rows">
            <thead>
              <tr><th>title</th><th>severity</th><th>status</th><th>opened</th></tr>
            </thead>
            <tbody>
              {tickets.data.map((t) => (
                <tr key={t.id}>
                  <td>{t.title}</td>
                  <td><span className="kind-tag">{t.severity}</span></td>
                  <td>{t.status}</td>
                  <td className="num">{fmtDate(t.opened_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}
