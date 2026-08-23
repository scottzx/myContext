package cli_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/scottzx/mycontext/internal/cli"
	"github.com/scottzx/mycontext/internal/protocol"
)

// runCLI executes one command against a temp root and captures stdout, the
// way a caller or an agent would see it.
func runCLI(t *testing.T, root string, args ...string) (string, int) {
	t.Helper()
	full := append([]string{"--root", root}, args...)

	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()

	code := cli.Execute(full)
	w.Close()
	os.Stdout = original
	return <-done, code
}

// decode parses a JSON envelope, failing the test if it is malformed.
func decode(t *testing.T, out string) protocol.Envelope {
	t.Helper()
	var env protocol.Envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("output is not a valid envelope: %v\n%s", err, out)
	}
	return env
}

func newRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if out, code := runCLI(t, root, "init", "--format", "json"); code != protocol.ExitOK {
		t.Fatalf("init failed (%d): %s", code, out)
	}
	return root
}

// createTask returns the id of a task created through the CLI.
func createTask(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, code := runCLI(t, root, append([]string{"task", "create", "--format", "json"}, args...)...)
	if code != protocol.ExitOK {
		t.Fatalf("task create failed (%d): %s", code, out)
	}
	var payload struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return payload.Data.ID
}

func TestVersionEnvelopeShape(t *testing.T) {
	out, code := runCLI(t, t.TempDir(), "version", "--format", "json")
	if code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	env := decode(t, out)
	if env.Protocol != protocol.Version {
		t.Fatalf("protocol %q, want %q", env.Protocol, protocol.Version)
	}
	if !env.OK {
		t.Fatal("ok should be true")
	}
	if env.Command != "system.version" {
		t.Fatalf("command %q", env.Command)
	}
	// meta.root must always be present so a caller can tell which instance
	// answered (§8.2).
	if env.Meta.Root == "" {
		t.Fatal("meta.root is empty")
	}
	if env.Meta.CLIVersion == "" {
		t.Fatal("meta.cli_version is empty")
	}
}

func TestInitIsIdempotent(t *testing.T) {
	root := t.TempDir()
	if _, code := runCLI(t, root, "init", "--format", "json"); code != protocol.ExitOK {
		t.Fatal("first init failed")
	}
	out, code := runCLI(t, root, "init", "--format", "json")
	if code != protocol.ExitOK {
		t.Fatalf("second init exit %d: %s", code, out)
	}
	var payload struct {
		Data struct {
			AlreadyExisted bool `json:"already_existed"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &payload)
	if !payload.Data.AlreadyExisted {
		t.Fatal("re-running init should report the existing instance")
	}
}

func TestDoctorPassesOnAFreshInstance(t *testing.T) {
	root := newRoot(t)
	out, code := runCLI(t, root, "doctor", "--format", "json")
	if code != protocol.ExitOK {
		t.Fatalf("doctor exit %d: %s", code, out)
	}
	var payload struct {
		Data struct {
			Summary map[string]int `json:"summary"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &payload)
	if payload.Data.Summary["fail"] != 0 {
		t.Fatalf("doctor reported failures: %s", out)
	}
}

func TestCommandsWithoutAnInstanceReportNotFound(t *testing.T) {
	out, code := runCLI(t, t.TempDir(), "ops", "status", "--format", "json")
	if code != protocol.ExitNotFound {
		t.Fatalf("exit %d, want %d: %s", code, protocol.ExitNotFound, out)
	}
	env := decode(t, out)
	if env.OK || env.Error == nil || env.Error.Code != protocol.CodeNotFound {
		t.Fatalf("unexpected error envelope: %s", out)
	}
}

func TestErrorEnvelopeAndExitCodes(t *testing.T) {
	root := newRoot(t)
	id := createTask(t, root, "版本冲突测试")

	cases := []struct {
		name     string
		args     []string
		wantExit int
		wantCode string
	}{
		{
			name:     "stale version",
			args:     []string{"task", "update", id, "--expected-version", "99", "--importance", "P0"},
			wantExit: protocol.ExitConflict,
			wantCode: protocol.CodeVersionConflict,
		},
		{
			name:     "unknown task",
			args:     []string{"task", "get", "task_does_not_exist"},
			wantExit: protocol.ExitNotFound,
			wantCode: protocol.CodeNotFound,
		},
		{
			name:     "invalid importance",
			args:     []string{"task", "create", "x", "--importance", "critical"},
			wantExit: protocol.ExitBadInput,
			wantCode: protocol.CodeBadInput,
		},
		{
			name:     "invalid date",
			args:     []string{"task", "reschedule", id, "2026/08/25"},
			wantExit: protocol.ExitBadInput,
			wantCode: protocol.CodeBadInput,
		},
		{
			name: "hard deadline without a reason",
			args: []string{"task", "update", id, "--expected-version", "1",
				"--hard-due", "2026-09-03T18:00:00+08:00"},
			wantExit: protocol.ExitForbidden,
			wantCode: protocol.CodeForbidden,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runCLI(t, root, append(tc.args, "--format", "json")...)
			if code != tc.wantExit {
				t.Fatalf("exit %d, want %d: %s", code, tc.wantExit, out)
			}
			env := decode(t, out)
			if env.OK {
				t.Fatalf("ok should be false: %s", out)
			}
			if env.Error == nil || env.Error.Code != tc.wantCode {
				t.Fatalf("error code %v, want %s", env.Error, tc.wantCode)
			}
			// An agent branches on the code, so the message must never be the
			// only signal.
			if env.Error.Message == "" {
				t.Fatal("error message is empty")
			}
		})
	}
}

