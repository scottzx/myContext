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
// contracts: contractual income of every kind.
// ---------------------------------------------------------------------------

// CreateContractInput is the payload of `contract.create`.
type CreateContractInput struct {
	AccountID     string   `json:"account_id"`
	OpportunityID string   `json:"opportunity_id,omitempty"`
	ApplicationID string   `json:"application_id,omitempty"`
	Kind          string   `json:"kind,omitempty"`
	ContractNo    string   `json:"contract_no,omitempty"`
	Name          string   `json:"name"`
	SignDate      string   `json:"sign_date,omitempty"`
	StartDate     string   `json:"start_date,omitempty"`
	EndDate       string   `json:"end_date,omitempty"`
	Amount        float64  `json:"amount"`
	UnitPrice     *float64 `json:"unit_price,omitempty"`
	Quantity      *float64 `json:"quantity,omitempty"`
	Currency      string   `json:"currency,omitempty"`
	Status        string   `json:"status,omitempty"`
	PaymentTerms  string   `json:"payment_terms,omitempty"`
	Note          string   `json:"note,omitempty"`
	LegacyRef     string   `json:"legacy_ref,omitempty"`
}

func (in *CreateContractInput) normalize() error {
	if in.AccountID == "" {
		return protocol.BadInput("account_id is required")
	}
	if in.Name == "" {
		return protocol.BadInput("name is required")
	}
	if in.OpportunityID != "" && in.ApplicationID != "" {
		return protocol.BadInput("a contract comes from an opportunity or an application, not both")
	}
	if in.Amount <= 0 {
		return protocol.BadInput("amount must be greater than 0")
	}
	if in.UnitPrice != nil && *in.UnitPrice <= 0 {
		return protocol.BadInput("unit_price must be greater than 0")
	}
	if in.Quantity != nil && *in.Quantity <= 0 {
		return protocol.BadInput("quantity must be greater than 0")
	}
	if in.Kind == "" {
		in.Kind = "sales"
	}
	if !validContractKind[in.Kind] {
		return protocol.BadInput("kind must be sales|prize|sponsorship|grant|piecework|other")
	}
	if in.Currency == "" {
		in.Currency = "CNY"
	}
	if in.Status == "" {
		in.Status = "draft"
	}
	if !validContractStatus[in.Status] {
		return protocol.BadInput("status is not valid")
	}
	if in.Status != "draft" && in.SignDate == "" {
		return protocol.BadInput("status %q requires sign_date", in.Status)
	}
	for field, value := range map[string]string{
		"sign_date": in.SignDate, "start_date": in.StartDate, "end_date": in.EndDate,
	} {
		if value != "" {
			if err := ValidateDate(field, value); err != nil {
				return err
			}
		}
	}
	if in.StartDate != "" && in.EndDate != "" && in.StartDate > in.EndDate {
		return protocol.BadInput("start_date must not be after end_date")
	}
	return nil
}

