package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/ops"
)

// The case commands read the business-item workspace from the terminal.
//
// They are pure projections over the 010 views: which rows belong to a case and
// in what order is decided once in SQL, so `case timeline` and the web
// workspace cannot disagree about what happened.

func newCaseCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "case",
		Short: "Read a business item end to end: evidence, facts, execution, next step",
		Long: "A case is a projection rooted on a real business object - in this\n" +
			"version an opportunity. There is no `cases` table: the root is the\n" +
			"opportunity itself, and everything else is reached from it.",
	}
	cmd.AddCommand(caseListCmd(opts), caseShowCmd(opts), caseTimelineCmd(opts), caseNextCmd(opts))
	return cmd
}

func caseListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.CaseFilter
	cmd := &cobra.Command{
		Use:         "list",
		Short:       "List business items, most urgent first",
		Annotations: map[string]string{"op": "case.list"},
	}
	cmd.Flags().StringVar(&f.RootType, "root-type", "", "opportunity")
	cmd.Flags().StringVar(&f.Stage, "stage", "", "filter by stage")
	cmd.Flags().BoolVar(&f.OpenOnly, "open", false, "hide won and lost items")
	cmd.Flags().StringVar(&f.Search, "search", "", "match the title or the counterparty")
	cmd.Flags().IntVar(&f.Limit, "limit", 50, "maximum items")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "case.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		rows, err := store.ListCases(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, rows, func(w io.Writer, data any) error {
			cases, ok := data.([]ops.CaseIndexRow)
			if !ok || len(cases) == 0 {
				fmt.Fprintln(w, "no business items yet")
				return nil
			}
			for _, c := range cases {
				fmt.Fprintf(w, "%-26s %-12s %s · %s\n", c.RootID, c.Stage, c.Title, c.CounterpartyName)
				next := derefOr(c.NextMilestoneName, "")
				if next != "" {
					fmt.Fprintf(w, "%-26s next: %s %s\n", "", derefOr(c.NextMilestoneAt, ""), next)
				}
				if c.OverdueCount > 0 || c.WarningCount > 0 {
					fmt.Fprintf(w, "%-26s %d overdue · %d warnings\n", "", c.OverdueCount, c.WarningCount)
				}
			}
			return nil
		})
	})
	return cmd
}

func caseShowCmd(opts *GlobalOptions) *cobra.Command {
	var rootType string
	cmd := &cobra.Command{
		Use:         "show <root-id>",
		Short:       "Show one item's header, confirmed facts and warnings",
		Annotations: map[string]string{"op": "case.get"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&rootType, "root-type", "opportunity", "which kind of root this id is")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "case.get"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		detail, err := store.GetCase(ctx, rootType, args[0])
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, detail, func(w io.Writer, data any) error {
			d, ok := data.(*ops.CaseDetail)
			if !ok {
				return nil
			}
			fmt.Fprintf(w, "%s · %s\n%s / %s\n\n",
				d.Index.Title, d.Index.CounterpartyName, d.Index.Kind, d.Index.Stage)
			for key, value := range d.Facts {
				fmt.Fprintf(w, "  %-20s %s\n", key, value)
			}
			if len(d.Evidence) > 0 {
				fmt.Fprintln(w, "\nevidence")
				for _, e := range d.Evidence {
					mark := " "
					if e.IsCurrent {
						mark = "*"
					}
					fmt.Fprintf(w, "  %s %-20s %s\n", mark, e.FieldName, derefOr(e.DocumentTitle, ""))
				}
			}
			if len(d.Warnings) > 0 {
				fmt.Fprintln(w, "\nwarnings")
				for _, warn := range d.Warnings {
					fmt.Fprintf(w, "  %s: %s\n", warn.Issue, warn.Detail)
				}
			}
			return nil
		})
	})
	return cmd
}

func caseTimelineCmd(opts *GlobalOptions) *cobra.Command {
	var rootType, cursor string
	var limit int
	cmd := &cobra.Command{
		Use:         "timeline <root-id>",
		Short:       "Show what happened, newest first",
		Annotations: map[string]string{"op": "case.timeline"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&rootType, "root-type", "opportunity", "which kind of root this id is")
	cmd.Flags().StringVar(&cursor, "cursor", "", "continue from a previous page")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum items")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "case.timeline"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		out, err := store.GetCaseTimeline(ctx, rootType, args[0], cursor, limit)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, out, func(w io.Writer, data any) error {
			tl, ok := data.(*ops.CaseTimeline)
			if !ok || len(tl.Items) == 0 {
				fmt.Fprintln(w, "nothing has happened on this item yet")
				return nil
			}
			for _, it := range tl.Items {
				fmt.Fprintf(w, "%-22s %-12s %s\n", it.OccurredAt, it.ItemType, derefOr(it.Title, ""))
				if s := derefOr(it.Summary, ""); s != "" {
					fmt.Fprintf(w, "%-22s %s\n", "", strings.TrimSpace(s))
				}
			}
			if tl.NextCursor != "" {
				fmt.Fprintf(w, "\nmore: --cursor '%s'\n", tl.NextCursor)
			}
			return nil
		})
	})
	return cmd
}

func caseNextCmd(opts *GlobalOptions) *cobra.Command {
	var rootType string
	cmd := &cobra.Command{
		Use:         "next <root-id>",
		Short:       "Show the next node: milestones, open tasks, what is due",
		Annotations: map[string]string{"op": "case.next-actions"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&rootType, "root-type", "opportunity", "which kind of root this id is")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "case.next-actions"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		out, err := store.GetCaseNextActions(ctx, rootType, args[0])
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, out, func(w io.Writer, data any) error {
			n, ok := data.(*ops.CaseNextActions)
			if !ok {
				return nil
			}
			if n.NextMilestoneName != nil {
				fmt.Fprintf(w, "next node: %s %s\n",
					derefOr(n.NextMilestoneAt, ""), *n.NextMilestoneName)
			}
			fmt.Fprintf(w, "%d open tasks, %d overdue\n\n", n.OpenTaskCount, n.OverdueCount)
			for _, m := range n.Milestones {
				mark := " "
				if m.ReachedAt != nil {
					mark = "*"
				}
				fmt.Fprintf(w, "  %s %s  %s  %d/%d open\n",
					mark, m.TargetDate, m.Name, m.OpenTasks, m.TotalTasks)
			}
			for _, task := range n.Tasks {
				fmt.Fprintf(w, "  [ ] %-4s %-28s %s\n", task.Importance, task.Title,
					derefOr(task.PlannedDate, ""))
			}
			return nil
		})
	})
	return cmd
}
