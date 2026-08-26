package ops

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// ---------------------------------------------------------------------------
// accounts: any external party we deal with.
// ---------------------------------------------------------------------------

// CreateAccountInput is the payload of `account.create`.
type CreateAccountInput struct {
	Name        string `json:"name"`
	ShortName   string `json:"short_name,omitempty"`
	AccountType string `json:"account_type,omitempty"`
	Industry    string `json:"industry,omitempty"`
	Region      string `json:"region,omitempty"`
	Status      string `json:"status,omitempty"`
	Owner       string `json:"owner,omitempty"`
	Note        string `json:"note,omitempty"`
	LegacyRef   string `json:"legacy_ref,omitempty"`
}

func (in *CreateAccountInput) normalize() error {
	if in.Name == "" {
		return protocol.BadInput("name is required")
	}
	if in.AccountType == "" {
		in.AccountType = "prospect"
	}
	if !validAccountType[in.AccountType] {
		return protocol.BadInput("account_type %q is not valid", in.AccountType)
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if !validAccountStatus[in.Status] {
		return protocol.BadInput("status must be active|dormant|archived")
	}
	return nil
}

// CreateAccount inserts a counterparty. Names are not unique on purpose: the
// same organisation legitimately appears twice in imported batches, and
// deduplication is a judgement call, not a constraint.
func (s *Store) CreateAccount(ctx context.Context, wc WriteContext, in CreateAccountInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "account.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		id := system.NewID("acct")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO accounts (id, name, short_name, account_type, industry, region,
                                  status, owner, note, legacy_ref, version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			id, in.Name, nullString(in.ShortName), in.AccountType, nullString(in.Industry),
			nullString(in.Region), in.Status, nullString(in.Owner), nullString(in.Note),
			nullString(in.LegacyRef), ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "account", id, "created", nil, in); err != nil {
			return nil, err
		}
		acct, err := loadAccount(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: acct,
			Changes: []protocol.Change{{EntityType: "account", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"accounts"}}},
		}, nil
	})
}

// UpdateAccountInput patches an account under optimistic concurrency.
type UpdateAccountInput struct {
	AccountID       string  `json:"account_id"`
	ExpectedVersion int64   `json:"expected_version"`
	Name            *string `json:"name,omitempty"`
	ShortName       *string `json:"short_name,omitempty"`
	AccountType     *string `json:"account_type,omitempty"`
	Industry        *string `json:"industry,omitempty"`
	Region          *string `json:"region,omitempty"`
	Status          *string `json:"status,omitempty"`
	Owner           *string `json:"owner,omitempty"`
	Note            *string `json:"note,omitempty"`
}

// UpdateAccount applies a patch under optimistic concurrency control.
func (s *Store) UpdateAccount(ctx context.Context, wc WriteContext, in UpdateAccountInput) (*Result, error) {
	if in.AccountID == "" {
		return nil, protocol.BadInput("account_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the account first")
	}
	if in.AccountType != nil && !validAccountType[*in.AccountType] {
		return nil, protocol.BadInput("account_type %q is not valid", *in.AccountType)
	}
	if in.Status != nil && !validAccountStatus[*in.Status] {
		return nil, protocol.BadInput("status must be active|dormant|archived")
	}
	return s.execute(ctx, "account.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadAccount(ctx, tx, in.AccountID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("account", in.ExpectedVersion, before.Version)
		}
		set := newPatch()
		set.str("name", in.Name)
		set.str("short_name", in.ShortName)
		set.str("account_type", in.AccountType)
		set.str("industry", in.Industry)
		set.str("region", in.Region)
		set.str("status", in.Status)
		set.str("owner", in.Owner)
		set.str("note", in.Note)
		if set.empty() {
			return nil, protocol.BadInput("no fields to update")
		}
		if err := set.apply(ctx, tx, "accounts", "account", in.AccountID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		eventType := "updated"
		if in.Status != nil {
			eventType = "status_changed"
		}
		after, err := loadAccount(ctx, tx, in.AccountID)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "account", in.AccountID, eventType, before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "account", EntityID: in.AccountID,
				EventType: eventType, Version: after.Version, ProjectionKeys: []string{"accounts"}}},
		}, nil
	})
}

