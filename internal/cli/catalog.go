package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/scottzx/mycontext/internal/protocol"
)

// The catalog is how an agent discovers what this binary can do without
// hardcoding a command list. It is derived from the live cobra tree, so it
// cannot drift from the commands that actually exist.

// Operation describes one invocable command.
type Operation struct {
	// Name is the canonical operation name, matching the `command` field of
	// the JSON envelope (e.g. "task.reschedule").
	Name    string `json:"name"`
	Invoke  string `json:"invoke"`
	Summary string `json:"summary"`
	Long    string `json:"long,omitempty"`

	// Write marks operations that mutate state. These accept --request-id and
	// most require --expected-version.
	Write bool `json:"write"`

	Arguments []Argument `json:"arguments,omitempty"`
	Flags     []Flag     `json:"flags,omitempty"`
}

type Argument struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

type Flag struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Default     string `json:"default,omitempty"`
}

// Catalog is the machine-readable contract an agent reads once and then uses
// to construct calls.
type Catalog struct {
	Protocol   string      `json:"protocol"`
	CLIVersion string      `json:"cli_version"`
	Binary     string      `json:"binary"`
	Global     []Flag      `json:"global_flags"`
	Operations []Operation `json:"operations"`
	ExitCodes  []ExitCode  `json:"exit_codes"`
	Envelope   Envelope    `json:"envelope"`
}

type ExitCode struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

// Envelope documents the response shape so a caller can parse without
// guessing at the field names.
type Envelope struct {
	Success string   `json:"success_shape"`
	Failure string   `json:"failure_shape"`
	Notes   []string `json:"notes"`
}

// canonicalName maps a cobra command to its protocol operation name. The
// command path is the name for everything except the bare system commands.
func canonicalName(cmd *cobra.Command) string {
	if op, ok := cmd.Annotations["op"]; ok && op != "" {
		return op
	}
	path := strings.Fields(cmd.CommandPath())
	if len(path) <= 1 {
		return ""
	}
	return strings.Join(path[1:], ".")
}

func isWrite(cmd *cobra.Command) bool {
	return cmd.Annotations["write"] == "true"
}

// BuildCatalog walks the command tree and describes every runnable leaf.
func BuildCatalog(root *cobra.Command) Catalog {
	catalog := Catalog{
		Protocol:   protocol.Version,
		CLIVersion: Version,
		Binary:     root.Name(),
		Global:     collectFlags(root.PersistentFlags()),
		ExitCodes: []ExitCode{
			{protocol.ExitOK, "success"},
			{protocol.ExitBadInput, "invalid arguments or input"},
			{protocol.ExitNotFound, "object does not exist"},
			{protocol.ExitConflict, "ambiguous match, version conflict or idempotency conflict"},
			{protocol.ExitIncompatible, "CLI and database schema are incompatible"},
			{protocol.ExitBusy, "database busy or lock timeout"},
			{protocol.ExitForbidden, "permission, privacy policy or missing confirmation"},
			{protocol.ExitIntegrity, "integrity check failed"},
			{protocol.ExitExternal, "external network or source fetch failed"},
			{protocol.ExitNeedsRecovery, "operation did not finish; recovery required"},
		},
		Envelope: Envelope{
			Success: `{"protocol","ok":true,"command","request_id","data","changes","warnings","meta"}`,
			Failure: `{"protocol","ok":false,"command","error":{"code","message","details","retryable"},"meta"}`,
			Notes: []string{
				"Branch on exit code and error.code, never on error.message text.",
				"stdout carries only the payload; diagnostics go to stderr.",
				"Timestamps are RFC 3339 with a timezone; calendar days are YYYY-MM-DD.",
				"IDs are always strings.",
				"changes[].projection_keys name the read models a write invalidated.",
			},
		},
	}

	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, child := range cmd.Commands() {
			if child.Hidden || child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			if child.Runnable() {
				if name := canonicalName(child); name != "" {
					catalog.Operations = append(catalog.Operations, describe(child, name))
				}
			}
			walk(child)
		}
	}
	walk(root)

	sort.Slice(catalog.Operations, func(i, j int) bool {
		return catalog.Operations[i].Name < catalog.Operations[j].Name
	})
	return catalog
}

func describe(cmd *cobra.Command, name string) Operation {
	return Operation{
		Name:      name,
		Invoke:    cmd.UseLine(),
		Summary:   cmd.Short,
		Long:      cmd.Long,
		Write:     isWrite(cmd),
		Arguments: parseArguments(cmd.Use),
		Flags:     collectFlags(cmd.Flags()),
	}
}

// parseArguments reads the positional arguments out of a Use string such as
// "reschedule <id> <YYYY-MM-DD>" or "day [YYYY-MM-DD]".
func parseArguments(use string) []Argument {
	fields := strings.Fields(use)
	if len(fields) <= 1 {
		return nil
	}
	var args []Argument
	for _, token := range fields[1:] {
		switch {
		case strings.HasPrefix(token, "<") && strings.HasSuffix(token, ">"):
			args = append(args, Argument{Name: strings.Trim(token, "<>"), Required: true})
		case strings.HasPrefix(token, "[") && strings.HasSuffix(token, "]"):
			args = append(args, Argument{Name: strings.Trim(token, "[]"), Required: false})
		}
	}
	return args
}

func collectFlags(set *pflag.FlagSet) []Flag {
	var flags []Flag
	set.VisitAll(func(f *pflag.Flag) {
		flags = append(flags, Flag{
			Name:        "--" + f.Name,
			Type:        f.Value.Type(),
			Description: f.Usage,
			Default:     f.DefValue,
		})
	})
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	return flags
}

func newCatalogCmd(opts *GlobalOptions, root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "catalog",
		Short:       "Describe every operation, flag and exit code as JSON",
		Annotations: map[string]string{"op": "system.catalog"},
		Long: "Emits the machine-readable command contract. An agent reads this\n" +
			"once to discover what it can call, instead of hardcoding a command list.",
	}
	cmd.RunE = run(opts, func(rt *Runtime) int {
		catalog := BuildCatalog(root)
		return rt.EmitData("system.catalog", catalog, func(w io.Writer, _ any) error {
			for _, op := range catalog.Operations {
				marker := " "
				if op.Write {
					marker = "*"
				}
				fmt.Fprintf(w, "%s %-24s %s\n", marker, op.Name, op.Summary)
			}
			fmt.Fprintf(w, "\n* = write operation (accepts --request-id)\n")
			return nil
		})
	})
	return cmd
}
