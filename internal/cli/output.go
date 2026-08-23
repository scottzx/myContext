package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
)

// TextRenderer prints a human-readable form of a command's data. Commands
// supply one; JSON output never calls it.
type TextRenderer func(w io.Writer, data any) error

// Emit renders a successful result and returns the process exit code.
// stdout carries only the final payload; everything else goes to stderr.
func (rt *Runtime) Emit(command string, result *ops.Result, render TextRenderer) int {
	env := protocol.Envelope{
		Protocol:  protocol.Version,
		OK:        true,
		Command:   command,
		RequestID: rt.Opts.RequestID,
		Data:      result.Data,
		Changes:   result.Changes,
		Warnings:  result.Warnings,
		Meta:      rt.meta(),
	}
	return rt.write(env, render)
}

// EmitData renders a read-only result.
func (rt *Runtime) EmitData(command string, data any, render TextRenderer) int {
	env := protocol.Envelope{
		Protocol: protocol.Version,
		OK:       true,
		Command:  command,
		Data:     data,
		Meta:     rt.meta(),
	}
	return rt.write(env, render)
}

// EmitError renders a failure and returns the documented exit code (§11.4).
func (rt *Runtime) EmitError(command string, err error) int {
	app := AsAppError(err)
	env := protocol.Envelope{
		Protocol:  protocol.Version,
		OK:        false,
		Command:   command,
		RequestID: rt.Opts.RequestID,
		Error: &protocol.Error{
			Code:      app.Code,
			Message:   app.Message,
			Details:   app.Details,
			Retryable: app.Retryable,
		},
		Meta: rt.meta(),
	}
	if rt.jsonMode() {
		rt.encode(rt.Stdout, env)
	} else {
		fmt.Fprintf(rt.Stderr, "error [%s]: %s\n", app.Code, app.Message)
		if rt.Opts.Trace && app.Cause != nil {
			fmt.Fprintf(rt.Stderr, "cause: %v\n", app.Cause)
		}
		if app.Details != nil {
			rt.encode(rt.Stderr, app.Details)
		}
	}
	return app.ExitCode()
}

func (rt *Runtime) write(env protocol.Envelope, render TextRenderer) int {
	if rt.jsonMode() {
		if err := rt.encode(rt.Stdout, env); err != nil {
			fmt.Fprintf(rt.Stderr, "cannot encode output: %v\n", err)
			return protocol.ExitIntegrity
		}
		return protocol.ExitOK
	}
	for _, w := range env.Warnings {
		fmt.Fprintf(rt.Stderr, "warning: %s\n", w)
	}
	if render == nil {
		return rt.EmitError(env.Command, protocol.Internal("no text renderer for %s", env.Command))
	}
	if err := render(rt.Stdout, env.Data); err != nil {
		return rt.EmitError(env.Command, err)
	}
	return protocol.ExitOK
}

func (rt *Runtime) meta() protocol.Meta {
	return protocol.Meta{
		Root:           rt.Root,
		CLIVersion:     Version,
		SchemaVersions: rt.SchemaVersions(),
		DurationMS:     time.Since(rt.started).Milliseconds(),
		Actor:          rt.Opts.Actor,
		DryRun:         rt.Opts.DryRun,
	}
}

func (rt *Runtime) jsonMode() bool {
	return rt.Opts.Format == "json" || rt.Opts.Format == "ndjson"
}

func (rt *Runtime) encode(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if rt.Opts.Format != "ndjson" {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(value)
}

// ---------------------------------------------------------------------------
// Text helpers. Deliberately plain: no colour codes by default, no progress
// bars, nothing that would confuse a pipe.
// ---------------------------------------------------------------------------

// Table renders aligned columns.
func Table(w io.Writer, headers []string, rows [][]string) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, "(none)")
		return nil
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = runeLen(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && runeLen(cell) > widths[i] {
				widths[i] = runeLen(cell)
			}
		}
	}
	var b strings.Builder
	for i, h := range headers {
		b.WriteString(pad(h, widths[i]))
		if i < len(headers)-1 {
			b.WriteString("  ")
		}
	}
	fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	for _, row := range rows {
		var line strings.Builder
		for i := range headers {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			line.WriteString(pad(cell, widths[i]))
			if i < len(headers)-1 {
				line.WriteString("  ")
			}
		}
		fmt.Fprintln(w, strings.TrimRight(line.String(), " "))
	}
	return nil
}

// runeLen approximates display width, counting CJK characters as two cells.
func runeLen(s string) int {
	width := 0
	for _, r := range s {
		if r >= 0x1100 && (r <= 0x115F || (r >= 0x2E80 && r <= 0xA4CF) ||
			(r >= 0xAC00 && r <= 0xD7A3) || (r >= 0xF900 && r <= 0xFAFF) ||
			(r >= 0xFE30 && r <= 0xFE6F) || (r >= 0xFF00 && r <= 0xFF60) ||
			(r >= 0xFFE0 && r <= 0xFFE6)) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

func pad(s string, width int) string {
	if diff := width - runeLen(s); diff > 0 {
		return s + strings.Repeat(" ", diff)
	}
	return s
}

// Deref renders an optional string, or a dash when unset.
func Deref(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

// DerefInt renders an optional int, or a dash when unset.
func DerefInt(v *int) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}

// Fatal reports a startup failure that happened before a Runtime existed.
func Fatal(err error) int {
	app := AsAppError(err)
	fmt.Fprintf(os.Stderr, "error [%s]: %s\n", app.Code, app.Message)
	return app.ExitCode()
}