const accountColumns = `
    id, name, short_name, account_type, industry, region, status, owner, note,
    legacy_ref, version, created_at, updated_at`

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Name, &a.ShortName, &a.AccountType, &a.Industry, &a.Region,
		&a.Status, &a.Owner, &a.Note, &a.LegacyRef, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func loadAccount(ctx context.Context, tx *sql.Tx, id string) (*Account, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = ?`, id)
	a, err := scanAccount(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("account %s does not exist", id)
	}
	return a, err
}

// GetAccount loads one account by id.
func (s *Store) GetAccount(ctx context.Context, id string) (*Account, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = ?`, id)
	a, err := scanAccount(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("account %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return a, nil
}

// AccountFilter is the query surface of `mycontext account list`.
type AccountFilter struct {
	AccountType string
	Status      string
	Search      string
	Limit       int
}

// ListAccounts returns accounts, most recently updated first.
func (s *Store) ListAccounts(ctx context.Context, f AccountFilter) ([]*Account, error) {
	query := `SELECT ` + accountColumns + ` FROM accounts WHERE 1=1`
	var args []any
	if f.AccountType != "" {
		query += " AND account_type = ?"
		args = append(args, f.AccountType)
	}
	if f.Status != "" {
		query += " AND status = ?"
		args = append(args, f.Status)
	}
	if f.Search != "" {
		query += " AND name LIKE ? ESCAPE '\\'"
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, a)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// contacts: a person at an account.
// ---------------------------------------------------------------------------

// CreateContactInput is the payload of `contact.create`.
type CreateContactInput struct {
	AccountID string `json:"account_id"`
	Name      string `json:"name"`
	Title     string `json:"title,omitempty"`
	DealRole  string `json:"deal_role,omitempty"`
	Phone     string `json:"phone,omitempty"`
	Email     string `json:"email,omitempty"`
	Wechat    string `json:"wechat,omitempty"`
	Status    string `json:"status,omitempty"`
	Note      string `json:"note,omitempty"`
	LegacyRef string `json:"legacy_ref,omitempty"`
}

func (in *CreateContactInput) normalize() error {
	if in.AccountID == "" {
		return protocol.BadInput("account_id is required")
	}
	if in.Name == "" {
		return protocol.BadInput("name is required")
	}
	if in.DealRole != "" && !validDealRole[in.DealRole] {
		return protocol.BadInput("deal_role must be decider|influencer|user|gatekeeper")
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if !validContactStatus[in.Status] {
		return protocol.BadInput("status must be active|inactive|left|archived")
	}
	return nil
}

// CreateContact adds a person at an account. account_id is required - a
// person we know only through a company is still reached through it.
func (s *Store) CreateContact(ctx context.Context, wc WriteContext, in CreateContactInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "contact.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, "accounts", in.AccountID, "account"); err != nil {
			return nil, err
		}
		id := system.NewID("ctc")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO contacts (id, account_id, name, title, deal_role, phone, email,
                                  wechat, status, note, legacy_ref, version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			id, in.AccountID, in.Name, nullString(in.Title), nullString(in.DealRole),
			nullString(in.Phone), nullString(in.Email), nullString(in.Wechat), in.Status,
			nullString(in.Note), nullString(in.LegacyRef), ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "contact", id, "created", nil, in); err != nil {
			return nil, err
		}
		c, err := loadContact(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: c,
			Changes: []protocol.Change{{EntityType: "contact", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"contacts", "account:" + in.AccountID}}},
		}, nil
	})
}

// UpdateContactInput patches a contact under optimistic concurrency.
type UpdateContactInput struct {
	ContactID       string  `json:"contact_id"`
	ExpectedVersion int64   `json:"expected_version"`
	Name            *string `json:"name,omitempty"`
	Title           *string `json:"title,omitempty"`
	DealRole        *string `json:"deal_role,omitempty"`
	Phone           *string `json:"phone,omitempty"`
	Email           *string `json:"email,omitempty"`
	Wechat          *string `json:"wechat,omitempty"`
	Status          *string `json:"status,omitempty"`
	Note            *string `json:"note,omitempty"`
	AccountID       *string `json:"account_id,omitempty"`
}

