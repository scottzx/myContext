package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/ops"
)

// floatOrDash renders an optional float the way Deref renders an optional
// string, so amount-bearing lines never print a raw <nil>.
func floatOrDash(v *float64) string {
	if v == nil {
		return "-"
	}
	return trimFloat(*v)
}

// ---------------------------------------------------------------------------
// mycontext account create|update|get|list
// ---------------------------------------------------------------------------

func newAccountCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "account", Short: "Any external party we deal with"}
	cmd.AddCommand(accountCreateCmd(opts), accountUpdateCmd(opts), accountGetCmd(opts), accountListCmd(opts))
	return cmd
}

func accountLine(a *ops.Account) string {
	return fmt.Sprintf("%s  %s  [%s %s]  owner=%s  v%d", a.ID, a.Name, a.AccountType, a.Status, Deref(a.Owner), a.Version)
}

func accountCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateAccountInput
	cmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Record a counterparty (customer|prospect|partner|vendor|organizer|media|community|individual)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.ShortName, "short-name", "", "short/display name")
	cmd.Flags().StringVar(&in.AccountType, "type", "", "customer|prospect|partner|vendor|organizer|media|community|individual (default prospect)")
	cmd.Flags().StringVar(&in.Industry, "industry", "", "industry")
	cmd.Flags().StringVar(&in.Region, "region", "", "region")
	cmd.Flags().StringVar(&in.Status, "status", "", "active|dormant|archived (default active)")
	cmd.Flags().StringVar(&in.Owner, "owner", "", "who owns this relationship")
	cmd.Flags().StringVar(&in.Note, "note", "", "note")
	cmd.Flags().StringVar(&in.LegacyRef, "legacy-ref", "", "reference into the legacy data source")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "account.create"
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
		result, err := store.CreateAccount(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if a, ok := data.(*ops.Account); ok {
				fmt.Fprintf(w, "created %s  %s\n", a.ID, a.Name)
			}
			return nil
		})
	})
	return cmd
}

func accountUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateAccountInput
	var name, shortName, accountType, industry, region, status, owner, note string

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch an account (requires --expected-version)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&shortName, "short-name", "", "short/display name")
	cmd.Flags().StringVar(&accountType, "type", "", "customer|prospect|partner|vendor|organizer|media|community|individual")
	cmd.Flags().StringVar(&industry, "industry", "", "industry")
	cmd.Flags().StringVar(&region, "region", "", "region")
	cmd.Flags().StringVar(&status, "status", "", "active|dormant|archived")
	cmd.Flags().StringVar(&owner, "owner", "", "who owns this relationship")
	cmd.Flags().StringVar(&note, "note", "", "note")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "account.update"
		in.AccountID = args[0]
		setIfChanged(cmd, "name", name, &in.Name)
		setIfChanged(cmd, "short-name", shortName, &in.ShortName)
		setIfChanged(cmd, "type", accountType, &in.AccountType)
		setIfChanged(cmd, "industry", industry, &in.Industry)
		setIfChanged(cmd, "region", region, &in.Region)
		setIfChanged(cmd, "status", status, &in.Status)
		setIfChanged(cmd, "owner", owner, &in.Owner)
		setIfChanged(cmd, "note", note, &in.Note)

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		if in.ExpectedVersion == 0 && rt.Opts.Actor != "agent" {
			account, err := store.GetAccount(ctx, in.AccountID)
			if err != nil {
				return rt.EmitError(command, err)
			}
			in.ExpectedVersion = account.Version
		}
		result, err := store.UpdateAccount(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if a, ok := data.(*ops.Account); ok {
				fmt.Fprintln(w, accountLine(a))
			}
			return nil
		})
	})
	return cmd
}

func accountGetCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "get <id>", Short: "Show one account", Args: cobra.ExactArgs(1)}
	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "account.get"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		a, err := store.GetAccount(ctx, args[0])
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, a, func(w io.Writer, data any) error {
			if a, ok := data.(*ops.Account); ok {
				fmt.Fprintln(w, accountLine(a))
			}
			return nil
		})
	})
	return cmd
}

func accountListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.AccountFilter
	cmd := &cobra.Command{Use: "list", Short: "List accounts, most recently updated first"}
	cmd.Flags().StringVar(&f.AccountType, "type", "", "filter by account_type")
	cmd.Flags().StringVar(&f.Status, "status", "", "filter by status")
	cmd.Flags().StringVar(&f.Search, "search", "", "match name")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "account.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListAccounts(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, a := range items {
				rows = append(rows, []string{a.ID, a.AccountType, a.Status, a.Name, Deref(a.Owner)})
			}
			return Table(w, []string{"ID", "TYPE", "STATUS", "NAME", "OWNER"}, rows)
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext contact create|update|get|list
// ---------------------------------------------------------------------------

func newContactCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "contact", Short: "A person at an account"}
	cmd.AddCommand(contactCreateCmd(opts), contactUpdateCmd(opts), contactGetCmd(opts), contactListCmd(opts))
	return cmd
}

func contactLine(c *ops.Contact) string {
	return fmt.Sprintf("%s  %s  [%s]  role=%s  account=%s  v%d",
		c.ID, c.Name, c.Status, Deref(c.DealRole), c.AccountID, c.Version)
}

func contactCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateContactInput
	cmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Add a person at an account",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.AccountID, "account", "", "account id this person belongs to (required)")
	cmd.Flags().StringVar(&in.Title, "title", "", "job title")
	cmd.Flags().StringVar(&in.DealRole, "role", "", "decider|influencer|user|gatekeeper")
	cmd.Flags().StringVar(&in.Phone, "phone", "", "phone")
	cmd.Flags().StringVar(&in.Email, "email", "", "email")
	cmd.Flags().StringVar(&in.Wechat, "wechat", "", "wechat id")
	cmd.Flags().StringVar(&in.Status, "status", "", "active|inactive|left|archived (default active)")
	cmd.Flags().StringVar(&in.Note, "note", "", "note")
	cmd.Flags().StringVar(&in.LegacyRef, "legacy-ref", "", "reference into the legacy data source")
	cmd.MarkFlagRequired("account")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "contact.create"
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
		result, err := store.CreateContact(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.Contact); ok {
				fmt.Fprintf(w, "created %s  %s\n", c.ID, c.Name)
			}
			return nil
		})
	})
	return cmd
}

func contactUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateContactInput
	var name, title, role, phone, email, wechat, status, note, account string

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch a contact (requires --expected-version)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&title, "title", "", "job title")
	cmd.Flags().StringVar(&role, "role", "", "decider|influencer|user|gatekeeper")
	cmd.Flags().StringVar(&phone, "phone", "", "phone")
	cmd.Flags().StringVar(&email, "email", "", "email")
	cmd.Flags().StringVar(&wechat, "wechat", "", "wechat id")
	cmd.Flags().StringVar(&status, "status", "", "active|inactive|left|archived")
	cmd.Flags().StringVar(&note, "note", "", "note")
	cmd.Flags().StringVar(&account, "account", "", "move to a different account id")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "contact.update"
		in.ContactID = args[0]
		setIfChanged(cmd, "name", name, &in.Name)
		setIfChanged(cmd, "title", title, &in.Title)
		setIfChanged(cmd, "role", role, &in.DealRole)
		setIfChanged(cmd, "phone", phone, &in.Phone)
		setIfChanged(cmd, "email", email, &in.Email)
		setIfChanged(cmd, "wechat", wechat, &in.Wechat)
		setIfChanged(cmd, "status", status, &in.Status)
		setIfChanged(cmd, "note", note, &in.Note)
		setIfChanged(cmd, "account", account, &in.AccountID)

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		if in.ExpectedVersion == 0 && rt.Opts.Actor != "agent" {
			contact, err := store.GetContact(ctx, in.ContactID)
			if err != nil {
				return rt.EmitError(command, err)
			}
			in.ExpectedVersion = contact.Version
		}
		result, err := store.UpdateContact(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.Contact); ok {
				fmt.Fprintln(w, contactLine(c))
			}
			return nil
		})
	})
	return cmd
}

func contactGetCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "get <id>", Short: "Show one contact", Args: cobra.ExactArgs(1)}
	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "contact.get"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		c, err := store.GetContact(ctx, args[0])
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, c, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.Contact); ok {
				fmt.Fprintln(w, contactLine(c))
			}
			return nil
		})
	})
	return cmd
}

func contactListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.ContactFilter
	cmd := &cobra.Command{Use: "list", Short: "List contacts, newest first"}
	cmd.Flags().StringVar(&f.AccountID, "account", "", "filter by account id")
	cmd.Flags().StringVar(&f.Status, "status", "", "filter by status")
	cmd.Flags().StringVar(&f.Search, "search", "", "match name")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "contact.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListContacts(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, c := range items {
				rows = append(rows, []string{c.ID, c.AccountID, c.Status, Deref(c.DealRole), c.Name})
			}
			return Table(w, []string{"ID", "ACCOUNT", "STATUS", "ROLE", "NAME"}, rows)
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext opp create|update|get|list, advance|win|lose
// ---------------------------------------------------------------------------

func newOpportunityCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "opp", Short: "A possible deal"}
	cmd.AddCommand(
		opportunityCreateCmd(opts), opportunityUpdateCmd(opts), opportunityGetCmd(opts), opportunityListCmd(opts),
		opportunityAdvanceCmd(opts), opportunityWinCmd(opts), opportunityLoseCmd(opts),
	)
	return cmd
}

func opportunityLine(o *ops.Opportunity) string {
	return fmt.Sprintf("%s  %s  [%s]  amount=%s  v%d", o.ID, o.Name, o.Stage, floatOrDash(o.EstAmount), o.Version)
}

// fillOpportunityVersion reads the current version for an interactive caller,
// the same convenience `project update` gives a person at a terminal - an
// agent still must pass --expected-version itself.
func fillOpportunityVersion(ctx context.Context, store *ops.Store, actor, id string, version *int64) error {
	if *version != 0 || actor == "agent" {
		return nil
	}
	o, err := store.GetOpportunity(ctx, id)
	if err != nil {
		return err
	}
	*version = o.Version
	return nil
}

func opportunityCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateOpportunityInput
	var amount, probability float64
	cmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Record a possible deal",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.AccountID, "account", "", "account id (required)")
	cmd.Flags().StringVar(&in.AreaID, "area", "", "area id")
	cmd.Flags().StringVar(&in.PrimaryContactID, "contact", "", "primary contact id")
	cmd.Flags().StringVar(&in.Source, "source", "", "where this came from")
	cmd.Flags().StringVar(&in.SourceBatch, "source-batch", "", "import/source batch tag")
	cmd.Flags().StringVar(&in.Stage, "stage", "", "lead|qualified|proposal|negotiation|won|lost (default lead)")
	cmd.Flags().Float64Var(&amount, "amount", 0, "estimated deal amount")
	cmd.Flags().Float64Var(&probability, "probability", 0, "win probability, 0-1")
	cmd.Flags().StringVar(&in.ExpectedSignDate, "sign-date", "", "expected sign date, YYYY-MM-DD")
	cmd.Flags().StringVar(&in.Owner, "owner", "", "who owns this deal")
	cmd.Flags().StringVar(&in.NextStep, "next-step", "", "what happens next")
	cmd.Flags().StringVar(&in.LostReason, "lost-reason", "", "why lost (required if --stage lost)")
	cmd.Flags().StringVar(&in.LegacyRef, "legacy-ref", "", "reference into the legacy data source")
	cmd.MarkFlagRequired("account")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "opportunity.create"
		if len(args) == 1 {
			in.Name = args[0]
		}
		bindFloat(cmd, "amount", &amount, &in.EstAmount)
		bindFloat(cmd, "probability", &probability, &in.WinProbability)
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
		result, err := store.CreateOpportunity(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if o, ok := data.(*ops.Opportunity); ok {
				fmt.Fprintf(w, "created %s  %s\n", o.ID, o.Name)
			}
			return nil
		})
	})
	return cmd
}

func opportunityUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateOpportunityInput
	var name, source, sourceBatch, contact, signDate, owner, nextStep, lostReason, note string
	var amount, probability float64

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch an opportunity (requires --expected-version; use `opp advance|win|lose` to change stage)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&source, "source", "", "where this came from")
	cmd.Flags().StringVar(&sourceBatch, "source-batch", "", "import/source batch tag")
	cmd.Flags().StringVar(&contact, "contact", "", "primary contact id")
	cmd.Flags().Float64Var(&amount, "amount", 0, "estimated deal amount")
	cmd.Flags().Float64Var(&probability, "probability", 0, "win probability, 0-1")
	cmd.Flags().StringVar(&signDate, "sign-date", "", "expected sign date, YYYY-MM-DD")
	cmd.Flags().StringVar(&owner, "owner", "", "who owns this deal")
	cmd.Flags().StringVar(&nextStep, "next-step", "", "what happens next")
	cmd.Flags().StringVar(&lostReason, "lost-reason", "", "correct the recorded lost reason (does not change stage)")
	cmd.Flags().StringVar(&note, "note", "", "note")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "opportunity.update"
		in.OpportunityID = args[0]
		setIfChanged(cmd, "name", name, &in.Name)
		setIfChanged(cmd, "source", source, &in.Source)
		setIfChanged(cmd, "source-batch", sourceBatch, &in.SourceBatch)
		setIfChanged(cmd, "contact", contact, &in.PrimaryContactID)
		setIfChanged(cmd, "sign-date", signDate, &in.ExpectedSignDate)
		setIfChanged(cmd, "owner", owner, &in.Owner)
		setIfChanged(cmd, "next-step", nextStep, &in.NextStep)
		setIfChanged(cmd, "lost-reason", lostReason, &in.LostReason)
		setIfChanged(cmd, "note", note, &in.Note)
		bindFloat(cmd, "amount", &amount, &in.EstAmount)
		bindFloat(cmd, "probability", &probability, &in.WinProbability)

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if err := fillOpportunityVersion(ctx, store, rt.Opts.Actor, in.OpportunityID, &in.ExpectedVersion); err != nil {
			return rt.EmitError(command, err)
		}
		result, err := store.UpdateOpportunity(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if o, ok := data.(*ops.Opportunity); ok {
				fmt.Fprintln(w, opportunityLine(o))
			}
			return nil
		})
	})
	return cmd
}

func opportunityGetCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "get <id>", Short: "Show one opportunity", Args: cobra.ExactArgs(1)}
	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "opportunity.get"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		o, err := store.GetOpportunity(ctx, args[0])
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, o, func(w io.Writer, data any) error {
			if o, ok := data.(*ops.Opportunity); ok {
				fmt.Fprintln(w, opportunityLine(o))
			}
			return nil
		})
	})
	return cmd
}

func opportunityListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.OpportunityFilter
	cmd := &cobra.Command{Use: "list", Short: "List opportunities, nearest expected sign date first"}
	cmd.Flags().StringVar(&f.AccountID, "account", "", "filter by account id")
	cmd.Flags().StringVar(&f.AreaID, "area", "", "filter by area id")
	cmd.Flags().StringVar(&f.Stage, "stage", "", "filter by stage")
	cmd.Flags().BoolVar(&f.OpenOnly, "open", false, "exclude won and lost")
	cmd.Flags().StringVar(&f.Search, "search", "", "match name")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "opportunity.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListOpportunities(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, o := range items {
				rows = append(rows, []string{
					o.ID, o.Stage, floatOrDash(o.EstAmount), Deref(o.ExpectedSignDate), o.Name, o.AccountID,
				})
			}
			return Table(w, []string{"ID", "STAGE", "AMOUNT", "SIGN", "NAME", "ACCOUNT"}, rows)
		})
	})
	return cmd
}

func opportunityAdvanceCmd(opts *GlobalOptions) *cobra.Command {
	var expectedVersion int64
	var to string
	cmd := &cobra.Command{
		Use:         "advance <id>",
		Short:       "Move an opportunity to a stage (leaving won/lost requires --reason)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&expectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&to, "to", "", "lead|qualified|proposal|negotiation|won|lost (required)")
	cmd.MarkFlagRequired("to")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "opportunity.update"
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if err := fillOpportunityVersion(ctx, store, rt.Opts.Actor, args[0], &expectedVersion); err != nil {
			return rt.EmitError(command, err)
		}
		in := ops.UpdateOpportunityInput{OpportunityID: args[0], ExpectedVersion: expectedVersion, Stage: &to}
		result, err := store.UpdateOpportunity(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if o, ok := data.(*ops.Opportunity); ok {
				fmt.Fprintln(w, opportunityLine(o))
			}
			return nil
		})
	})
	return cmd
}

func opportunityWinCmd(opts *GlobalOptions) *cobra.Command {
	var expectedVersion int64
	cmd := &cobra.Command{
		Use:         "win <id>",
		Short:       "Mark an opportunity won",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&expectedVersion, "expected-version", 0, "version read before editing")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "opportunity.update"
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if err := fillOpportunityVersion(ctx, store, rt.Opts.Actor, args[0], &expectedVersion); err != nil {
			return rt.EmitError(command, err)
		}
		won := "won"
		in := ops.UpdateOpportunityInput{OpportunityID: args[0], ExpectedVersion: expectedVersion, Stage: &won}
		result, err := store.UpdateOpportunity(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if o, ok := data.(*ops.Opportunity); ok {
				fmt.Fprintln(w, opportunityLine(o))
			}
			return nil
		})
	})
	return cmd
}

