// Package cli is the terminal adapter. It parses arguments, calls one
// application use case and renders the result; it holds no business rules.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	mycontext "github.com/scottzx/mycontext"
	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// Build metadata, injected with -ldflags at release time.
var (
	Version   = "0.1.0-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// GlobalOptions are the flags every command accepts (§10.2).
type GlobalOptions struct {
	Root      string
	Format    string
	RequestID string
	Actor     string
	Timeout   time.Duration
	DryRun    bool
	Trace     bool
	NoColor   bool
	Reason    string
	Confirm   bool
	InputFile string
}

// Runtime is one command's resolved environment. It is built per invocation
// and torn down on exit; nothing survives the process (§9).
type Runtime struct {
	Opts    GlobalOptions
	Root    string
	Layout  system.Layout
	Config  system.Config
	Clock   system.Clock
	Stdout  io.Writer
	Stderr  io.Writer
	started time.Time

	opsDB *sqlite.DB
}

// NewRuntime resolves the data root. It does not open any database yet: a
// command opens only what it needs.
func NewRuntime(opts GlobalOptions) (*Runtime, error) {
	root, err := system.ResolveRoot(opts.Root)
	if err != nil {
		return nil, err
	}
	rt := &Runtime{
		Opts:    opts,
		Root:    root,
		Layout:  system.NewLayout(root),
		Clock:   system.NewClock(),
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		started: time.Now(),
	}
	// A missing config is fine for `init` and `version`; those commands do
	// not call LoadConfig.
	if cfg, err := system.LoadConfig(rt.Layout); err == nil {
		rt.Config = cfg
		rt.applyTimezone(cfg.Timezone)
	}
	return rt, nil
}

// applyTimezone makes SQLite's 'localtime' modifier mean the timezone the
// user declared, not whatever the host happens to be set to. The
// deterministic views depend on this.
func (rt *Runtime) applyTimezone(name string) {
	if name == "" {
		return
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		fmt.Fprintf(rt.Stderr, "warning: unknown timezone %q, using host local time\n", name)
		return
	}
	time.Local = loc
}

// Context returns a context honouring --timeout.
func (rt *Runtime) Context() (context.Context, context.CancelFunc) {
	timeout := rt.Opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return context.WithTimeout(context.Background(), timeout)
}

// OpsStore opens ops.db and returns the ops repository. readOnly picks a
// read-only connection so a query can never take a write lock (§13.1).
func (rt *Runtime) OpsStore(readOnly bool) (*ops.Store, error) {
	if rt.Config.InstanceID == "" {
		return nil, protocol.NotFound("no mycontext instance at %s (run `mycontext init`)", rt.Root)
	}
	db, err := sqlite.Open(rt.Layout.OpsDB(), sqlite.Options{
		ReadOnly:    readOnly,
		BusyTimeout: rt.Config.BusyTimeout,
		JournalMode: rt.Config.JournalMode,
	})
	if err != nil {
		return nil, err
	}
	rt.opsDB = db

	if err := rt.assertSchemaCompatible(db, readOnly); err != nil {
		db.Close()
		rt.opsDB = nil
		return nil, err
	}
	return ops.NewStore(db, rt.Clock), nil
}

// assertSchemaCompatible refuses to write to a database that this binary does
// not fully understand, in either direction (§13.3).
func (rt *Runtime) assertSchemaCompatible(db *sqlite.DB, readOnly bool) error {
	migrations, err := sqlite.LoadMigrations(mycontext.Migrations, mycontext.MigrationsDirOps)
	if err != nil {
		return err
	}
	ctx, cancel := rt.Context()
	defer cancel()

	current, err := sqlite.CurrentVersion(ctx, db)
	if err != nil {
		return err
	}
	var target int64
	for _, m := range migrations {
		if m.Version > target {
			target = m.Version
		}
	}
	switch {
	case current > target:
		return protocol.Incompatible(
			"ops.db is at schema %d but this binary understands up to %d; upgrade the CLI",
			current, target)
	case current < target && !readOnly:
		return protocol.Incompatible(
			"ops.db is at schema %d and needs migrating to %d; run `mycontext schema migrate`",
			current, target)
	}
	return nil
}

// Close releases whatever the command opened.
func (rt *Runtime) Close() {
	if rt.opsDB != nil {
		rt.opsDB.Close()
		rt.opsDB = nil
	}
}

// WriteContext builds the audit/idempotency envelope for a mutation. When the
// caller supplies no request_id, one is generated: a human at a terminal
// should not have to invent one, while an agent must pass its own to retry
// safely.
func (rt *Runtime) WriteContext() ops.WriteContext {
	requestID := rt.Opts.RequestID
	if requestID == "" {
		requestID = system.NewID("req")
	}
	actorType := rt.Opts.Actor
	if actorType == "" {
		actorType = "user"
	}
	return ops.WriteContext{
		RequestID: requestID,
		Actor:     ops.Actor{Type: actorType, EntryPoint: "cli"},
		Reason:    rt.Opts.Reason,
		Confirmed: rt.Opts.Confirm,
		DryRun:    rt.Opts.DryRun,
	}
}

// SchemaVersions reports the schema level of each database that exists.
func (rt *Runtime) SchemaVersions() map[string]int64 {
	out := map[string]int64{}
	if rt.opsDB != nil {
		ctx, cancel := rt.Context()
		defer cancel()
		if v, err := sqlite.CurrentVersion(ctx, rt.opsDB); err == nil {
			out["ops"] = v
		}
	}
	return out
}

// ReadInput loads a JSON payload from --input, or from stdin when the value
// is "-". Complex payloads never go through shell quoting (§10.2).
func (rt *Runtime) ReadInput(target any) error {
	if rt.Opts.InputFile == "" {
		return protocol.BadInput("--input <file|-> is required for this command")
	}
	var raw []byte
	var err error
	if rt.Opts.InputFile == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(rt.Opts.InputFile)
	}
	if err != nil {
		return protocol.BadInput("cannot read input: %v", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return protocol.BadInput("input is not valid JSON: %v", err)
	}
	return nil
}

// AsAppError normalises any error into the protocol error type.
func AsAppError(err error) *protocol.AppError {
	var app *protocol.AppError
	if errors.As(err, &app) {
		return app
	}
	return &protocol.AppError{Code: protocol.CodeInternal, Message: err.Error()}
}