func TestWriteReturnsChangesWithProjectionKeys(t *testing.T) {
	root := newRoot(t)
	out, code := runCLI(t, root, "task", "create", "带计划的任务",
		"--plan", "2026-08-25", "--estimate", "60", "--format", "json")
	if code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	env := decode(t, out)
	if len(env.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(env.Changes))
	}
	change := env.Changes[0]
	if change.EntityType != "task" || change.EventType != "created" {
		t.Fatalf("unexpected change: %+v", change)
	}
	// The UI refreshes only the projections a write touched (§17.1).
	var hasDay bool
	for _, key := range change.ProjectionKeys {
		if key == "day:2026-08-25" {
			hasDay = true
		}
	}
	if !hasDay {
		t.Fatalf("projection keys do not name the affected day: %v", change.ProjectionKeys)
	}
}

func TestTextOutputStaysOnStdoutAndIsReadable(t *testing.T) {
	root := newRoot(t)
	createTask(t, root, "文本渲染检查", "--importance", "P1", "--plan", "2026-08-25", "--estimate", "45")

	out, code := runCLI(t, root, "task", "list")
	if code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, want := range []string{"ID", "IMP", "STATUS", "PLANNED", "文本渲染检查", "P1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("text output is missing %q:\n%s", want, out)
		}
	}
	// Text mode must not emit JSON, or a human-facing pipe becomes ambiguous.
	if strings.Contains(out, "\"protocol\"") {
		t.Fatalf("text output contains a JSON envelope:\n%s", out)
	}
}

func TestJSONModeEmitsOnlyTheEnvelopeOnStdout(t *testing.T) {
	root := newRoot(t)
	// A dry run produces a warning, which must go to stderr in text mode but
	// stay inside the envelope in JSON mode (§11.3).
	out, code := runCLI(t, root, "--dry-run", "task", "create", "试运行", "--format", "json")
	if code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	env := decode(t, out)
	if len(env.Warnings) == 0 {
		t.Fatal("dry run did not report a warning")
	}
	if !env.Meta.DryRun {
		t.Fatal("meta.dry_run should be true")
	}
	if strings.Count(out, "\"protocol\"") != 1 {
		t.Fatalf("stdout should carry exactly one envelope:\n%s", out)
	}
}