func opportunityLoseCmd(opts *GlobalOptions) *cobra.Command {
	var expectedVersion int64
	var reason string
	cmd := &cobra.Command{
		Use:         "lose <id>",
		Short:       "Mark an opportunity lost (requires --reason)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&expectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&reason, "reason", "", "why it was lost (required)")
	cmd.MarkFlagRequired("reason")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "opportunity.update"
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if err := fillOpportunityVersion(ctx, store, rt.Opts.Actor, args[0], &expectedVersion); err != nil {
			return rt.EmitError(command, err)
		}
		lost := "lost"
		in := ops.UpdateOpportunityInput{
			OpportunityID: args[0], ExpectedVersion: expectedVersion, Stage: &lost, LostReason: &reason,
		}
		result, err := store.UpdateOpportunity(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if o, ok := data.(*ops.Opportunity); ok {
				fmt.Fprintln(w, opportunityLine(o))
			}
			return nil
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext app create|update|get|list, advance|win|reject|withdraw
// ---------------------------------------------------------------------------

func newApplicationCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "app", Short: "Something we apply to and someone else decides"}
	cmd.AddCommand(
		applicationCreateCmd(opts), applicationUpdateCmd(opts), applicationGetCmd(opts), applicationListCmd(opts),
		applicationAdvanceCmd(opts), applicationWinCmd(opts), applicationRejectCmd(opts), applicationWithdrawCmd(opts),
	)
	return cmd
}

func applicationLine(a *ops.Application) string {
	return fmt.Sprintf("%s  %s  [%s]  prize=%s  v%d", a.ID, a.Name, a.Stage, floatOrDash(a.PrizeAmount), a.Version)
}

// fillApplicationVersion is fillOpportunityVersion's twin for applications.
func fillApplicationVersion(ctx context.Context, store *ops.Store, actor, id string, version *int64) error {
	if *version != 0 || actor == "agent" {
		return nil
	}
	a, err := store.GetApplication(ctx, id)
	if err != nil {
		return err
	}
	*version = a.Version
	return nil
}

func applicationCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateApplicationInput
	var prize float64
	cmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Record something we apply to (competition|program|job|listing|partnership)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.AreaID, "area", "", "area id")
	cmd.Flags().StringVar(&in.AccountID, "account", "", "account id, if this is with a known party")
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "project id this supports")
	cmd.Flags().StringVar(&in.Kind, "kind", "", "competition|program|job|listing|partnership (default competition)")
	cmd.Flags().StringVar(&in.Stage, "stage", "", "discovered|preparing|submitted|under_review|shortlisted|won|rejected|withdrawn (default discovered)")
	cmd.Flags().StringVar(&in.SubmittedAt, "submitted-at", "", "when submitted, RFC 3339")
	cmd.Flags().StringVar(&in.DecidedAt, "decided-at", "", "when decided, RFC 3339")
	cmd.Flags().Float64Var(&prize, "prize", 0, "prize/value amount")
	cmd.Flags().StringVar(&in.OutcomeNote, "outcome", "", "outcome note")
	cmd.Flags().StringVar(&in.RejectReason, "reject-reason", "", "why rejected (required if --stage rejected)")
	cmd.Flags().StringVar(&in.Owner, "owner", "", "who owns this")
	cmd.Flags().StringVar(&in.NextStep, "next-step", "", "what happens next")
	cmd.Flags().StringVar(&in.LegacyRef, "legacy-ref", "", "reference into the legacy data source")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "application.create"
		if len(args) == 1 {
			in.Name = args[0]
		}
		bindFloat(cmd, "prize", &prize, &in.PrizeAmount)
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
		result, err := store.CreateApplication(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if a, ok := data.(*ops.Application); ok {
				fmt.Fprintf(w, "created %s  %s\n", a.ID, a.Name)
			}
			return nil
		})
	})
	return cmd
}

func applicationUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateApplicationInput
	var name, project, account, area, submittedAt, decidedAt, outcome, rejectReason, owner, nextStep string
	var prize float64

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch an application (requires --expected-version; use `app advance|win|reject|withdraw` to change stage)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&project, "project", "", "project id this supports")
	cmd.Flags().StringVar(&account, "account", "", "account id, if this is with a known party")
	cmd.Flags().StringVar(&area, "area", "", "area id")
	cmd.Flags().StringVar(&submittedAt, "submitted-at", "", "when submitted, RFC 3339")
	cmd.Flags().StringVar(&decidedAt, "decided-at", "", "when decided, RFC 3339")
	cmd.Flags().Float64Var(&prize, "prize", 0, "prize/value amount")
	cmd.Flags().StringVar(&outcome, "outcome", "", "outcome note")
	cmd.Flags().StringVar(&rejectReason, "reject-reason", "", "correct the recorded reject reason (does not change stage)")
	cmd.Flags().StringVar(&owner, "owner", "", "who owns this")
	cmd.Flags().StringVar(&nextStep, "next-step", "", "what happens next")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "application.update"
		in.ApplicationID = args[0]
		setIfChanged(cmd, "name", name, &in.Name)
		setIfChanged(cmd, "project", project, &in.ProjectID)
		setIfChanged(cmd, "account", account, &in.AccountID)
		setIfChanged(cmd, "area", area, &in.AreaID)
		setIfChanged(cmd, "submitted-at", submittedAt, &in.SubmittedAt)
		setIfChanged(cmd, "decided-at", decidedAt, &in.DecidedAt)
		setIfChanged(cmd, "outcome", outcome, &in.OutcomeNote)
		setIfChanged(cmd, "reject-reason", rejectReason, &in.RejectReason)
		setIfChanged(cmd, "owner", owner, &in.Owner)
		setIfChanged(cmd, "next-step", nextStep, &in.NextStep)
		bindFloat(cmd, "prize", &prize, &in.PrizeAmount)

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if err := fillApplicationVersion(ctx, store, rt.Opts.Actor, in.ApplicationID, &in.ExpectedVersion); err != nil {
			return rt.EmitError(command, err)
		}
		result, err := store.UpdateApplication(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if a, ok := data.(*ops.Application); ok {
				fmt.Fprintln(w, applicationLine(a))
			}
			return nil
		})
	})
	return cmd
}

func applicationGetCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "get <id>", Short: "Show one application", Args: cobra.ExactArgs(1)}
	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "application.get"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		a, err := store.GetApplication(ctx, args[0])
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, a, func(w io.Writer, data any) error {
			if a, ok := data.(*ops.Application); ok {
				fmt.Fprintln(w, applicationLine(a))
			}
			return nil
		})
	})
	return cmd
}

func applicationListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.ApplicationFilter
	cmd := &cobra.Command{Use: "list", Short: "List applications, most recently submitted first"}
	cmd.Flags().StringVar(&f.AreaID, "area", "", "filter by area id")
	cmd.Flags().StringVar(&f.AccountID, "account", "", "filter by account id")
	cmd.Flags().StringVar(&f.Kind, "kind", "", "filter by kind")
	cmd.Flags().StringVar(&f.Stage, "stage", "", "filter by stage")
	cmd.Flags().BoolVar(&f.OpenOnly, "open", false, "exclude won, rejected and withdrawn")
	cmd.Flags().StringVar(&f.Search, "search", "", "match name")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "application.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListApplications(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, a := range items {
				rows = append(rows, []string{a.ID, a.Kind, a.Stage, Deref(a.SubmittedAt), a.Name})
			}
			return Table(w, []string{"ID", "KIND", "STAGE", "SUBMITTED", "NAME"}, rows)
		})
	})
	return cmd
}

func applicationAdvanceCmd(opts *GlobalOptions) *cobra.Command {
	var expectedVersion int64
	var to string
	cmd := &cobra.Command{
		Use:         "advance <id>",
		Short:       "Move an application to a stage",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&expectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&to, "to", "", "discovered|preparing|submitted|under_review|shortlisted|won|rejected|withdrawn (required)")
	cmd.MarkFlagRequired("to")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "application.update"
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if err := fillApplicationVersion(ctx, store, rt.Opts.Actor, args[0], &expectedVersion); err != nil {
			return rt.EmitError(command, err)
		}
		in := ops.UpdateApplicationInput{ApplicationID: args[0], ExpectedVersion: expectedVersion, Stage: &to}
		result, err := store.UpdateApplication(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if a, ok := data.(*ops.Application); ok {
				fmt.Fprintln(w, applicationLine(a))
			}
			return nil
		})
	})
	return cmd
}

func applicationWinCmd(opts *GlobalOptions) *cobra.Command {
	var expectedVersion int64
	cmd := &cobra.Command{
		Use:         "win <id>",
		Short:       "Mark an application won",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&expectedVersion, "expected-version", 0, "version read before editing")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "application.update"
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if err := fillApplicationVersion(ctx, store, rt.Opts.Actor, args[0], &expectedVersion); err != nil {
			return rt.EmitError(command, err)
		}
		won := "won"
		in := ops.UpdateApplicationInput{ApplicationID: args[0], ExpectedVersion: expectedVersion, Stage: &won}
		result, err := store.UpdateApplication(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if a, ok := data.(*ops.Application); ok {
				fmt.Fprintln(w, applicationLine(a))
			}
			return nil
		})
	})
	return cmd
}

