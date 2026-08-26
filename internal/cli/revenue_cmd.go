package cli

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/ops"
)

// ---------------------------------------------------------------------------
// mycontext contract create|update|get|list, contract sign
// ---------------------------------------------------------------------------

func newContractCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "contract", Short: "Contractual income of every kind"}
	cmd.AddCommand(contractCreateCmd(opts), contractUpdateCmd(opts), contractGetCmd(opts),
		contractListCmd(opts), contractSignCmd(opts))
	return cmd
}

func contractCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateContractInput
	var amount float64
	var unitPrice, quantity float64

	cmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Record contractual income (declared amount is authoritative, never derived)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.AccountID, "account", "", "account id (required)")
	cmd.Flags().StringVar(&in.OpportunityID, "opportunity", "", "won opportunity this comes from")
	cmd.Flags().StringVar(&in.ApplicationID, "application", "", "won application this comes from")
	cmd.Flags().StringVar(&in.Kind, "kind", "", "sales|prize|sponsorship|grant|piecework|other (default sales)")
	cmd.Flags().StringVar(&in.ContractNo, "contract-no", "", "external contract number")
	cmd.Flags().StringVar(&in.SignDate, "sign-date", "", "sign date, YYYY-MM-DD")
	cmd.Flags().StringVar(&in.StartDate, "start", "", "window start, YYYY-MM-DD")
	cmd.Flags().StringVar(&in.EndDate, "end", "", "window end, YYYY-MM-DD")
	cmd.Flags().Float64Var(&amount, "amount", 0, "contract amount, the authoritative number (required)")
	cmd.Flags().Float64Var(&unitPrice, "unit-price", 0, "piecework: price per unit (extra column, never reconciled against amount)")
	cmd.Flags().Float64Var(&quantity, "quantity", 0, "piecework: quantity (extra column, never reconciled against amount)")
	cmd.Flags().StringVar(&in.Currency, "currency", "", "currency code (default CNY)")
	cmd.Flags().StringVar(&in.Status, "status", "", "draft|signed|active|completed|terminated (default draft)")
	cmd.Flags().StringVar(&in.PaymentTerms, "payment-terms", "", "payment terms")
	cmd.Flags().StringVar(&in.Note, "note", "", "note")
	cmd.Flags().StringVar(&in.LegacyRef, "legacy-ref", "", "reference into the legacy data source")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "contract.create"
		if len(args) == 1 {
			in.Name = args[0]
		}
		in.Amount = amount
		bindFloat(cmd, "unit-price", &unitPrice, &in.UnitPrice)
		bindFloat(cmd, "quantity", &quantity, &in.Quantity)
		if rt.Opts.InputFile != "" {
			if err := rt.ReadInput(&in); err != nil {
				return rt.EmitError(command, err)
			}
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.CreateContract(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.Contract); ok {
				fmt.Fprintf(w, "created %s  %s  [%s %s]  %s %s\n", c.ID, c.Name, c.Kind, c.Status,
					trimFloat(c.Amount), c.Currency)
			}
			return nil
		})
	})
	return cmd
}

func contractUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateContractInput
	var account, name, contractNo, kind, signDate, start, end, currency, status, paymentTerms, note string
	var amount, unitPrice, quantity float64

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch a contract (requires --expected-version; never reconciles amount against plans/receipts)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing (required)")
	cmd.Flags().StringVar(&account, "account", "", "move to account id (a signed contract may not move accounts)")
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&contractNo, "contract-no", "", "external contract number")
	cmd.Flags().StringVar(&kind, "kind", "", "sales|prize|sponsorship|grant|piecework|other")
	cmd.Flags().StringVar(&signDate, "sign-date", "", "sign date, YYYY-MM-DD")
	cmd.Flags().StringVar(&start, "start", "", "window start, YYYY-MM-DD")
	cmd.Flags().StringVar(&end, "end", "", "window end, YYYY-MM-DD")
	cmd.Flags().Float64Var(&amount, "amount", 0, "contract amount, the authoritative number")
	cmd.Flags().Float64Var(&unitPrice, "unit-price", 0, "piecework: price per unit")
	cmd.Flags().Float64Var(&quantity, "quantity", 0, "piecework: quantity")
	cmd.Flags().StringVar(&currency, "currency", "", "currency code")
	cmd.Flags().StringVar(&status, "status", "", "draft|signed|active|completed|terminated (signed/etc require sign_date)")
	cmd.Flags().StringVar(&paymentTerms, "payment-terms", "", "payment terms")
	cmd.Flags().StringVar(&note, "note", "", "note")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "contract.update"
		in.ContractID = args[0]
		setIfChanged(cmd, "account", account, &in.AccountID)
		setIfChanged(cmd, "name", name, &in.Name)
		setIfChanged(cmd, "contract-no", contractNo, &in.ContractNo)
		setIfChanged(cmd, "kind", kind, &in.Kind)
		setIfChanged(cmd, "sign-date", signDate, &in.SignDate)
		setIfChanged(cmd, "start", start, &in.StartDate)
		setIfChanged(cmd, "end", end, &in.EndDate)
		setIfChanged(cmd, "currency", currency, &in.Currency)
		setIfChanged(cmd, "status", status, &in.Status)
		setIfChanged(cmd, "payment-terms", paymentTerms, &in.PaymentTerms)
		setIfChanged(cmd, "note", note, &in.Note)
		bindFloat(cmd, "amount", &amount, &in.Amount)
		bindFloat(cmd, "unit-price", &unitPrice, &in.UnitPrice)
		bindFloat(cmd, "quantity", &quantity, &in.Quantity)

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.UpdateContract(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.Contract); ok {
				fmt.Fprintf(w, "%s  %s  [%s %s]  %s %s  v%d\n", c.ID, c.Name, c.Kind, c.Status,
					trimFloat(c.Amount), c.Currency, c.Version)
			}
			return nil
		})
	})
	return cmd
}

func contractSignCmd(opts *GlobalOptions) *cobra.Command {
	var date string
	var expectedVersion int64

	cmd := &cobra.Command{
		Use:         "sign <id>",
		Short:       "Mark a contract signed as of a date (requires --expected-version)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&date, "date", "", "sign date, YYYY-MM-DD (required)")
	cmd.Flags().Int64Var(&expectedVersion, "expected-version", 0, "version read before signing (required)")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "contract.sign"
		status := "signed"
		in := ops.UpdateContractInput{
			ContractID:      args[0],
			ExpectedVersion: expectedVersion,
			Status:          &status,
			SignDate:        &date,
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.UpdateContract(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.Contract); ok {
				fmt.Fprintf(w, "%s  %s  signed %s\n", c.ID, c.Name, Deref(c.SignDate))
			}
			return nil
		})
	})
	return cmd
}

func contractGetCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Show one contract",
		Args:  cobra.ExactArgs(1),
	}
	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "contract.get"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		c, err := store.GetContract(ctx, args[0])
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, c, func(w io.Writer, _ any) error {
			fmt.Fprintf(w, "%s  [%s %s]  v%d\n", c.Name, c.Kind, c.Status, c.Version)
			fmt.Fprintf(w, "id:            %s\n", c.ID)
			fmt.Fprintf(w, "account:       %s\n", c.AccountID)
			fmt.Fprintf(w, "contract no:   %s\n", Deref(c.ContractNo))
			fmt.Fprintf(w, "sign date:     %s\n", Deref(c.SignDate))
			fmt.Fprintf(w, "window:        %s ~ %s\n", Deref(c.StartDate), Deref(c.EndDate))
			fmt.Fprintf(w, "amount:        %s %s\n", trimFloat(c.Amount), c.Currency)
			fmt.Fprintf(w, "unit price:    %s\n", derefFloat(c.UnitPrice))
			fmt.Fprintf(w, "quantity:      %s\n", derefFloat(c.Quantity))
			fmt.Fprintf(w, "payment terms: %s\n", Deref(c.PaymentTerms))
			fmt.Fprintf(w, "note:          %s\n", Deref(c.Note))
			return nil
		})
	})
	return cmd
}

func contractListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.ContractFilter
	cmd := &cobra.Command{Use: "list", Short: "List contracts, most recently signed first", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&f.AccountID, "account", "", "filter by account id")
	cmd.Flags().StringVar(&f.Status, "status", "", "filter by status")
	cmd.Flags().StringVar(&f.Kind, "kind", "", "filter by kind")
	cmd.Flags().StringVar(&f.Search, "search", "", "match name")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "contract.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListContracts(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, c := range items {
				rows = append(rows, []string{
					c.ID, c.Kind, c.Status, Deref(c.SignDate),
					trimFloat(c.Amount), c.Currency, c.Name,
				})
			}
			return Table(w, []string{"ID", "KIND", "STATUS", "SIGNED", "AMOUNT", "CCY", "NAME"}, rows)
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext plan set|list
// ---------------------------------------------------------------------------

func newPlanCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "plan", Short: "When a slice of a contract's money is supposed to arrive"}
	cmd.AddCommand(planSetCmd(opts), planListCmd(opts))
	return cmd
}

func planSetCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.SetReceivablePlanInput
	var amount float64

	cmd := &cobra.Command{
		Use:         "set",
		Short:       "Declare an instalment of a contract's expected income (same seq again corrects it in place)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.NoArgs,
	}
	cmd.Flags().StringVar(&in.ContractID, "contract", "", "contract id (required)")
	cmd.Flags().IntVar(&in.Seq, "seq", 0, "instalment sequence number, from 1 (required)")
	cmd.Flags().StringVar(&in.DueDate, "due", "", "due date, YYYY-MM-DD (required)")
	cmd.Flags().Float64Var(&amount, "amount", 0, "instalment amount (required, never reconciled against contract.amount)")
	cmd.Flags().StringVar(&in.ConditionNote, "condition", "", "condition attached to this instalment")
	cmd.Flags().StringVar(&in.Status, "status", "", "planned|invoiced|received|waived (default planned)")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "plan.set"
		in.Amount = amount
		if rt.Opts.InputFile != "" {
			if err := rt.ReadInput(&in); err != nil {
				return rt.EmitError(command, err)
			}
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.SetReceivablePlan(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if p, ok := data.(*ops.ReceivablePlan); ok {
				fmt.Fprintf(w, "%s  seq %d  due %s  %s  [%s]\n", p.ContractID, p.Seq, p.DueDate,
					trimFloat(p.Amount), p.Status)
			}
			return nil
		})
	})
	return cmd
}

func planListCmd(opts *GlobalOptions) *cobra.Command {
	var contractID string
	cmd := &cobra.Command{Use: "list", Short: "List one contract's receivable instalments, in sequence", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&contractID, "contract", "", "contract id (required)")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "plan.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListReceivablePlans(ctx, contractID)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, p := range items {
				rows = append(rows, []string{
					fmt.Sprintf("%d", p.Seq), p.DueDate, trimFloat(p.Amount), p.Status, Deref(p.ConditionNote),
				})
			}
			return Table(w, []string{"SEQ", "DUE", "AMOUNT", "STATUS", "CONDITION"}, rows)
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext receipt record|list
// ---------------------------------------------------------------------------

func newReceiptCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "receipt", Short: "Money that actually arrived"}
	cmd.AddCommand(receiptRecordCmd(opts), receiptListCmd(opts))
	return cmd
}

func receiptRecordCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.RecordReceiptInput
	var amount float64

	cmd := &cobra.Command{
		Use:         "record",
		Short:       "Log money that arrived against a contract (never marks a plan received or adjusts amount)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.NoArgs,
	}
	cmd.Flags().StringVar(&in.ContractID, "contract", "", "contract id (required)")
	cmd.Flags().StringVar(&in.PlanID, "plan", "", "receivable plan id this receipt applies to (an unplanned payment is still a payment)")
	cmd.Flags().StringVar(&in.ReceivedAt, "at", "", "when it arrived, RFC 3339 with timezone (required)")
	cmd.Flags().Float64Var(&amount, "amount", 0, "amount received (required, never reconciled against contract.amount)")
	cmd.Flags().StringVar(&in.Method, "method", "", "how it arrived")
	cmd.Flags().StringVar(&in.Note, "note", "", "note")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "receipt.record"
		in.Amount = amount
		if rt.Opts.InputFile != "" {
			if err := rt.ReadInput(&in); err != nil {
				return rt.EmitError(command, err)
			}
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.RecordReceipt(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if r, ok := data.(ops.Receipt); ok {
				fmt.Fprintf(w, "%s  %s  received %s\n", r.ContractID, trimFloat(r.Amount), r.ReceivedAt)
			}
			return nil
		})
	})
	return cmd
}

