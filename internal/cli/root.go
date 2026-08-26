package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/protocol"
)

// exitCode is set by each command's RunE wrapper so main can exit correctly
// without cobra printing its own error text.
var exitCode int

// Execute builds the command tree and runs it, returning the process exit code.
func Execute(args []string) int {
	opts := &GlobalOptions{}
	root := &cobra.Command{
		Use:   "mycontext",
		Short: "个人经营上下文系统 — deterministic local CLI",
		Long: "mycontext is the deterministic core of the personal operations context system.\n" +
			"Each invocation opens what it needs, does one thing and exits; there is no daemon.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	flags := root.PersistentFlags()
	flags.StringVar(&opts.Root, "root", "", "data instance to operate on (overrides MINIS_ROOT)")
	flags.StringVar(&opts.Format, "format", "text", "output format: text|json|ndjson")
	flags.StringVar(&opts.RequestID, "request-id", "", "idempotency key for retryable writes")
	flags.StringVar(&opts.Actor, "actor", "user", "who is acting: user|agent|ui|migration|system")
	flags.DurationVar(&opts.Timeout, "timeout", 0, "upper bound for locks and I/O (default 30s)")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "report the change without committing it")
	flags.BoolVar(&opts.Trace, "trace", false, "print local diagnostics to stderr")
	flags.BoolVar(&opts.NoColor, "no-color", false, "disable colour in text output")
	flags.StringVar(&opts.Reason, "reason", "", "why this change is being made (audited)")
	flags.BoolVar(&opts.Confirm, "confirm", false, "explicit confirmation for a high-impact action")
	flags.StringVar(&opts.InputFile, "input", "", "read the JSON payload from a file, or - for stdin")

	root.AddCommand(
		newInitCmd(opts),
		newVersionCmd(opts),
		newDoctorCmd(opts),
		newSchemaCmd(opts),
		newBackupCmd(opts),
		newOpsCmd(opts),
		newTaskCmd(opts),
		newProjectCmd(opts),
		newAreaCmd(opts),
		newInitiativeCmd(opts),
		newScheduleCmd(opts),
		newCapacityCmd(opts),
		newEventCmd(opts),
		newObjectiveCmd(opts),
		newKeyResultCmd(opts),
		newMilestoneCmd(opts),
		newDepCmd(opts),
		newTagCmd(opts),
		newImportCmd(opts),
		newUICmd(opts),

		// 005/006 business core: the counterparty chain, then the objects the
		// non-consulting lines produce, then the cross-cutting layers.
		newAccountCmd(opts),
		newContactCmd(opts),
		newOpportunityCmd(opts),
		newApplicationCmd(opts),
		newInteractionCmd(opts),
		newContractCmd(opts),
		newPlanCmd(opts),
		newReceiptCmd(opts),
		newReceivableCmd(opts),
		newProductCmd(opts),
		newTicketCmd(opts),
		newDocCmd(opts),
		newLibraryCmd(opts),
		newMetricCmd(opts),
		newBizCmd(opts),

		// 006_content_product.sql: mode B (content, on our own channels) and
		// mode C (product releases and the campaigns that promote either).
		newChannelCmd(opts),
		newContentCmd(opts),
		newReleaseCmd(opts),
		newCampaignCmd(opts),
	)
	// Registered last: it describes the tree that now exists.
	root.AddCommand(newCatalogCmd(opts, root))

	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		// Flag and usage errors never reached a command body.
		fmt.Fprintf(os.Stderr, "error [%s]: %v\n", protocol.CodeBadInput, err)
		return protocol.ExitBadInput
	}
	return exitCode
}

// run wires a command body to the runtime lifecycle: build, defer close,
// capture the exit code. Every command body goes through here.
func run(opts *GlobalOptions, fn func(rt *Runtime) int) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := validateFormat(opts.Format); err != nil {
			exitCode = Fatal(err)
			return nil
		}
		rt, err := NewRuntime(*opts)
		if err != nil {
			exitCode = Fatal(err)
			return nil
		}
		defer rt.Close()
		exitCode = fn(rt)
		return nil
	}
}

func validateFormat(format string) error {
	switch format {
	case "text", "json", "ndjson":
		return nil
	default:
		return protocol.BadInput("--format must be text, json or ndjson, got %q", format)
	}
}

// runArgs is run() for commands that need their positional arguments.
func runArgs(opts *GlobalOptions, fn func(rt *Runtime, args []string) int) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := validateFormat(opts.Format); err != nil {
			exitCode = Fatal(err)
			return nil
		}
		rt, err := NewRuntime(*opts)
		if err != nil {
			exitCode = Fatal(err)
			return nil
		}
		defer rt.Close()
		exitCode = fn(rt, args)
		return nil
	}
}