func applicationRejectCmd(opts *GlobalOptions) *cobra.Command {
	var expectedVersion int64
	var reason string
	cmd := &cobra.Command{
		Use:         "reject <id>",
		Short:       "Mark an application rejected (requires --reason)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&expectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&reason, "reason", "", "why it was rejected (required)")
	cmd.MarkFlagRequired("reason")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "application.update"
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if err := fillApplicationVersion(ctx, store, rt.Opts.Actor, args[0], &expectedVersion); err != nil {
			return rt.EmitError(command, err)
		}
		rejected := "rejected"
		in := ops.UpdateApplicationInput{
			ApplicationID: args[0], ExpectedVersion: expectedVersion, Stage: &rejected, RejectReason: &reason,
		}
		result, err := store.UpdateApplication(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if a, ok := data.(*ops.Application); ok {
				fmt.Fprintln(w, applicationLine(a))
			}
			return nil
		})
	})
	return cmd
}

func applicationWithdrawCmd(opts *GlobalOptions) *cobra.Command {
	var expectedVersion int64
	cmd := &cobra.Command{
		Use:         "withdraw <id>",
		Short:       "Mark an application withdrawn",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&expectedVersion, "expected-version", 0, "version read before editing")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "application.update"
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if err := fillApplicationVersion(ctx, store, rt.Opts.Actor, args[0], &expectedVersion); err != nil {
			return rt.EmitError(command, err)
		}
		withdrawn := "withdrawn"
		in := ops.UpdateApplicationInput{ApplicationID: args[0], ExpectedVersion: expectedVersion, Stage: &withdrawn}
		result, err := store.UpdateApplication(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if a, ok := data.(*ops.Application); ok {
				fmt.Fprintln(w, applicationLine(a))
			}
			return nil
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext interaction log|list
// ---------------------------------------------------------------------------

func newInteractionCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "interaction", Short: "A conversation that happened"}
	cmd.AddCommand(interactionLogCmd(opts), interactionListCmd(opts))
	return cmd
}

func interactionLogCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.LogInteractionInput
	cmd := &cobra.Command{
		Use:         "log <subject-type> <subject-id>",
		Short:       "Record that a conversation happened (subject_type is one of: " + ops.EntityTypeList() + ")",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(2),
	}
	cmd.Flags().StringVar(&in.OccurredAt, "at", "", "when it happened, RFC 3339 (required)")
	cmd.Flags().StringVar(&in.Channel, "channel", "", "meeting|call|im|email|visit (default meeting)")
	cmd.Flags().StringVar(&in.Summary, "summary", "", "what was discussed")
	cmd.Flags().StringVar(&in.Participants, "participants", "", "who was there")
	cmd.Flags().StringVar(&in.Owner, "owner", "", "who logged this")
	cmd.MarkFlagRequired("at")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "interaction.log"
		in.SubjectType, in.SubjectID = args[0], args[1]
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
		result, err := store.LogInteraction(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if i, ok := data.(ops.Interaction); ok {
				fmt.Fprintf(w, "logged %s  %s->%s  (%s)\n", i.ID, i.SubjectType, i.SubjectID, i.OccurredAt)
			}
			return nil
		})
	})
	return cmd
}

func interactionListCmd(opts *GlobalOptions) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "list <subject-type> <subject-id>",
		Short: "List the conversation history for one subject, newest first",
		Args:  cobra.ExactArgs(2),
	}
	cmd.Flags().IntVar(&limit, "limit", 200, "maximum rows")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "interaction.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListInteractions(ctx, args[0], args[1], limit)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, i := range items {
				rows = append(rows, []string{i.OccurredAt, i.Channel, i.ID, Deref(i.Summary), Deref(i.Owner)})
			}
			return Table(w, []string{"WHEN", "CHANNEL", "ID", "SUMMARY", "OWNER"}, rows)
		})
	})
	return cmd
}