// UpdateContact applies a patch under optimistic concurrency control.
func (s *Store) UpdateContact(ctx context.Context, wc WriteContext, in UpdateContactInput) (*Result, error) {
	if in.ContactID == "" {
		return nil, protocol.BadInput("contact_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the contact first")
	}
	if in.DealRole != nil && *in.DealRole != "" && !validDealRole[*in.DealRole] {
		return nil, protocol.BadInput("deal_role must be decider|influencer|user|gatekeeper")
	}
	if in.Status != nil && !validContactStatus[*in.Status] {
		return nil, protocol.BadInput("status must be active|inactive|left|archived")
	}
	return s.execute(ctx, "contact.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadContact(ctx, tx, in.ContactID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("contact", in.ExpectedVersion, before.Version)
		}
		if in.AccountID != nil && *in.AccountID != "" {
			if err := requireExists(ctx, tx, "accounts", *in.AccountID, "account"); err != nil {
				return nil, err
			}
		}
		set := newPatch()
		set.str("name", in.Name)
		set.str("title", in.Title)
		set.str("deal_role", in.DealRole)
		set.str("phone", in.Phone)
		set.str("email", in.Email)
		set.str("wechat", in.Wechat)
		set.str("status", in.Status)
		set.str("note", in.Note)
		set.str("account_id", in.AccountID)
		if set.empty() {
			return nil, protocol.BadInput("no fields to update")
		}
		if err := set.apply(ctx, tx, "contacts", "contact", in.ContactID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		eventType := "updated"
		if in.Status != nil {
			eventType = "status_changed"
		}
		after, err := loadContact(ctx, tx, in.ContactID)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "contact", in.ContactID, eventType, before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "contact", EntityID: in.ContactID,
				EventType: eventType, Version: after.Version, ProjectionKeys: []string{"contacts"}}},
		}, nil
	})
}

const contactColumns = `
    id, account_id, name, title, deal_role, phone, email, wechat, status, note,
    legacy_ref, version, created_at, updated_at`

