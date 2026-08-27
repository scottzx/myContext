package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
)

// The intake commands: capture evidence, propose candidates, revise them.
//
// `inbox confirm` is deliberately ABSENT. Confirming is what turns a model's
// guess into a business fact, and the design binds that act to a grant issued
// by a live UI session (design §3/§6). A CLI subcommand would be a second door
// into the same tables with none of that proof, so the CLI stops at proposing -
// which is exactly what an agent is allowed to do.

func newInboxCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Capture evidence and review what an extractor proposes",
		Long: "Evidence enters here as immutable bytes, becomes candidate facts,\n" +
			"and only becomes real business objects when a person confirms it in\n" +
			"the web UI. Nothing in this command group writes a business table.",
	}
	cmd.AddCommand(
		inboxCaptureTextCmd(opts),
		inboxListCmd(opts),
		inboxShowCmd(opts),
		inboxProposeCmd(opts),
		inboxArchiveCmd(opts),
	)
	return cmd
}

func inboxCaptureTextCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CaptureTextInput
	var textFile string

	cmd := &cobra.Command{
		Use:         "capture-text",
		Short:       "Seal pasted text into the Library and queue it for review",
		Annotations: map[string]string{"write": "true", "op": "inbox.capture-text"},
		Long: "Normalises the text to UTF-8 with LF endings, seals it as an immutable\n" +
			"capture package, then registers a document and one inbox item. The URL\n" +
			"in --source is recorded as provenance only; nothing is fetched.",
	}
	cmd.Flags().StringVar(&in.Title, "title", "", "what this is (defaults to the first line)")
	cmd.Flags().StringVar(&in.SourceRef, "source", "", "where it came from, recorded verbatim")
	cmd.Flags().StringVar(&textFile, "text-file", "", "read the text from a file, or - for stdin")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "inbox.capture-text"
		in.SchemaVersion = 1
		if rt.Opts.InputFile != "" {
			if err := rt.ReadInput(&in); err != nil {
				return rt.EmitError(command, err)
			}
		}
		if textFile != "" {
			text, err := readTextArg(textFile)
			if err != nil {
				return rt.EmitError(command, err)
			}
			in.Text = text
		}
		if strings.TrimSpace(in.Text) == "" {
			return rt.EmitError(command, protocol.BadInput("--text-file or --input is required"))
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.CaptureText(ctx, rt.WriteContext(), rt.Layout, in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			c, ok := data.(ops.CaptureTextResult)
			if !ok {
				return nil
			}
			fmt.Fprintf(w, "captured %d bytes\n", c.Bytes)
			fmt.Fprintf(w, "  package  %s\n  document %s\n  inbox    %s\n",
				c.PackageID, c.DocumentID, c.InboxID)
			return nil
		})
	})
	return cmd
}

func inboxListCmd(opts *GlobalOptions) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:         "list",
		Short:       "Show everything still waiting on a decision",
		Annotations: map[string]string{"op": "inbox.list"},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum items")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "inbox.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListInboxPending(ctx, limit)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, data any) error {
			rows, ok := data.([]ops.InboxPending)
			if !ok || len(rows) == 0 {
				fmt.Fprintln(w, "the inbox is empty")
				return nil
			}
			for _, r := range rows {
				undecided := r.UndecidedEntities + r.UndecidedFacts +
					r.UndecidedRelations + r.UndecidedActions
				fmt.Fprintf(w, "%-28s %-10s %s\n", r.InboxID, r.Status, derefOr(r.Title, "(untitled)"))
				if undecided > 0 {
					fmt.Fprintf(w, "%-28s %d candidates awaiting review\n", "", undecided)
				}
				if r.ErrorCode != nil {
					fmt.Fprintf(w, "%-28s error: %s\n", "", *r.ErrorCode)
				}
			}
			return nil
		})
	})
	return cmd
}

func inboxShowCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "show <inbox-id>",
		Short:       "Show one item with its original text and every candidate",
		Annotations: map[string]string{"op": "inbox.get"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "inbox.get"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		detail, err := store.GetInbox(ctx, rt.Layout, args[0])
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, detail, func(w io.Writer, data any) error {
			d, ok := data.(*ops.InboxDetail)
			if !ok {
				return nil
			}
			fmt.Fprintf(w, "%s  %s\n", d.Item.ID, d.Item.Status)
			if d.ActiveRunID == "" {
				fmt.Fprintln(w, "no extraction has been proposed yet")
				return nil
			}
			fmt.Fprintf(w, "run %s\n\n", d.ActiveRunID)
			for _, e := range d.Entities {
				fmt.Fprintf(w, "  [%s] %s %s %s\n", e.Status, e.Intent, e.EntityType, e.TargetLabel)
			}
			for _, f := range d.Facts {
				fmt.Fprintf(w, "  [%s] %s = %s\n", f.Status, f.FieldName, f.Source.Quote)
			}
			for _, r := range d.Relations {
				fmt.Fprintf(w, "  [%s] %s %s %s\n", r.Status, r.FromType, r.RelationType, r.ToType)
			}
			for _, a := range d.Actions {
				fmt.Fprintf(w, "  [%s] %s %s\n", a.Status, a.ActionType, marshalCompact(a.Draft))
			}
			fmt.Fprintln(w, "\nconfirm this in the web UI: mycontext ui serve")
			return nil
		})
	})
	return cmd
}

func inboxProposeCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "propose",
		Short:       "Submit candidate facts, relations and actions for an item",
		Annotations: map[string]string{"write": "true", "op": "inbox.propose"},
		Long: "Reads a proposal payload from --input. Candidates are validated against\n" +
			"the field, action and relation registries and every source locator is\n" +
			"re-hashed against the sealed original before anything is stored.",
	}
	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "inbox.propose"
		var in ops.ProposeInput
		if rt.Opts.InputFile == "" {
			return rt.EmitError(command, protocol.BadInput("--input is required"))
		}
		if err := rt.ReadInput(&in); err != nil {
			return rt.EmitError(command, err)
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.Propose(ctx, rt.WriteContext(), rt.Layout, in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			p, ok := data.(ops.ProposeResult)
			if !ok {
				return nil
			}
			fmt.Fprintf(w, "run %s (attempt %d): %d entities, %d facts, %d relations, %d actions\n",
				p.RunID, p.AttemptNo, p.Entities, p.Facts, p.Relations, p.Actions)
			fmt.Fprintln(w, "nothing is written to the business tables until a person confirms it")
			return nil
		})
	})
	return cmd
}

func inboxArchiveCmd(opts *GlobalOptions) *cobra.Command {
	var expectedVersion int64
	cmd := &cobra.Command{
		Use:         "archive <inbox-id>",
		Short:       "Drop an item from the queue without materialising anything",
		Annotations: map[string]string{"write": "true", "op": "inbox.archive"},
		Args:        cobra.ExactArgs(1),
		Long: "Archives the queue ITEM only. The sealed capture package and its\n" +
			"document stay exactly as they are: deciding not to file something is\n" +
			"not the same as deciding it never happened.",
	}
	cmd.Flags().Int64Var(&expectedVersion, "expected-version", 0, "version read before archiving")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "inbox.archive"
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.ArchiveInbox(ctx, rt.WriteContext(), ops.ArchiveInboxInput{
			SchemaVersion: 1, InboxID: args[0],
			ExpectedVersion: expectedVersion, Reason: rt.Opts.Reason,
		})
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			fmt.Fprintf(w, "archived %s\n", args[0])
			return nil
		})
	})
	return cmd
}

// newCandidateCmd exposes revision only. Accepting or rejecting is a person's
// act and lives behind a session-bound grant; revising is a proposal, which is
// what an agent is for.
func newCandidateCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "candidate", Short: "Work with proposed candidates"}
	cmd.AddCommand(candidateReviseCmd(opts))
	return cmd
}

func candidateReviseCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "revise",
		Short:       "Replace one candidate with a corrected version",
		Annotations: map[string]string{"write": "true", "op": "candidate.revise"},
		Long: "Reads {candidate_type, candidate_id, replacement} from --input. The\n" +
			"replacement is the FULL candidate DTO, not a patch: a corrected claim\n" +
			"still has to say which bytes it came from.",
	}
	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "candidate.revise"
		var in ops.ReviseInput
		if rt.Opts.InputFile == "" {
			return rt.EmitError(command, protocol.BadInput("--input is required"))
		}
		if err := rt.ReadInput(&in); err != nil {
			return rt.EmitError(command, err)
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.Revise(ctx, rt.WriteContext(), rt.Layout, in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			r, ok := data.(ops.ReviseResult)
			if !ok {
				return nil
			}
			fmt.Fprintf(w, "%s superseded by %s\n", r.OldCandidateID, r.NewCandidateID)
			return nil
		})
	})
	return cmd
}

func readTextArg(path string) (string, error) {
	if path == "-" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", protocol.Wrap(err, protocol.CodeBadInput, "cannot read stdin")
		}
		return string(raw), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", protocol.Wrap(err, protocol.CodeBadInput, "cannot read "+path)
	}
	return string(raw), nil
}

func derefOr(s *string, fallback string) string {
	if s == nil || *s == "" {
		return fallback
	}
	return *s
}

// marshalCompact keeps a draft readable on one terminal line.
func marshalCompact(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