func receiptListCmd(opts *GlobalOptions) *cobra.Command {
	var contractID string
	cmd := &cobra.Command{Use: "list", Short: "List receipts recorded against one contract, newest first", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&contractID, "contract", "", "contract id (required)")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "receipt.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListReceipts(ctx, contractID)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, r := range items {
				rows = append(rows, []string{
					r.ID, r.ReceivedAt, trimFloat(r.Amount), Deref(r.Method), Deref(r.PlanID),
				})
			}
			return Table(w, []string{"ID", "RECEIVED", "AMOUNT", "METHOD", "PLAN"}, rows)
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext receivable list (read-only: v_contract_receivable / v_receivable_aging)
// ---------------------------------------------------------------------------

func newReceivableCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "receivable", Short: "Outstanding and aging contract income (read-only views)"}
	cmd.AddCommand(receivableListCmd(opts))
	return cmd
}

func receivableListCmd(opts *GlobalOptions) *cobra.Command {
	var contractID, accountID string
	var aging, overdueOnly bool
	var limit int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List outstanding balances by contract, or open instalments with --aging",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&contractID, "contract", "", "filter by contract id")
	cmd.Flags().StringVar(&accountID, "account", "", "filter by account id")
	cmd.Flags().BoolVar(&aging, "aging", false, "show open instalments bucketed by days overdue (v_receivable_aging) instead of per-contract balances")
	cmd.Flags().BoolVar(&overdueOnly, "overdue", false, "with --aging, only instalments already past due (v_receivable_overdue)")
	cmd.Flags().IntVar(&limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "receivable.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		db := store.DB().SQL()

		if aging {
			rows, err := queryReceivableAging(ctx, db, contractID, accountID, overdueOnly, limit)
			if err != nil {
				return rt.EmitError(command, err)
			}
			return rt.EmitData(command, rows, renderReceivableAging)
		}
		rows, err := queryContractReceivable(ctx, db, contractID, accountID, limit)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, rows, renderContractReceivable)
	})
	return cmd
}

func clampReceivableLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 200
	}
	return limit
}