func scanContact(row interface{ Scan(...any) error }) (*Contact, error) {
	var c Contact
	err := row.Scan(&c.ID, &c.AccountID, &c.Name, &c.Title, &c.DealRole, &c.Phone, &c.Email,
		&c.Wechat, &c.Status, &c.Note, &c.LegacyRef, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func loadContact(ctx context.Context, tx *sql.Tx, id string) (*Contact, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+contactColumns+` FROM contacts WHERE id = ?`, id)
	c, err := scanContact(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("contact %s does not exist", id)
	}
	return c, err
}

// GetContact loads one contact by id.
func (s *Store) GetContact(ctx context.Context, id string) (*Contact, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+contactColumns+` FROM contacts WHERE id = ?`, id)
	c, err := scanContact(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("contact %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return c, nil
}

// ContactFilter is the query surface of `mycontext contact list`.
type ContactFilter struct {
	AccountID string
	Status    string
	Search    string
	Limit     int
}

// ListContacts returns contacts, newest first.
func (s *Store) ListContacts(ctx context.Context, f ContactFilter) ([]*Contact, error) {
	query := `SELECT ` + contactColumns + ` FROM contacts WHERE 1=1`
	var args []any
	if f.AccountID != "" {
		query += " AND account_id = ?"
		args = append(args, f.AccountID)
	}
	if f.Status != "" {
		query += " AND status = ?"
		args = append(args, f.Status)
	}
	if f.Search != "" {
		query += " AND name LIKE ? ESCAPE '\\'"
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, c)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// service_tickets: what happens after delivery.
// ---------------------------------------------------------------------------

// CreateServiceTicketInput is the payload of `ticket.create`.
type CreateServiceTicketInput struct {
	AccountID  string `json:"account_id"`
	ContractID string `json:"contract_id,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
	Title      string `json:"title"`
	OpenedAt   string `json:"opened_at"`
	Kind       string `json:"kind,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Status     string `json:"status,omitempty"`
	Assignee   string `json:"assignee,omitempty"`
	ClosedAt   string `json:"closed_at,omitempty"`
	Resolution string `json:"resolution,omitempty"`
}

func (in *CreateServiceTicketInput) normalize() error {
	if in.AccountID == "" {
		return protocol.BadInput("account_id is required")
	}
	if in.Title == "" {
		return protocol.BadInput("title is required")
	}
	if in.OpenedAt == "" {
		return protocol.BadInput("opened_at is required")
	}
	if err := ValidateTimestamp("opened_at", in.OpenedAt); err != nil {
		return err
	}
	if in.Kind == "" {
		in.Kind = "question"
	}
	if !validTicketKind[in.Kind] {
		return protocol.BadInput("kind must be question|incident|change_request|training|other")
	}
	if in.Severity == "" {
		in.Severity = "S3"
	}
	if !validTicketSeverity[in.Severity] {
		return protocol.BadInput("severity must be S1|S2|S3|S4")
	}
	if in.Status == "" {
		in.Status = "open"
	}
	if !validTicketStatus[in.Status] {
		return protocol.BadInput("status is not valid")
	}
	if in.ClosedAt != "" {
		if err := ValidateTimestamp("closed_at", in.ClosedAt); err != nil {
			return err
		}
	}
	if in.Status == "closed" && in.ClosedAt == "" {
		return protocol.BadInput("status closed requires closed_at")
	}
	return nil
}

// CreateServiceTicket opens a ticket. account_id is required; contract and
// project are optional because plenty of support has neither.
func (s *Store) CreateServiceTicket(ctx context.Context, wc WriteContext, in CreateServiceTicketInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "ticket.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, "accounts", in.AccountID, "account"); err != nil {
			return nil, err
		}
		if in.ContractID != "" {
			if err := requireExists(ctx, tx, "contracts", in.ContractID, "contract"); err != nil {
				return nil, err
			}
		}
		if in.ProjectID != "" {
			if err := requireExists(ctx, tx, "projects", in.ProjectID, "project"); err != nil {
				return nil, err
			}
		}
		id := system.NewID("tkt")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO service_tickets (id, account_id, contract_id, project_id, title,
                                         opened_at, kind, severity, status, assignee,
                                         closed_at, resolution, version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			id, in.AccountID, nullString(in.ContractID), nullString(in.ProjectID), in.Title,
			in.OpenedAt, in.Kind, in.Severity, in.Status, nullString(in.Assignee),
			nullString(in.ClosedAt), nullString(in.Resolution), ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "ticket", id, "created", nil, in); err != nil {
			return nil, err
		}
		tk, err := loadServiceTicket(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: tk,
			Changes: []protocol.Change{{EntityType: "ticket", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"tickets", "account:" + in.AccountID}}},
		}, nil
	})
}

// UpdateServiceTicketInput patches a ticket under optimistic concurrency.
type UpdateServiceTicketInput struct {
	TicketID        string  `json:"ticket_id"`
	ExpectedVersion int64   `json:"expected_version"`
	Title           *string `json:"title,omitempty"`
	Kind            *string `json:"kind,omitempty"`
	Severity        *string `json:"severity,omitempty"`
	Status          *string `json:"status,omitempty"`
	Assignee        *string `json:"assignee,omitempty"`
	Resolution      *string `json:"resolution,omitempty"`
	ContractID      *string `json:"contract_id,omitempty"`
	ProjectID       *string `json:"project_id,omitempty"`
}

