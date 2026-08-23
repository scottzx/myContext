package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
)

// Migration is one forward-only schema step. Down migrations are deliberately
// not supported; recovery is by restoring a snapshot (§13.3).
type Migration struct {
	Version  int64
	Name     string
	SQL      string
	Checksum string
}

// AppliedMigration records a step that already ran against a database.
type AppliedMigration struct {
	Version   int64  `json:"version"`
	Name      string `json:"name"`
	AppliedAt string `json:"applied_at"`
	AppliedBy string `json:"applied_by"`
	Checksum  string `json:"checksum"`
}

// Status is the answer to `mycontext schema status`.
type Status struct {
	Database       string             `json:"database"`
	Path           string             `json:"path"`
	Exists         bool               `json:"exists"`
	CurrentVersion int64              `json:"current_version"`
	TargetVersion  int64              `json:"target_version"`
	Pending        []PendingMigration `json:"pending"`
	Applied        []AppliedMigration `json:"applied,omitempty"`
	Compatible     bool               `json:"compatible"`
	Note           string             `json:"note,omitempty"`
}

type PendingMigration struct {
	Version int64  `json:"version"`
	Name    string `json:"name"`
}

// LoadMigrations reads NNN_name.sql files from an embedded directory. The
// numeric prefix is the version and must be unique and increasing.
func LoadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, protocol.Wrap(err, protocol.CodeInternal, "cannot read migrations")
	}
	var migrations []Migration
	seen := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 {
			return nil, protocol.Internal("migration %q must be named NNN_name.sql", e.Name())
		}
		version, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, protocol.Internal("migration %q has a non-numeric version", e.Name())
		}
		if prev, dup := seen[version]; dup {
			return nil, protocol.Internal("migration version %d used twice (%s, %s)", version, prev, e.Name())
		}
		seen[version] = e.Name()

		raw, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, protocol.Wrap(err, protocol.CodeInternal, "cannot read migration "+e.Name())
		}
		sum := sha256.Sum256(raw)
		migrations = append(migrations, Migration{
			Version:  version,
			Name:     parts[1],
			SQL:      string(raw),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

const migrationTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,
    checksum    TEXT NOT NULL,
    applied_at  TEXT NOT NULL,
    applied_by  TEXT NOT NULL
);`

// EnsureMigrationTable creates the bookkeeping table if absent.
func EnsureMigrationTable(ctx context.Context, db *DB) error {
	if _, err := db.sqlDB.ExecContext(ctx, migrationTableDDL); err != nil {
		return classify(err)
	}
	return nil
}

// CurrentVersion returns the highest applied migration, or 0 for a fresh
// database.
func CurrentVersion(ctx context.Context, db *DB) (int64, error) {
	var version sql.NullInt64
	err := db.sqlDB.QueryRowContext(ctx, `
        SELECT MAX(version) FROM schema_migrations`).Scan(&version)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, classify(err)
	}
	if !version.Valid {
		return 0, nil
	}
	return version.Int64, nil
}

// AppliedMigrations lists the migration history in order.
func AppliedMigrations(ctx context.Context, db *DB) ([]AppliedMigration, error) {
	rows, err := db.sqlDB.QueryContext(ctx, `
        SELECT version, name, applied_at, applied_by, checksum
          FROM schema_migrations ORDER BY version`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, classify(err)
	}
	defer rows.Close()

	var out []AppliedMigration
	for rows.Next() {
		var m AppliedMigration
		if err := rows.Scan(&m.Version, &m.Name, &m.AppliedAt, &m.AppliedBy, &m.Checksum); err != nil {
			return nil, classify(err)
		}
		out = append(out, m)
	}
	return out, classify(rows.Err())
}

// Plan reports which migrations would run, and refuses when the database is
// newer than this binary understands (§13.3).
func Plan(ctx context.Context, db *DB, migrations []Migration) (Status, error) {
	status := Status{Path: db.path, Exists: true, Compatible: true}
	if err := EnsureMigrationTable(ctx, db); err != nil {
		return status, err
	}
	applied, err := AppliedMigrations(ctx, db)
	if err != nil {
		return status, err
	}
	status.Applied = applied

	appliedByVersion := map[int64]AppliedMigration{}
	for _, a := range applied {
		appliedByVersion[a.Version] = a
		if a.Version > status.CurrentVersion {
			status.CurrentVersion = a.Version
		}
	}
	for _, m := range migrations {
		if m.Version > status.TargetVersion {
			status.TargetVersion = m.Version
		}
		known, ok := appliedByVersion[m.Version]
		if !ok {
			status.Pending = append(status.Pending, PendingMigration{Version: m.Version, Name: m.Name})
			continue
		}
		// A changed checksum means history was rewritten; refuse rather than
		// guess what the database actually contains.
		if known.Checksum != m.Checksum {
			status.Compatible = false
			status.Note = fmt.Sprintf("migration %d (%s) differs from the version already applied", m.Version, m.Name)
			return status, protocol.Integrity("%s", status.Note)
		}
	}
	if status.CurrentVersion > status.TargetVersion {
		status.Compatible = false
		status.Note = "database schema is newer than this binary; upgrade the CLI"
		return status, protocol.Incompatible("%s (database %d, binary %d)",
			status.Note, status.CurrentVersion, status.TargetVersion)
	}
	return status, nil
}

// Migrate applies pending migrations, each in its own transaction so a
// failure leaves the database at a known version.
func Migrate(ctx context.Context, db *DB, migrations []Migration, appliedBy string) (Status, error) {
	status, err := Plan(ctx, db, migrations)
	if err != nil {
		return status, err
	}
	pending := map[int64]bool{}
	for _, p := range status.Pending {
		pending[p.Version] = true
	}
	for _, m := range migrations {
		if !pending[m.Version] {
			continue
		}
		if err := applyOne(ctx, db, m, appliedBy); err != nil {
			return status, err
		}
	}
	return Plan(ctx, db, migrations)
}

func applyOne(ctx context.Context, db *DB, m Migration, appliedBy string) error {
	// Some DDL (notably foreign-key-sensitive rebuilds) misbehaves with
	// foreign_keys on; SQLite requires toggling it outside a transaction.
	if _, err := db.sqlDB.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return classify(err)
	}
	defer db.sqlDB.ExecContext(ctx, "PRAGMA foreign_keys = ON")

	err := db.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
		}
		_, err := tx.ExecContext(ctx, `
            INSERT INTO schema_migrations (version, name, checksum, applied_at, applied_by)
            VALUES (?, ?, ?, ?, ?)`,
			m.Version, m.Name, m.Checksum, time.Now().UTC().Format(time.RFC3339), appliedBy)
		return err
	})
	if err != nil {
		return err
	}
	// A failed foreign-key check after DDL means the migration produced
	// dangling references; surface it instead of leaving a corrupt schema.
	return CheckForeignKeys(ctx, db)
}

// CheckForeignKeys runs PRAGMA foreign_key_check and reports violations.
func CheckForeignKeys(ctx context.Context, db *DB) error {
	rows, err := db.sqlDB.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return classify(err)
	}
	defer rows.Close()
	var violations []string
	for rows.Next() {
		var table, parent sql.NullString
		var rowid, fkid sql.NullInt64
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return classify(err)
		}
		violations = append(violations, fmt.Sprintf("%s -> %s", table.String, parent.String))
	}
	if err := rows.Err(); err != nil {
		return classify(err)
	}
	if len(violations) > 0 {
		return protocol.Integrity("foreign key violations: %s", strings.Join(violations, ", "))
	}
	return nil
}

// IntegrityCheck runs PRAGMA integrity_check.
func IntegrityCheck(ctx context.Context, db *DB) error {
	var result string
	if err := db.sqlDB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return classify(err)
	}
	if result != "ok" {
		return protocol.Integrity("integrity_check reported: %s", result)
	}
	return nil
}