// contractReceivableRow mirrors v_contract_receivable (005_business_core.sql):
// declared amount, planned amount, received amount, outstanding, and whether
// the declared amount and the plan/line totals agree. Every *_mismatch and
// over_received flag here is a fact the view states, never one this command
// resolves or blocks a write over.
type contractReceivableRow struct {
	ContractID        string   `json:"contract_id"`
	ContractNo        *string  `json:"contract_no"`
	Name              string   `json:"name"`
	Kind              string   `json:"kind"`
	Status            string   `json:"status"`
	Currency          string   `json:"currency"`
	SignDate          *string  `json:"sign_date"`
	StartDate         *string  `json:"start_date"`
	EndDate           *string  `json:"end_date"`
	PaymentTerms      *string  `json:"payment_terms"`
	AccountID         string   `json:"account_id"`
	AccountName       *string  `json:"account_name"`
	OpportunityID     *string  `json:"opportunity_id"`
	ApplicationID     *string  `json:"application_id"`
	DeclaredAmount    float64  `json:"declared_amount"`
	PlannedAmount     float64  `json:"planned_amount"`
	PlanCount         int64    `json:"plan_count"`
	WaivedAmount      float64  `json:"waived_amount"`
	ReceivedAmount    float64  `json:"received_amount"`
	ReceiptCount      int64    `json:"receipt_count"`
	LastReceiptAt     *string  `json:"last_receipt_at"`
	OutstandingAmount float64  `json:"outstanding_amount"`
	ReceivedRatio     float64  `json:"received_ratio"`
	PlanGap           float64  `json:"plan_gap"`
	PlanMismatch      bool     `json:"plan_mismatch"`
	UnitPrice         *float64 `json:"unit_price"`
	Quantity          *float64 `json:"quantity"`
	LineAmount        *float64 `json:"line_amount"`
	LineMismatch      bool     `json:"line_mismatch"`
	OverReceived      bool     `json:"over_received"`
	Version           int64    `json:"version"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

const contractReceivableColumns = `
    contract_id, contract_no, name, kind, status, currency,
    sign_date, start_date, end_date, payment_terms,
    account_id, account_name, opportunity_id, application_id,
    declared_amount, planned_amount, plan_count, waived_amount,
    received_amount, receipt_count, last_receipt_at, outstanding_amount,
    received_ratio, plan_gap, plan_mismatch,
    unit_price, quantity, line_amount, line_mismatch, over_received,
    version, created_at, updated_at`

func queryContractReceivable(ctx context.Context, db *sql.DB, contractID, accountID string, limit int) ([]contractReceivableRow, error) {
	query := `SELECT ` + contractReceivableColumns + ` FROM v_contract_receivable WHERE 1=1`
	var args []any
	if contractID != "" {
		query += " AND contract_id = ?"
		args = append(args, contractID)
	}
	if accountID != "" {
		query += " AND account_id = ?"
		args = append(args, accountID)
	}
	query += " ORDER BY outstanding_amount DESC LIMIT ?"
	args = append(args, clampReceivableLimit(limit))

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []contractReceivableRow{}
	for rows.Next() {
		var r contractReceivableRow
		if err := rows.Scan(&r.ContractID, &r.ContractNo, &r.Name, &r.Kind, &r.Status, &r.Currency,
			&r.SignDate, &r.StartDate, &r.EndDate, &r.PaymentTerms,
			&r.AccountID, &r.AccountName, &r.OpportunityID, &r.ApplicationID,
			&r.DeclaredAmount, &r.PlannedAmount, &r.PlanCount, &r.WaivedAmount,
			&r.ReceivedAmount, &r.ReceiptCount, &r.LastReceiptAt, &r.OutstandingAmount,
			&r.ReceivedRatio, &r.PlanGap, &r.PlanMismatch,
			&r.UnitPrice, &r.Quantity, &r.LineAmount, &r.LineMismatch, &r.OverReceived,
			&r.Version, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, r)
	}
	return out, sqlite.Classify(rows.Err())
}

func renderContractReceivable(w io.Writer, data any) error {
	rows, ok := data.([]contractReceivableRow)
	if !ok {
		return nil
	}
	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		flags := ""
		if r.PlanMismatch {
			flags += "plan_mismatch "
		}
		if r.LineMismatch {
			flags += "line_mismatch "
		}
		if r.OverReceived {
			flags += "over_received "
		}
		tableRows = append(tableRows, []string{
			r.ContractID, r.Name, r.Status,
			trimFloat(r.DeclaredAmount), trimFloat(r.ReceivedAmount), trimFloat(r.OutstandingAmount),
			flags,
		})
	}
	return Table(w, []string{"CONTRACT", "NAME", "STATUS", "DECLARED", "RECEIVED", "OUTSTANDING", "FLAGS"}, tableRows)
}

// receivableAgingRow mirrors v_receivable_aging / v_receivable_overdue
// (005_business_core.sql): one still-open instalment, with its remaining
// open amount and how many days past due it is.
type receivableAgingRow struct {
	PlanID         string  `json:"plan_id"`
	ContractID     string  `json:"contract_id"`
	ContractNo     *string `json:"contract_no"`
	ContractName   string  `json:"contract_name"`
	ContractKind   string  `json:"contract_kind"`
	ContractStatus string  `json:"contract_status"`
	Currency       string  `json:"currency"`
	AccountID      string  `json:"account_id"`
	AccountName    *string `json:"account_name"`
	Seq            int     `json:"seq"`
	DueDate        string  `json:"due_date"`
	PlannedAmount  float64 `json:"planned_amount"`
	OpenAmount     float64 `json:"open_amount"`
	Status         string  `json:"status"`
	ConditionNote  *string `json:"condition_note"`
	DaysOverdue    int64   `json:"days_overdue"`
	AgingBucket    string  `json:"aging_bucket"`
}

const receivableAgingColumns = `
    plan_id, contract_id, contract_no, contract_name, contract_kind, contract_status, currency,
    account_id, account_name, seq, due_date, planned_amount, open_amount,
    status, condition_note, days_overdue, aging_bucket`

func queryReceivableAging(ctx context.Context, db *sql.DB, contractID, accountID string, overdueOnly bool, limit int) ([]receivableAgingRow, error) {
	view := "v_receivable_aging"
	if overdueOnly {
		view = "v_receivable_overdue"
	}
	query := `SELECT ` + receivableAgingColumns + ` FROM ` + view + ` WHERE 1=1`
	var args []any
	if contractID != "" {
		query += " AND contract_id = ?"
		args = append(args, contractID)
	}
	if accountID != "" {
		query += " AND account_id = ?"
		args = append(args, accountID)
	}
	query += " ORDER BY days_overdue DESC LIMIT ?"
	args = append(args, clampReceivableLimit(limit))

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []receivableAgingRow{}
	for rows.Next() {
		var r receivableAgingRow
		if err := rows.Scan(&r.PlanID, &r.ContractID, &r.ContractNo, &r.ContractName, &r.ContractKind,
			&r.ContractStatus, &r.Currency, &r.AccountID, &r.AccountName, &r.Seq, &r.DueDate,
			&r.PlannedAmount, &r.OpenAmount, &r.Status, &r.ConditionNote, &r.DaysOverdue,
			&r.AgingBucket); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, r)
	}
	return out, sqlite.Classify(rows.Err())
}

func renderReceivableAging(w io.Writer, data any) error {
	rows, ok := data.([]receivableAgingRow)
	if !ok {
		return nil
	}
	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		tableRows = append(tableRows, []string{
			r.PlanID, r.ContractID, fmt.Sprintf("%d", r.Seq), r.DueDate,
			trimFloat(r.OpenAmount), fmt.Sprintf("%d", r.DaysOverdue), r.AgingBucket, r.Status,
		})
	}
	return Table(w, []string{"PLAN", "CONTRACT", "SEQ", "DUE", "OPEN", "DAYS", "BUCKET", "STATUS"}, tableRows)
}

// ---------------------------------------------------------------------------
// mycontext product create|update|get|list
// ---------------------------------------------------------------------------

func newProductCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "product", Short: "The hub a contract sells, a content piece promotes, a release iterates"}
	cmd.AddCommand(productCreateCmd(opts), productUpdateCmd(opts), productGetCmd(opts), productListCmd(opts))
	return cmd
}

func productCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateProductInput
	cmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Register a product, service or solution",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.Kind, "kind", "", "product|service|solution (default product)")
	cmd.Flags().StringVar(&in.Status, "status", "", "concept|developing|released|maintained|sunset (default concept)")
	cmd.Flags().StringVar(&in.Positioning, "positioning", "", "positioning")
	cmd.Flags().StringVar(&in.RepoURL, "repo", "", "repository URL")
	cmd.Flags().StringVar(&in.Owner, "owner", "", "owner")
	cmd.Flags().StringVar(&in.LaunchDate, "launch-date", "", "launch date, YYYY-MM-DD")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "product.create"
		if len(args) == 1 {
			in.Name = args[0]
		}
		if rt.Opts.InputFile != "" {
			if err := rt.ReadInput(&in); err != nil {
				return rt.EmitError(command, err)
			}
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.CreateProduct(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if p, ok := data.(*ops.Product); ok {
				fmt.Fprintf(w, "created %s  %s  [%s %s]\n", p.ID, p.Name, p.Kind, p.Status)
			}
			return nil
		})
	})
	return cmd
}

func productUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateProductInput
	var name, kind, status, positioning, repoURL, owner, releaseID, launchDate string

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch a product (requires --expected-version)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing (required)")
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&kind, "kind", "", "product|service|solution")
	cmd.Flags().StringVar(&status, "status", "", "concept|developing|released|maintained|sunset")
	cmd.Flags().StringVar(&positioning, "positioning", "", "positioning")
	cmd.Flags().StringVar(&repoURL, "repo", "", "repository URL")
	cmd.Flags().StringVar(&owner, "owner", "", "owner")
	cmd.Flags().StringVar(&releaseID, "release", "", "current release id")
	cmd.Flags().StringVar(&launchDate, "launch-date", "", "launch date, YYYY-MM-DD")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "product.update"
		in.ProductID = args[0]
		setIfChanged(cmd, "name", name, &in.Name)
		setIfChanged(cmd, "kind", kind, &in.Kind)
		setIfChanged(cmd, "status", status, &in.Status)
		setIfChanged(cmd, "positioning", positioning, &in.Positioning)
		setIfChanged(cmd, "repo", repoURL, &in.RepoURL)
		setIfChanged(cmd, "owner", owner, &in.Owner)
		setIfChanged(cmd, "release", releaseID, &in.CurrentReleaseID)
		setIfChanged(cmd, "launch-date", launchDate, &in.LaunchDate)

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.UpdateProduct(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if p, ok := data.(*ops.Product); ok {
				fmt.Fprintf(w, "%s  %s  [%s %s]  v%d\n", p.ID, p.Name, p.Kind, p.Status, p.Version)
			}
			return nil
		})
	})
	return cmd
}

func productGetCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Show one product",
		Args:  cobra.ExactArgs(1),
	}
	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "product.get"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		p, err := store.GetProduct(ctx, args[0])
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, p, func(w io.Writer, _ any) error {
			fmt.Fprintf(w, "%s  [%s %s]  v%d\n", p.Name, p.Kind, p.Status, p.Version)
			fmt.Fprintf(w, "id:            %s\n", p.ID)
			fmt.Fprintf(w, "positioning:   %s\n", Deref(p.Positioning))
			fmt.Fprintf(w, "repo:          %s\n", Deref(p.RepoURL))
			fmt.Fprintf(w, "owner:         %s\n", Deref(p.Owner))
			fmt.Fprintf(w, "release:       %s\n", Deref(p.CurrentReleaseID))
			fmt.Fprintf(w, "launch date:   %s\n", Deref(p.LaunchDate))
			return nil
		})
	})
	return cmd
}

func productListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.ProductFilter
	cmd := &cobra.Command{Use: "list", Short: "List products, newest activity first", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&f.Kind, "kind", "", "filter by kind")
	cmd.Flags().StringVar(&f.Status, "status", "", "filter by status")
	cmd.Flags().StringVar(&f.Search, "search", "", "match name")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "product.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListProducts(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, p := range items {
				rows = append(rows, []string{p.ID, p.Kind, p.Status, Deref(p.LaunchDate), p.Name})
			}
			return Table(w, []string{"ID", "KIND", "STATUS", "LAUNCH", "NAME"}, rows)
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext ticket open|update|close|list
// ---------------------------------------------------------------------------

func newTicketCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "ticket", Short: "Service tickets: what happens after delivery"}
	cmd.AddCommand(ticketOpenCmd(opts), ticketUpdateCmd(opts), ticketCloseCmd(opts), ticketListCmd(opts))
	return cmd
}

func ticketOpenCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateServiceTicketInput
	cmd := &cobra.Command{
		Use:         "open <title>",
		Short:       "Open a service ticket",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.AccountID, "account", "", "account id (required)")
	cmd.Flags().StringVar(&in.ContractID, "contract", "", "related contract id")
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "related project id")
	cmd.Flags().StringVar(&in.OpenedAt, "at", "", "when it was opened, RFC 3339 with timezone (required)")
	cmd.Flags().StringVar(&in.Kind, "kind", "", "question|incident|change_request|training|other (default question)")
	cmd.Flags().StringVar(&in.Severity, "severity", "", "S1|S2|S3|S4 (default S3)")
	cmd.Flags().StringVar(&in.Assignee, "assignee", "", "assignee")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "ticket.open"
		if len(args) == 1 {
			in.Title = args[0]
		}
		if rt.Opts.InputFile != "" {
			if err := rt.ReadInput(&in); err != nil {
				return rt.EmitError(command, err)
			}
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.CreateServiceTicket(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if t, ok := data.(*ops.ServiceTicket); ok {
				fmt.Fprintf(w, "opened %s  %s  [%s %s]\n", t.ID, t.Title, t.Severity, t.Status)
			}
			return nil
		})
	})
	return cmd
}

func ticketUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateServiceTicketInput
	var title, kind, severity, status, assignee, resolution, contractID, projectID string

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch a ticket (requires --expected-version)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing (required)")
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&kind, "kind", "", "question|incident|change_request|training|other")
	cmd.Flags().StringVar(&severity, "severity", "", "S1|S2|S3|S4")
	cmd.Flags().StringVar(&status, "status", "", "open|in_progress|waiting|resolved|closed")
	cmd.Flags().StringVar(&assignee, "assignee", "", "assignee")
	cmd.Flags().StringVar(&resolution, "resolution", "", "how it was, or is being, resolved")
	cmd.Flags().StringVar(&contractID, "contract", "", "move to contract id")
	cmd.Flags().StringVar(&projectID, "project", "", "move to project id")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "ticket.update"
		in.TicketID = args[0]
		setIfChanged(cmd, "title", title, &in.Title)
		setIfChanged(cmd, "kind", kind, &in.Kind)
		setIfChanged(cmd, "severity", severity, &in.Severity)
		setIfChanged(cmd, "status", status, &in.Status)
		setIfChanged(cmd, "assignee", assignee, &in.Assignee)
		setIfChanged(cmd, "resolution", resolution, &in.Resolution)
		setIfChanged(cmd, "contract", contractID, &in.ContractID)
		setIfChanged(cmd, "project", projectID, &in.ProjectID)

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.UpdateServiceTicket(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if t, ok := data.(*ops.ServiceTicket); ok {
				fmt.Fprintf(w, "%s  %s  [%s %s]  v%d\n", t.ID, t.Title, t.Severity, t.Status, t.Version)
			}
			return nil
		})
	})
	return cmd
}

func ticketCloseCmd(opts *GlobalOptions) *cobra.Command {
	var expectedVersion int64
	var resolution string

	cmd := &cobra.Command{
		Use:         "close <id>",
		Short:       "Close a ticket (requires --expected-version; resolution is a note, not a gate)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&expectedVersion, "expected-version", 0, "version read before closing (required)")
	cmd.Flags().StringVar(&resolution, "resolution", "", "how it was resolved")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "ticket.close"
		status := "closed"
		in := ops.UpdateServiceTicketInput{
			TicketID:        args[0],
			ExpectedVersion: expectedVersion,
			Status:          &status,
		}
		if resolution != "" {
			in.Resolution = &resolution
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.UpdateServiceTicket(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if t, ok := data.(*ops.ServiceTicket); ok {
				fmt.Fprintf(w, "%s  %s  [%s]\n", t.ID, t.Title, t.Status)
			}
			return nil
		})
	})
	return cmd
}

func ticketListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.ServiceTicketFilter
	cmd := &cobra.Command{Use: "list", Short: "List tickets, oldest open first", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&f.AccountID, "account", "", "filter by account id")
	cmd.Flags().StringVar(&f.Status, "status", "", "filter by status")
	cmd.Flags().StringVar(&f.Severity, "severity", "", "filter by severity")
	cmd.Flags().BoolVar(&f.OpenOnly, "open", false, "exclude closed")
	cmd.Flags().StringVar(&f.Search, "search", "", "match title")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "ticket.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListServiceTickets(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, t := range items {
				rows = append(rows, []string{
					t.ID, t.Severity, t.Status, t.Kind, t.OpenedAt, t.AccountID, t.Title,
				})
			}
			return Table(w, []string{"ID", "SEV", "STATUS", "KIND", "OPENED", "ACCOUNT", "TITLE"}, rows)
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// small local helpers
// ---------------------------------------------------------------------------

// derefFloat renders an optional money/quantity value, or a dash when unset.
func derefFloat(v *float64) string {
	if v == nil {
		return "-"
	}
	return trimFloat(*v)
}