// UpdateServiceTicket applies a patch under optimistic concurrency control.
// Closing without a resolution is allowed - resolution is a note, not a gate.
func (s *Store) UpdateServiceTicket(ctx context.Context, wc WriteContext, in UpdateServiceTicketInput) (*Result, error) {
	if in.TicketID == "" {
		return nil, protocol.BadInput("ticket_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the ticket first")
	}
	if in.Kind != nil && !validTicketKind[*in.Kind] {
		return nil, protocol.BadInput("kind must be question|incident|change_request|training|other")
	}
	if in.Severity != nil && !validTicketSeverity[*in.Severity] {
		return nil, protocol.BadInput("severity must be S1|S2|S3|S4")
	}
	if in.Status != nil && !validTicketStatus[*in.Status] {
		return nil, protocol.BadInput("status is not valid")
	}
	return s.execute(ctx, "ticket.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadServiceTicket(ctx, tx, in.TicketID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("ticket", in.ExpectedVersion, before.Version)
		}
		if in.ContractID != nil && *in.ContractID != "" {
			if err := requireExists(ctx, tx, "contracts", *in.ContractID, "contract"); err != nil {
				return nil, err
			}
		}
		if in.ProjectID != nil && *in.ProjectID != "" {
			if err := requireExists(ctx, tx, "projects", *in.ProjectID, "project"); err != nil {
				return nil, err
			}
		}
		set := newPatch()
		set.str("title", in.Title)
		set.str("kind", in.Kind)
		set.str("severity", in.Severity)
		set.str("assignee", in.Assignee)
		set.str("resolution", in.Resolution)
		set.str("contract_id", in.ContractID)
		set.str("project_id", in.ProjectID)
		eventType := "updated"
		if in.Status != nil {
			set.raw("status", *in.Status)
			eventType = "status_changed"
			switch *in.Status {
			case "closed":
				if before.ClosedAt == nil {
					set.raw("closed_at", system.FormatTimestamp(now))
				}
			default:
				if before.ClosedAt != nil {
					set.raw("closed_at", nil)
				}
			}
		}
		if set.empty() {
			return nil, protocol.BadInput("no fields to update")
		}
		if err := set.apply(ctx, tx, "service_tickets", "ticket", in.TicketID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		after, err := loadServiceTicket(ctx, tx, in.TicketID)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "ticket", in.TicketID, eventType, before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "ticket", EntityID: in.TicketID,
				EventType: eventType, Version: after.Version, ProjectionKeys: []string{"tickets"}}},
		}, nil
	})
}

const serviceTicketColumns = `
    id, account_id, contract_id, project_id, title, opened_at, kind, severity,
    status, assignee, closed_at, resolution, version, created_at, updated_at`