// CreateContract records contractual income. A contract may only come from a
// WON opportunity or a WON application - SQL cannot express that cross-table
// rule, so it is enforced here.
func (s *Store) CreateContract(ctx context.Context, wc WriteContext, in CreateContractInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "contract.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, "accounts", in.AccountID, "account"); err != nil {
			return nil, err
		}
		if in.OpportunityID != "" {
			opp, err := loadOpportunity(ctx, tx, in.OpportunityID)
			if err != nil {
				return nil, err
			}
			if opp.Stage != "won" {
				return nil, protocol.BadInput("opportunity %s is %q, not won; a contract may only reference a won opportunity", in.OpportunityID, opp.Stage)
			}
		}
		if in.ApplicationID != "" {
			app, err := loadApplication(ctx, tx, in.ApplicationID)
			if err != nil {
				return nil, err
			}
			if app.Stage != "won" {
				return nil, protocol.BadInput("application %s is %q, not won; a contract may only reference a won application", in.ApplicationID, app.Stage)
			}
		}
		id := system.NewID("ctr")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO contracts (id, account_id, opportunity_id, application_id, kind,
                                   contract_no, name, sign_date, start_date, end_date, amount,
                                   unit_price, quantity, currency, status, payment_terms,
                                   note, legacy_ref, version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			id, in.AccountID, nullString(in.OpportunityID), nullString(in.ApplicationID),
			in.Kind, nullString(in.ContractNo), in.Name, nullString(in.SignDate),
			nullString(in.StartDate), nullString(in.EndDate), in.Amount, nullFloat(in.UnitPrice),
			nullFloat(in.Quantity), in.Currency, in.Status, nullString(in.PaymentTerms),
			nullString(in.Note), nullString(in.LegacyRef), ts, ts); err != nil {
			return nil, err
		}
		eventType := "created"
		if in.Status == "signed" {
			eventType = "signed"
		}
		if err := recordEvent(ctx, tx, wc, now, "contract", id, eventType, nil, in); err != nil {
			return nil, err
		}
		c, err := loadContract(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: c,
			Changes: []protocol.Change{{EntityType: "contract", EntityID: id, EventType: eventType,
				Version: 1, ProjectionKeys: []string{"contracts", "account:" + in.AccountID}}},
		}, nil
	})
}

// UpdateContractInput patches a contract under optimistic concurrency.
type UpdateContractInput struct {
	ContractID      string   `json:"contract_id"`
	ExpectedVersion int64    `json:"expected_version"`
	AccountID       *string  `json:"account_id,omitempty"`
	Name            *string  `json:"name,omitempty"`
	ContractNo      *string  `json:"contract_no,omitempty"`
	Kind            *string  `json:"kind,omitempty"`
	SignDate        *string  `json:"sign_date,omitempty"`
	StartDate       *string  `json:"start_date,omitempty"`
	EndDate         *string  `json:"end_date,omitempty"`
	Amount          *float64 `json:"amount,omitempty"`
	UnitPrice       *float64 `json:"unit_price,omitempty"`
	Quantity        *float64 `json:"quantity,omitempty"`
	Currency        *string  `json:"currency,omitempty"`
	Status          *string  `json:"status,omitempty"`
	PaymentTerms    *string  `json:"payment_terms,omitempty"`
	Note            *string  `json:"note,omitempty"`
}