func TestInvalidFormatIsRejected(t *testing.T) {
	_, code := runCLI(t, t.TempDir(), "version", "--format", "yaml")
	if code != protocol.ExitBadInput {
		t.Fatalf("exit %d, want %d", code, protocol.ExitBadInput)
	}
}

func TestAgentMustSupplyExpectedVersion(t *testing.T) {
	root := newRoot(t)
	id := createTask(t, root, "代理写入")

	out, code := runCLI(t, root, "--actor", "agent", "task", "complete", id, "--format", "json")
	if code != protocol.ExitBadInput {
		t.Fatalf("exit %d, want %d: %s", code, protocol.ExitBadInput, out)
	}
}

func TestScheduleWeekCoversSevenDays(t *testing.T) {
	root := newRoot(t)
	out, code := runCLI(t, root, "schedule", "week", "2026-08-24", "--format", "json")
	if code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	var payload struct {
		Data []struct {
			Load struct {
				Date string `json:"date"`
			} `json:"load"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &payload)
	if len(payload.Data) != 7 {
		t.Fatalf("week returned %d days, want 7", len(payload.Data))
	}
	if payload.Data[0].Load.Date != "2026-08-24" || payload.Data[6].Load.Date != "2026-08-30" {
		t.Fatalf("unexpected week range: %s..%s",
			payload.Data[0].Load.Date, payload.Data[6].Load.Date)
	}
}

// The catalog is the contract agents build calls from, so it must stay in
// sync with the real command tree rather than being maintained by hand.
func TestCatalogDescribesEveryOperation(t *testing.T) {
	out, code := runCLI(t, t.TempDir(), "catalog", "--format", "json")
	if code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	var payload struct {
		Data cli.Catalog `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	catalog := payload.Data

	if catalog.Protocol != protocol.Version {
		t.Fatalf("protocol %q", catalog.Protocol)
	}
	if len(catalog.ExitCodes) == 0 || len(catalog.Global) == 0 {
		t.Fatal("catalog is missing exit codes or global flags")
	}

	byName := make(map[string]cli.Operation, len(catalog.Operations))
	for _, op := range catalog.Operations {
		if op.Name == "" {
			t.Fatalf("operation with an empty canonical name: %+v", op)
		}
		if op.Summary == "" {
			t.Fatalf("%s has no summary; an agent cannot tell when to use it", op.Name)
		}
		byName[op.Name] = op
	}

	// Spot-check the operations an agent most needs to find, including the
	// write markers that tell it --request-id applies.
	for _, want := range []string{
		"ops.status", "task.list", "task.get", "task.create", "task.update",
		"task.reschedule", "task.complete", "task.set-review",
		"project.list", "project.tree", "schedule.day", "schedule.week",
		"capacity.set", "event.list", "system.version", "system.doctor",
	} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("catalog is missing %s", want)
		}
	}
	for _, write := range []string{"task.create", "task.reschedule", "capacity.set"} {
		if !byName[write].Write {
			t.Fatalf("%s should be marked as a write", write)
		}
	}
	for _, read := range []string{"ops.status", "task.list", "system.version"} {
		if byName[read].Write {
			t.Fatalf("%s should not be marked as a write", read)
		}
	}

	// Writes that need optimistic concurrency must advertise the flag.
	resched := byName["task.reschedule"]
	var hasExpectedVersion bool
	for _, f := range resched.Flags {
		if f.Name == "--expected-version" {
			hasExpectedVersion = true
		}
	}
	if !hasExpectedVersion {
		t.Fatal("task.reschedule does not advertise --expected-version")
	}
	if len(resched.Arguments) != 2 || !resched.Arguments[0].Required {
		t.Fatalf("task.reschedule arguments are wrong: %+v", resched.Arguments)
	}
}
