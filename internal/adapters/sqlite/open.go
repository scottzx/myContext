// Package sqlite adapts SQLite to the repository ports. The driver type never
// escapes this package (§7.1), so the domain stays swappable.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/scottzx/minis-context/internal/protocol"
	_ "modernc.org/sqlite"
)

// Options configure one short-lived connection. A CLI invocation opens only
// the databases it needs and closes them before exiting (§9).
type Options struct {
	ReadOnly    bool
	BusyTimeout int // milliseconds
	JournalMode string
}

func defaulted(o Options) Options {
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = 5000
	}
	if o.JournalMode == "" {
		o.JournalMode = "wal"
	}
	return o
}

// DB is a handle to one SQLite database file.
type DB struct {
	sqlDB    *sql.DB
	path     string
	readOnly bool
}

// Open connects to path. Read-only opens fail if the file is missing rather
// than silently creating an empty database.
func Open(path string, opts Options) (*DB, error) {
	opts = defaulted(opts)

	if opts.ReadOnly {
		if _, err := os.Stat(path); err != nil {
			return nil, protocol.NotFound("database not found: %s", path)
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, protocol.Wrap(err, protocol.CodeIntegrity, "cannot create data directory")
	}

	dsn := buildDSN(path, opts)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, protocol.Wrap(err, protocol.CodeIntegrity, "cannot open database")
	}
	// A short-lived process gains nothing from a connection pool, and a single
	// connection keeps PRAGMA state and write locks predictable.
	sqlDB.SetMaxOpenConns(1)

	db := &DB{sqlDB: sqlDB, path: path, readOnly: opts.ReadOnly}
	if err := db.applyPragmas(opts); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func buildDSN(path string, opts Options) string {
	params := url.Values{}
	params.Set("_pragma", fmt.Sprintf("busy_timeout(%d)", opts.BusyTimeout))
	if opts.ReadOnly {
		params.Set("mode", "ro")
	}
	return "file:" + path + "?" + strings.ReplaceAll(params.Encode(), "%28", "(")
}

func (db *DB) applyPragmas(opts Options) error {
	ctx := context.Background()
	// foreign_keys is mandatory on every connection (§13.1).
	pragmas := []string{
		fmt.Sprintf("PRAGMA busy_timeout = %d", opts.BusyTimeout),
		"PRAGMA foreign_keys = ON",
	}
	if !opts.ReadOnly {
		pragmas = append(pragmas, "PRAGMA journal_mode = "+opts.JournalMode)
	}
	for _, p := range pragmas {
		if _, err := db.sqlDB.ExecContext(ctx, p); err != nil {
			return protocol.Wrap(err, protocol.CodeIntegrity, "cannot apply "+p)
		}
	}
	var fk int
	if err := db.sqlDB.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return protocol.Wrap(err, protocol.CodeIntegrity, "cannot verify foreign_keys")
	}
	if fk != 1 {
		return protocol.Integrity("foreign_keys could not be enabled on %s", db.path)
	}
	return nil
}

func (db *DB) Close() error { return db.sqlDB.Close() }
func (db *DB) Path() string { return db.path }

// SQL exposes the standard handle for repositories inside this package tree.
func (db *DB) SQL() *sql.DB { return db.sqlDB }

// InTx runs fn inside one short write transaction and commits only if fn
// returns nil. Write transactions must stay small (§13.1).
func (db *DB) InTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if db.readOnly {
		return protocol.Integrity("cannot write: %s is open read-only", db.path)
	}
	tx, err := db.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return classify(err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return classify(err)
	}
	if err := tx.Commit(); err != nil {
		return classify(err)
	}
	return nil
}

// classify turns driver-level failures into protocol errors so that callers
// never inspect driver types.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var app *protocol.AppError
	if errors.As(err, &app) {
		return app
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "database is locked"), strings.Contains(msg, "busy"):
		return protocol.Busy(err)
	case strings.Contains(msg, "foreign key constraint"):
		return protocol.BadInput("referenced object does not exist: %v", err)
	case strings.Contains(msg, "unique constraint"):
		return protocol.BadInput("record already exists: %v", err)
	case strings.Contains(msg, "constraint"):
		return protocol.BadInput("value violates a schema constraint: %v", err)
	}
	return protocol.Wrap(err, protocol.CodeIntegrity, "database error")
}

// Classify is exported for repositories in sibling files.
func Classify(err error) error { return classify(err) }