// UpdateContract applies a patch under optimistic concurrency control. It
// never touches unit_price/quantity's relationship to amount, or amount's
// relationship to the receivable plans - those disagreements are facts for
// v_biz_quality_issues to state, not for this code to prevent.
func (s *Store) UpdateContract(ctx context.Context, wc WriteContext, in UpdateContractInput) (*Result, error) {
	if in.ContractID == "" {
		return nil, protocol.BadInput("contract_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the contract first")
	}
	if in.Kind != nil && !validContractKind[*in.Kind] {
		return nil, protocol.BadInput("kind must be sales|prize|sponsorship|grant|piecework|other")
	}
	if in.Status != nil && !validContractStatus[*in.Status] {
		return nil, protocol.BadInput("status is not valid")
	}
	if in.Amount != nil && *in.Amount <= 0 {
		return nil, protocol.BadInput("amount must be greater than 0")
	}
	if in.UnitPrice != nil && *in.UnitPrice <= 0 {
		return nil, protocol.BadInput("unit_price must be greater than 0")
	}
	if in.Quantity != nil && *in.Quantity <= 0 {
		return nil, protocol.BadInput("quantity must be greater than 0")
	}
	for field, value := range map[string]*string{
		"sign_date": in.SignDate, "start_date": in.StartDate, "end_date": in.EndDate,
	} {
		if value != nil && *value != "" {
			if err := ValidateDate(field, *value); err != nil {
				return nil, err
			}
		}
	}
	return s.execute(ctx, "contract.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadContract(ctx, tx, in.ContractID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("contract", in.ExpectedVersion, before.Version)
		}
		// A signed contract may not change its account_id: once executed, who
		// it is with is a fact of history, not a field to correct.
		if in.AccountID != nil && *in.AccountID != "" && *in.AccountID != before.AccountID && before.Status != "draft" {
			return nil, protocol.BadInput("a signed contract may not change its account_id")
		}

		set := newPatch()
		set.str("account_id", in.AccountID)
		set.str("name", in.Name)
		set.str("contract_no", in.ContractNo)
		set.str("kind", in.Kind)
		set.str("sign_date", in.SignDate)
		set.str("start_date", in.StartDate)
		set.str("end_date", in.EndDate)
		set.str("currency", in.Currency)
		set.str("payment_terms", in.PaymentTerms)
		set.str("note", in.Note)
		set.flt("unit_price", in.UnitPrice)
		set.flt("quantity", in.Quantity)
		if in.Amount != nil {
			set.flt("amount", in.Amount)
		}

		var eventTypes []string
		if in.Status != nil {
			signDate := before.SignDate
			if in.SignDate != nil {
				signDate = in.SignDate
			}
			if *in.Status != "draft" && (signDate == nil || *signDate == "") {
				return nil, protocol.BadInput("status %q requires sign_date", *in.Status)
			}
			set.raw("status", *in.Status)
			if *in.Status == "signed" {
				eventTypes = append(eventTypes, "signed")
			} else {
				eventTypes = append(eventTypes, "status_changed")
			}
		}
		if in.Amount != nil {
			eventTypes = append(eventTypes, "amount_set")
		}
		if len(eventTypes) == 0 {
			eventTypes = append(eventTypes, "updated")
		}
		if set.empty() {
			return nil, protocol.BadInput("no fields to update")
		}
		if err := set.apply(ctx, tx, "contracts", "contract", in.ContractID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		after, err := loadContract(ctx, tx, in.ContractID)
		if err != nil {
			return nil, err
		}
		for _, eventType := range eventTypes {
			if err := recordEvent(ctx, tx, wc, now, "contract", in.ContractID, eventType, before, after); err != nil {
				return nil, err
			}
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "contract", EntityID: in.ContractID,
				EventType: eventTypes[len(eventTypes)-1], Version: after.Version,
				ProjectionKeys: []string{"contracts"}}},
		}, nil
	})
}

const contractColumns = `
    id, account_id, opportunity_id, application_id, kind, contract_no, name,
    sign_date, start_date, end_date, amount, unit_price, quantity, currency,
    status, payment_terms, note, legacy_ref, version, created_at, updated_at`

