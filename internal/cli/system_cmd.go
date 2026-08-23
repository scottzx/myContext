package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	mycontext "github.com/scottzx/mycontext"
	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// ---------------------------------------------------------------------------
// mycontext init
// ---------------------------------------------------------------------------

func newInitCmd(opts *GlobalOptions) *cobra.Command {
	var migrate bool
	cmd := &cobra.Command{
		Use:         "init",
		Short:       "Create or report a data instance",
		Annotations: map[string]string{"op": "system.init", "write": "true"},
		Long: "Creates the directory layout and config for a data root.\n" +
			"init is idempotent: run against an existing instance it reports the\n" +
			"current state and changes nothing.",
	}
	cmd.Flags().BoolVar(&migrate, "migrate", true, "apply pending schema migrations after creating the instance")
	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "system.init"
		ctx, cancel := rt.Context()
		defer cancel()

		existed := rt.Config.InstanceID != ""
		if !existed {
			for _, dir := range rt.Layout.Dirs() {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return rt.EmitError(command, protocol.Wrap(err, protocol.CodeIntegrity, "cannot create "+dir))
				}
			}
			cfg := system.DefaultConfig(system.NewID("inst"), Version)
			if err := system.SaveConfig(rt.Layout, cfg); err != nil {
				return rt.EmitError(command, err)
			}
			rt.Config = cfg
			rt.applyTimezone(cfg.Timezone)
		}

		result := map[string]any{
			"root":            rt.Root,
			"instance_id":     rt.Config.InstanceID,
			"already_existed": existed,
			"timezone":        rt.Config.Timezone,
		}

		if migrate {
			status, err := migrateOps(ctx, rt)
			if err != nil {
				return rt.EmitError(command, err)
			}
			result["schema"] = status
		}

		return rt.EmitData(command, result, func(w io.Writer, data any) error {
			if existed {
				fmt.Fprintf(w, "instance already exists at %s\n", rt.Root)
			} else {
				fmt.Fprintf(w, "created instance at %s\n", rt.Root)
			}
			fmt.Fprintf(w, "instance_id: %s\ntimezone:    %s\n", rt.Config.InstanceID, rt.Config.Timezone)
			if s, ok := result["schema"].(sqlite.Status); ok {
				fmt.Fprintf(w, "ops schema:  %d\n", s.CurrentVersion)
			}
			return nil
		})
	})
	return cmd
}

// migrateOps opens ops.db for writing and brings it to the latest schema.
func migrateOps(ctx context.Context, rt *Runtime) (sqlite.Status, error) {
	migrations, err := sqlite.LoadMigrations(mycontext.Migrations, mycontext.MigrationsDirOps)
	if err != nil {
		return sqlite.Status{}, err
	}
	db, err := sqlite.Open(rt.Layout.OpsDB(), sqlite.Options{
		BusyTimeout: rt.Config.BusyTimeout,
		JournalMode: rt.Config.JournalMode,
	})
	if err != nil {
		return sqlite.Status{}, err
	}
	defer db.Close()

	status, err := sqlite.Migrate(ctx, db, migrations, Version)
	status.Database = "ops"
	return status, err
}

// ---------------------------------------------------------------------------
// mycontext version
// ---------------------------------------------------------------------------

func newVersionCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "version",
		Short:       "Report CLI, build and compatible schema versions",
		Annotations: map[string]string{"op": "system.version"},
	}
	cmd.RunE = run(opts, func(rt *Runtime) int {
		migrations, err := sqlite.LoadMigrations(mycontext.Migrations, mycontext.MigrationsDirOps)
		if err != nil {
			return rt.EmitError("system.version", err)
		}
		var opsTarget int64
		for _, m := range migrations {
			if m.Version > opsTarget {
				opsTarget = m.Version
			}
		}
		data := map[string]any{
			"cli_version":        Version,
			"commit":             Commit,
			"build_time":         BuildTime,
			"go_version":         runtime.Version(),
			"build_target":       runtime.GOOS + "/" + runtime.GOARCH,
			"protocol":           protocol.Version,
			"schema_targets":     map[string]int64{"ops": opsTarget},
			"projection_version": ops.ProjectionVersion,
		}
		return rt.EmitData("system.version", data, func(w io.Writer, _ any) error {
			fmt.Fprintf(w, "mycontext %s (%s)\n", Version, Commit)
			fmt.Fprintf(w, "built:    %s\n", BuildTime)
			fmt.Fprintf(w, "target:   %s, %s\n", runtime.GOOS+"/"+runtime.GOARCH, runtime.Version())
			fmt.Fprintf(w, "protocol: %s\n", protocol.Version)
			fmt.Fprintf(w, "ops schema target: %d\n", opsTarget)
			return nil
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext doctor
// ---------------------------------------------------------------------------

// Check is one diagnostic result (§21).
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail | repairable
	Detail string `json:"detail,omitempty"`
}

func newDoctorCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "doctor",
		Short:       "Check this instance and report pass/warn/fail per capability",
		Annotations: map[string]string{"op": "system.doctor"},
	}
	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "system.doctor"
		ctx, cancel := rt.Context()
		defer cancel()

		checks := []Check{}
		add := func(name, status, detail string) {
			checks = append(checks, Check{Name: name, Status: status, Detail: detail})
		}

		add("root.resolved", "pass", rt.Root)

		if rt.Config.InstanceID == "" {
			add("instance.config", "fail", "no config.json; run `mycontext init`")
			return emitChecks(rt, command, checks)
		}
		add("instance.config", "pass", "instance "+rt.Config.InstanceID)

		if _, err := time.LoadLocation(rt.Config.Timezone); err != nil {
			add("instance.timezone", "warn", fmt.Sprintf("unknown timezone %q", rt.Config.Timezone))
		} else {
			add("instance.timezone", "pass", rt.Config.Timezone)
		}

		for _, dir := range rt.Layout.Dirs() {
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				add("layout."+filepath.Base(dir), "repairable", "missing: "+dir)
			}
		}
		if !hasStatus(checks, "repairable") {
			add("layout.directories", "pass", "all present")
		}

		// Writability decides whether any mutation can succeed at all.
		probe := filepath.Join(rt.Layout.System(), ".write-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
			add("root.writable", "fail", err.Error())
		} else {
			os.Remove(probe)
			add("root.writable", "pass", "")
		}

		if _, err := os.Stat(rt.Layout.OpsDB()); err != nil {
			add("ops.database", "warn", "ops.db does not exist yet")
		} else {
			db, err := sqlite.Open(rt.Layout.OpsDB(), sqlite.Options{
				ReadOnly: true, BusyTimeout: rt.Config.BusyTimeout,
			})
			if err != nil {
				add("ops.database", "fail", err.Error())
			} else {
				defer db.Close()
				add("ops.foreign_keys", "pass", "enabled")

				if err := sqlite.IntegrityCheck(ctx, db); err != nil {
					add("ops.integrity", "fail", err.Error())
				} else {
					add("ops.integrity", "pass", "ok")
				}
				if err := sqlite.CheckForeignKeys(ctx, db); err != nil {
					add("ops.foreign_key_check", "fail", err.Error())
				} else {
					add("ops.foreign_key_check", "pass", "no violations")
				}

				migrations, err := sqlite.LoadMigrations(mycontext.Migrations, mycontext.MigrationsDirOps)
				if err != nil {
					add("ops.schema", "fail", err.Error())
				} else if status, err := sqlite.Plan(ctx, db, migrations); err != nil {
					add("ops.schema", "fail", err.Error())
				} else if len(status.Pending) > 0 {
					add("ops.schema", "repairable",
						fmt.Sprintf("at %d, %d migration(s) pending; run `mycontext schema migrate`",
							status.CurrentVersion, len(status.Pending)))
				} else {
					add("ops.schema", "pass", fmt.Sprintf("schema %d", status.CurrentVersion))
				}

				store := ops.NewStore(db, rt.Clock)
				if issues, err := store.QualityIssues(ctx); err != nil {
					add("ops.data_quality", "warn", err.Error())
				} else if len(issues) > 0 {
					add("ops.data_quality", "warn",
						fmt.Sprintf("%d data quality issue(s); see `mycontext ops status`", len(issues)))
				} else {
					add("ops.data_quality", "pass", "no issues")
				}
			}
		}

		add("platform", "pass", runtime.GOOS+"/"+runtime.GOARCH)
		return emitChecks(rt, command, checks)
	})
	return cmd
}