func scanServiceTicket(row interface{ Scan(...any) error }) (*ServiceTicket, error) {
	var t ServiceTicket
	err := row.Scan(&t.ID, &t.AccountID, &t.ContractID, &t.ProjectID, &t.Title, &t.OpenedAt,
		&t.Kind, &t.Severity, &t.Status, &t.Assignee, &t.ClosedAt, &t.Resolution,
		&t.Version, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func loadServiceTicket(ctx context.Context, tx *sql.Tx, id string) (*ServiceTicket, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+serviceTicketColumns+` FROM service_tickets WHERE id = ?`, id)
	t, err := scanServiceTicket(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("ticket %s does not exist", id)
	}
	return t, err
}

// GetServiceTicket loads one ticket by id.
func (s *Store) GetServiceTicket(ctx context.Context, id string) (*ServiceTicket, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+serviceTicketColumns+` FROM service_tickets WHERE id = ?`, id)
	t, err := scanServiceTicket(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("ticket %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return t, nil
}

// ServiceTicketFilter is the query surface of `mycontext ticket list`.
type ServiceTicketFilter struct {
	AccountID string
	Status    string
	Severity  string
	OpenOnly  bool
	Search    string
	Limit     int
}

// ListServiceTickets returns tickets, oldest open first.
func (s *Store) ListServiceTickets(ctx context.Context, f ServiceTicketFilter) ([]*ServiceTicket, error) {
	var where []string
	var args []any
	if f.AccountID != "" {
		where = append(where, "account_id = ?")
		args = append(args, f.AccountID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Severity != "" {
		where = append(where, "severity = ?")
		args = append(args, f.Severity)
	}
	if f.OpenOnly {
		where = append(where, "status <> 'closed'")
	}
	if f.Search != "" {
		where = append(where, "title LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}

	query := `SELECT ` + serviceTicketColumns + ` FROM service_tickets`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY opened_at LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*ServiceTicket{}
	for rows.Next() {
		t, err := scanServiceTicket(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, t)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// interactions: a conversation that happened.
// ---------------------------------------------------------------------------

// LogInteractionInput is the payload of `interaction.log`.
type LogInteractionInput struct {
	SubjectType  string `json:"subject_type"`
	SubjectID    string `json:"subject_id"`
	OccurredAt   string `json:"occurred_at"`
	Channel      string `json:"channel,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Participants string `json:"participants,omitempty"`
	Owner        string `json:"owner,omitempty"`
}

func (in *LogInteractionInput) normalize() error {
	if in.SubjectType == "" || in.SubjectID == "" {
		return protocol.BadInput("subject_type and subject_id are required")
	}
	if !validEntityType[in.SubjectType] {
		return protocol.BadInput("subject_type must be one of %s", EntityTypeList())
	}
	if in.OccurredAt == "" {
		return protocol.BadInput("occurred_at is required")
	}
	if err := ValidateTimestamp("occurred_at", in.OccurredAt); err != nil {
		return err
	}
	if in.Channel == "" {
		in.Channel = "meeting"
	}
	if !validInteractionChannel[in.Channel] {
		return protocol.BadInput("channel must be meeting|call|im|email|visit")
	}
	return nil
}

// LogInteraction records that a conversation happened. The minutes themselves
// are a document linked with doc_links link_type='minutes'; this is the
// one-line record, not a substitute for the original.
func (s *Store) LogInteraction(ctx context.Context, wc WriteContext, in LogInteractionInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "interaction.log", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, entityTables[in.SubjectType], in.SubjectID, in.SubjectType); err != nil {
			return nil, err
		}
		id := system.NewID("itx")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO interactions (id, subject_type, subject_id, occurred_at, channel,
                                      summary, participants, owner, created_at)
            VALUES (?,?,?,?,?,?,?,?,?)`,
			id, in.SubjectType, in.SubjectID, in.OccurredAt, in.Channel,
			nullString(in.Summary), nullString(in.Participants), nullString(in.Owner), ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, in.SubjectType, in.SubjectID, "note", nil, in); err != nil {
			return nil, err
		}
		itx := Interaction{ID: id, SubjectType: in.SubjectType, SubjectID: in.SubjectID,
			OccurredAt: in.OccurredAt, Channel: in.Channel, CreatedAt: ts}
		assignOptional(&itx.Summary, in.Summary)
		assignOptional(&itx.Participants, in.Participants)
		assignOptional(&itx.Owner, in.Owner)
		return &Result{
			Data: itx,
			Changes: []protocol.Change{{EntityType: in.SubjectType, EntityID: in.SubjectID,
				EventType: "note", ProjectionKeys: []string{"interactions"}}},
		}, nil
	})
}

// ListInteractions returns the conversation history for one subject, newest
// first.
func (s *Store) ListInteractions(ctx context.Context, subjectType, subjectID string, limit int) ([]Interaction, error) {
	if subjectType == "" || subjectID == "" {
		return nil, protocol.BadInput("subject_type and subject_id are required")
	}
	if !validEntityType[subjectType] {
		return nil, protocol.BadInput("subject_type must be one of %s", EntityTypeList())
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT id, subject_type, subject_id, occurred_at, channel, summary, participants,
               owner, created_at
          FROM interactions WHERE subject_type = ? AND subject_id = ?
         ORDER BY occurred_at DESC LIMIT ?`, subjectType, subjectID, limit)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []Interaction{}
	for rows.Next() {
		var i Interaction
		if err := rows.Scan(&i.ID, &i.SubjectType, &i.SubjectID, &i.OccurredAt, &i.Channel,
			&i.Summary, &i.Participants, &i.Owner, &i.CreatedAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, i)
	}
	return out, sqlite.Classify(rows.Err())
}