func scanContract(row interface{ Scan(...any) error }) (*Contract, error) {
	var c Contract
	err := row.Scan(&c.ID, &c.AccountID, &c.OpportunityID, &c.ApplicationID, &c.Kind,
		&c.ContractNo, &c.Name, &c.SignDate, &c.StartDate, &c.EndDate, &c.Amount,
		&c.UnitPrice, &c.Quantity, &c.Currency, &c.Status, &c.PaymentTerms, &c.Note,
		&c.LegacyRef, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func loadContract(ctx context.Context, tx *sql.Tx, id string) (*Contract, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+contractColumns+` FROM contracts WHERE id = ?`, id)
	c, err := scanContract(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("contract %s does not exist", id)
	}
	return c, err
}

// GetContract loads one contract by id.
func (s *Store) GetContract(ctx context.Context, id string) (*Contract, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+contractColumns+` FROM contracts WHERE id = ?`, id)
	c, err := scanContract(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("contract %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return c, nil
}

// ContractFilter is the query surface of `mycontext contract list`.
type ContractFilter struct {
	AccountID string
	Status    string
	Kind      string
	Search    string
	Limit     int
}

// ListContracts returns contracts, most recently signed first.
func (s *Store) ListContracts(ctx context.Context, f ContractFilter) ([]*Contract, error) {
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
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.Search != "" {
		where = append(where, "name LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}

	query := `SELECT ` + contractColumns + ` FROM contracts`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY sign_date IS NULL, sign_date DESC, created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*Contract{}
	for rows.Next() {
		c, err := scanContract(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, c)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// receivable_plans: when the money is supposed to arrive.
// ---------------------------------------------------------------------------

// SetReceivablePlanInput is the payload of `plan.set`. (contract_id, seq)
// identifies the instalment: setting the same seq again corrects it in
// place, the same way a repeated metric_samples row at one instant is a
// correction rather than a second observation.
type SetReceivablePlanInput struct {
	ContractID    string  `json:"contract_id"`
	Seq           int     `json:"seq"`
	DueDate       string  `json:"due_date"`
	Amount        float64 `json:"amount"`
	ConditionNote string  `json:"condition_note,omitempty"`
	Status        string  `json:"status,omitempty"`
}

func (in *SetReceivablePlanInput) normalize() error {
	if in.ContractID == "" {
		return protocol.BadInput("contract_id is required")
	}
	if in.Seq <= 0 {
		return protocol.BadInput("seq must be greater than 0")
	}
	if err := ValidateDate("due_date", in.DueDate); err != nil {
		return err
	}
	if in.Amount <= 0 {
		return protocol.BadInput("amount must be greater than 0")
	}
	if in.Status == "" {
		in.Status = "planned"
	}
	if !validReceivablePlanStatus[in.Status] {
		return protocol.BadInput("status must be planned|invoiced|received|waived")
	}
	return nil
}

// SetReceivablePlan declares one instalment of a contract's expected income.
// The sum of these is deliberately never checked against contracts.amount -
// that disagreement, if any, is v_biz_quality_issues' to state.
func (s *Store) SetReceivablePlan(ctx context.Context, wc WriteContext, in SetReceivablePlanInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "plan.set", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, "contracts", in.ContractID, "contract"); err != nil {
			return nil, err
		}
		ts := system.FormatTimestamp(now)
		id := system.NewID("rp")
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO receivable_plans (id, contract_id, seq, due_date, amount,
                                          condition_note, status, version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,1,?,?)
            ON CONFLICT(contract_id, seq) DO UPDATE SET
                due_date = excluded.due_date,
                amount = excluded.amount,
                condition_note = excluded.condition_note,
                status = excluded.status,
                updated_at = excluded.updated_at,
                version = receivable_plans.version + 1`,
			id, in.ContractID, in.Seq, in.DueDate, in.Amount, nullString(in.ConditionNote),
			in.Status, ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "contract", in.ContractID, "plan_change", nil, in); err != nil {
			return nil, err
		}
		plan, err := loadReceivablePlan(ctx, tx, in.ContractID, in.Seq)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: plan,
			Changes: []protocol.Change{{EntityType: "contract", EntityID: in.ContractID,
				EventType: "plan_change", ProjectionKeys: []string{"contracts", "contract:" + in.ContractID}}},
		}, nil
	})
}

const receivablePlanColumns = `
    id, contract_id, seq, due_date, amount, condition_note, status,
    version, created_at, updated_at`

func scanReceivablePlan(row interface{ Scan(...any) error }) (*ReceivablePlan, error) {
	var p ReceivablePlan
	err := row.Scan(&p.ID, &p.ContractID, &p.Seq, &p.DueDate, &p.Amount, &p.ConditionNote,
		&p.Status, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func loadReceivablePlan(ctx context.Context, tx *sql.Tx, contractID string, seq int) (*ReceivablePlan, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+receivablePlanColumns+` FROM receivable_plans WHERE contract_id = ? AND seq = ?`,
		contractID, seq)
	p, err := scanReceivablePlan(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("receivable plan %s/%d does not exist", contractID, seq)
	}
	return p, err
}

// ListReceivablePlans returns the instalments of one contract, in sequence.
func (s *Store) ListReceivablePlans(ctx context.Context, contractID string) ([]*ReceivablePlan, error) {
	if contractID == "" {
		return nil, protocol.BadInput("contract_id is required")
	}
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT `+receivablePlanColumns+` FROM receivable_plans WHERE contract_id = ? ORDER BY seq`,
		contractID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*ReceivablePlan{}
	for rows.Next() {
		p, err := scanReceivablePlan(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, p)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// receipts: money that actually arrived.
// ---------------------------------------------------------------------------

// RecordReceiptInput is the payload of `receipt.record`. plan_id is nullable
// on purpose - an unplanned payment is still a payment.
type RecordReceiptInput struct {
	ContractID string  `json:"contract_id"`
	PlanID     string  `json:"plan_id,omitempty"`
	ReceivedAt string  `json:"received_at"`
	Amount     float64 `json:"amount"`
	Method     string  `json:"method,omitempty"`
	Note       string  `json:"note,omitempty"`
}

func (in *RecordReceiptInput) normalize() error {
	if in.ContractID == "" {
		return protocol.BadInput("contract_id is required")
	}
	if in.ReceivedAt == "" {
		return protocol.BadInput("received_at is required")
	}
	if err := ValidateTimestamp("received_at", in.ReceivedAt); err != nil {
		return err
	}
	if in.Amount <= 0 {
		return protocol.BadInput("amount must be greater than 0")
	}
	return nil
}

// RecordReceipt logs money that arrived. It never marks a plan received or
// adjusts the contract amount - whether receipts exceed the contract amount
// is a fact v_biz_quality_issues states, never one this write blocks.
func (s *Store) RecordReceipt(ctx context.Context, wc WriteContext, in RecordReceiptInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "receipt.record", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, "contracts", in.ContractID, "contract"); err != nil {
			return nil, err
		}
		if in.PlanID != "" {
			plan, err := loadReceivablePlanByID(ctx, tx, in.PlanID)
			if err != nil {
				return nil, err
			}
			if plan.ContractID != in.ContractID {
				return nil, protocol.BadInput("plan %s belongs to contract %s, not %s", in.PlanID, plan.ContractID, in.ContractID)
			}
		}
		id := system.NewID("rcpt")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO receipts (id, contract_id, plan_id, received_at, amount, method, note, created_at)
            VALUES (?,?,?,?,?,?,?,?)`,
			id, in.ContractID, nullString(in.PlanID), in.ReceivedAt, in.Amount,
			nullString(in.Method), nullString(in.Note), ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "contract", in.ContractID, "received", nil, in); err != nil {
			return nil, err
		}
		r := Receipt{ID: id, ContractID: in.ContractID, ReceivedAt: in.ReceivedAt,
			Amount: in.Amount, CreatedAt: ts}
		assignOptional(&r.PlanID, in.PlanID)
		assignOptional(&r.Method, in.Method)
		assignOptional(&r.Note, in.Note)
		return &Result{
			Data: r,
			Changes: []protocol.Change{{EntityType: "contract", EntityID: in.ContractID,
				EventType: "received", ProjectionKeys: []string{"contracts", "contract:" + in.ContractID}}},
		}, nil
	})
}

func loadReceivablePlanByID(ctx context.Context, tx *sql.Tx, id string) (*ReceivablePlan, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+receivablePlanColumns+` FROM receivable_plans WHERE id = ?`, id)
	p, err := scanReceivablePlan(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("receivable plan %s does not exist", id)
	}
	return p, err
}

// ListReceipts returns the receipts recorded against one contract, newest
// first.
func (s *Store) ListReceipts(ctx context.Context, contractID string) ([]Receipt, error) {
	if contractID == "" {
		return nil, protocol.BadInput("contract_id is required")
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT id, contract_id, plan_id, received_at, amount, method, note, created_at
          FROM receipts WHERE contract_id = ? ORDER BY received_at DESC`, contractID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []Receipt{}
	for rows.Next() {
		var r Receipt
		if err := rows.Scan(&r.ID, &r.ContractID, &r.PlanID, &r.ReceivedAt, &r.Amount,
			&r.Method, &r.Note, &r.CreatedAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, r)
	}
	return out, sqlite.Classify(rows.Err())
}