func emitChecks(rt *Runtime, command string, checks []Check) int {
	code := rt.EmitData(command, map[string]any{
		"checks":  checks,
		"summary": summarise(checks),
	}, func(w io.Writer, _ any) error {
		rows := make([][]string, 0, len(checks))
		for _, c := range checks {
			rows = append(rows, []string{c.Status, c.Name, c.Detail})
		}
		return Table(w, []string{"STATUS", "CHECK", "DETAIL"}, rows)
	})
	// A failed capability must not look like success to a script.
	if hasStatus(checks, "fail") {
		return protocol.ExitIntegrity
	}
	return code
}

func summarise(checks []Check) map[string]int {
	out := map[string]int{"pass": 0, "warn": 0, "fail": 0, "repairable": 0}
	for _, c := range checks {
		out[c.Status]++
	}
	return out
}

func hasStatus(checks []Check, status string) bool {
	for _, c := range checks {
		if c.Status == status {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// mycontext schema status|plan|migrate
// ---------------------------------------------------------------------------

func newSchemaCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "schema", Short: "Inspect and apply database migrations"}

	statusCmd := &cobra.Command{Use: "status", Short: "Report current and target schema versions"}
	statusCmd.RunE = run(opts, func(rt *Runtime) int { return schemaReport(rt, "schema.status", false) })

	planCmd := &cobra.Command{
		Use:   "plan",
		Short: "Show which migrations would run, without applying them",
	}
	planCmd.RunE = run(opts, func(rt *Runtime) int { return schemaReport(rt, "schema.plan", false) })

	migrateCmd := &cobra.Command{
		Use:         "migrate",
		Short:       "Apply pending migrations after taking a snapshot",
		Annotations: map[string]string{"write": "true"},
	}
	migrateCmd.RunE = run(opts, func(rt *Runtime) int { return schemaReport(rt, "schema.migrate", true) })

	cmd.AddCommand(statusCmd, planCmd, migrateCmd)
	return cmd
}

func schemaReport(rt *Runtime, command string, apply bool) int {
	ctx, cancel := rt.Context()
	defer cancel()

	if rt.Config.InstanceID == "" {
		return rt.EmitError(command, protocol.NotFound("no mycontext instance at %s (run `mycontext init`)", rt.Root))
	}
	migrations, err := sqlite.LoadMigrations(mycontext.Migrations, mycontext.MigrationsDirOps)
	if err != nil {
		return rt.EmitError(command, err)
	}

	// A migration mutates the schema, so take a restorable snapshot first
	// (§13.3). Reads open the database read-only.
	var snapshotPath string
	if apply {
		if _, statErr := os.Stat(rt.Layout.OpsDB()); statErr == nil {
			snapshotPath, err = createSnapshot(ctx, rt, "pre-migrate")
			if err != nil {
				return rt.EmitError(command, err)
			}
		}
	}

	db, err := sqlite.Open(rt.Layout.OpsDB(), sqlite.Options{
		ReadOnly:    !apply,
		BusyTimeout: rt.Config.BusyTimeout,
		JournalMode: rt.Config.JournalMode,
	})
	if err != nil {
		return rt.EmitError(command, err)
	}
	defer db.Close()

	var status sqlite.Status
	if apply {
		status, err = sqlite.Migrate(ctx, db, migrations, Version)
	} else {
		status, err = sqlite.Plan(ctx, db, migrations)
	}
	status.Database = "ops"
	if err != nil {
		return rt.EmitError(command, err)
	}

	data := map[string]any{"ops": status}
	if snapshotPath != "" {
		data["snapshot"] = snapshotPath
	}
	return rt.EmitData(command, data, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "ops.db  current=%d target=%d pending=%d\n",
			status.CurrentVersion, status.TargetVersion, len(status.Pending))
		for _, p := range status.Pending {
			fmt.Fprintf(w, "  pending %03d %s\n", p.Version, p.Name)
		}
		if snapshotPath != "" {
			fmt.Fprintf(w, "snapshot: %s\n", snapshotPath)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// mycontext backup create|verify
// ---------------------------------------------------------------------------

func newBackupCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Create and verify consistent database snapshots"}

	createCmd := &cobra.Command{
		Use:         "create",
		Short:       "Write a consistent snapshot of ops.db",
		Annotations: map[string]string{"write": "true"},
		Long: "Uses SQLite's VACUUM INTO so the snapshot is internally consistent\n" +
			"even if something is writing; copying a live database file is not safe.",
	}
	createCmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "backup.create"
		ctx, cancel := rt.Context()
		defer cancel()
		path, err := createSnapshot(ctx, rt, "manual")
		if err != nil {
			return rt.EmitError(command, err)
		}
		info, _ := os.Stat(path)
		var size int64
		if info != nil {
			size = info.Size()
		}
		return rt.EmitData(command, map[string]any{"path": path, "size_bytes": size},
			func(w io.Writer, _ any) error {
				fmt.Fprintf(w, "snapshot: %s (%d bytes)\n", path, size)
				return nil
			})
	})

	verifyCmd := &cobra.Command{
		Use:   "verify <snapshot-path>",
		Short: "Open a snapshot read-only and run integrity checks",
		Args:  cobra.ExactArgs(1),
	}
	verifyCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "backup.verify"
		ctx, cancel := rt.Context()
		defer cancel()
		path := args[0]
		db, err := sqlite.Open(path, sqlite.Options{ReadOnly: true})
		if err != nil {
			return rt.EmitError(command, err)
		}
		defer db.Close()
		if err := sqlite.IntegrityCheck(ctx, db); err != nil {
			return rt.EmitError(command, err)
		}
		if err := sqlite.CheckForeignKeys(ctx, db); err != nil {
			return rt.EmitError(command, err)
		}
		version, err := sqlite.CurrentVersion(ctx, db)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, map[string]any{
			"path": path, "integrity": "ok", "schema_version": version,
		}, func(w io.Writer, _ any) error {
			fmt.Fprintf(w, "%s: integrity ok, schema %d\n", path, version)
			return nil
		})
	})
	cmd.AddCommand(createCmd, verifyCmd)
	return cmd
}

// createSnapshot writes a consistent copy of ops.db into snapshots/.
func createSnapshot(ctx context.Context, rt *Runtime, label string) (string, error) {
	if err := os.MkdirAll(rt.Layout.Snapshots(), 0o700); err != nil {
		return "", protocol.Wrap(err, protocol.CodeIntegrity, "cannot create snapshots directory")
	}
	name := fmt.Sprintf("ops-%s-%s.db", time.Now().UTC().Format("20060102T150405Z"), label)
	target := filepath.Join(rt.Layout.Snapshots(), name)

	db, err := sqlite.Open(rt.Layout.OpsDB(), sqlite.Options{
		ReadOnly: true, BusyTimeout: rt.Config.BusyTimeout,
	})
	if err != nil {
		return "", err
	}
	defer db.Close()

	if _, err := db.SQL().ExecContext(ctx, "VACUUM INTO ?", target); err != nil {
		return "", sqlite.Classify(err)
	}
	return target, nil
}
