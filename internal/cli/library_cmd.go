package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/library"
)

// newLibraryCmd exposes library.Verify (technical design §15.2) as
// `mycontext library verify`. The Library is a peer of ops.db, not a
// subordinate of documents: a capture package can outlive, precede, or have
// no document row at all, which is exactly what the orphaned and
// pending-adoption rows of the recovery matrix describe.
func newLibraryCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "library", Short: "Operate on the Library's file store directly"}
	cmd.AddCommand(libraryVerifyCmd(opts))
	return cmd
}

func libraryVerifyCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Reconcile the Library's journal against what is actually on disk",
		Long: "Reconciles library_packages against the filesystem, resolving every row of\n" +
			"the §15.2 crash-recovery matrix: an interrupted commit is validated and\n" +
			"completed (sealed), a staged package with no journal record is moved aside\n" +
			"as orphaned, a corrupt manifest or asset is quarantined, and a journal\n" +
			"entry whose files are simply gone is reported as an integrity error.\n\n" +
			"This never fabricates a missing file and never deletes a user's file - the\n" +
			"worst it does is move a package's own folder under library/_system/, which\n" +
			"stays fully recoverable.",
	}
	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "library.verify"
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		report, err := store.VerifyLibrary(ctx, rt.Layout)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, report, func(w io.Writer, data any) error {
			r, ok := data.(*library.Report)
			if !ok {
				return nil
			}
			if len(r.Entries) == 0 {
				fmt.Fprintln(w, "no capture packages found")
				return nil
			}
			counts := map[library.Action]int{}
			for _, e := range r.Entries {
				counts[e.Action]++
				fmt.Fprintf(w, "%-18s %-24s %s\n", e.Action, e.PackageID, e.Detail)
			}
			fmt.Fprintf(w, "\n%d package(s): ", len(r.Entries))
			for _, a := range []library.Action{
				library.ActionOK, library.ActionResumedSealed, library.ActionPendingAdoption,
				library.ActionOrphaned, library.ActionQuarantined, library.ActionIntegrityError,
				library.ActionUnexpectedState,
			} {
				if n := counts[a]; n > 0 {
					fmt.Fprintf(w, "%s=%d ", a, n)
				}
			}
			fmt.Fprintln(w)
			return nil
		})
	})
	return cmd
}
