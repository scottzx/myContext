package ops

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// patch accumulates a partial UPDATE. Only fields the caller actually set are
// included, so omitting a field never clears it; passing an empty string does.
type patch struct {
	columns []string
	args    []any
}

func newPatch() *patch { return &patch{} }

func (p *patch) empty() bool { return len(p.columns) == 0 }

// str stages a nullable text column from an optional input field.
func (p *patch) str(column string, value *string) {
	if value == nil {
		return
	}
	p.columns = append(p.columns, column+" = ?")
	p.args = append(p.args, nullString(*value))
}

// num stages a nullable integer column; a non-positive value clears it.
func (p *patch) num(column string, value *int) {
	if value == nil {
		return
	}
	p.columns = append(p.columns, column+" = ?")
	p.args = append(p.args, nullInt(*value))
}

// flt stages a nullable real column. Unlike num, 0 is a legitimate value -
// a metric that currently reads zero is not the same as no metric.
func (p *patch) flt(column string, value *float64) {
	if value == nil {
		return
	}
	p.columns = append(p.columns, column+" = ?")
	p.args = append(p.args, *value)
}

// raw stages a literal value the caller has already validated.
func (p *patch) raw(column string, value any) {
	p.columns = append(p.columns, column+" = ?")
	p.args = append(p.args, value)
}

// applyToTask writes the patch guarded by the expected version, bumping the
// version and updated_at in the same statement.
func (p *patch) applyToTask(ctx context.Context, tx *sql.Tx, id string, expected int64, now time.Time) error {
	return p.apply(ctx, tx, "tasks", "task", id, expected, now)
}

func (p *patch) applyToProject(ctx context.Context, tx *sql.Tx, id string, expected int64, now time.Time) error {
	return p.apply(ctx, tx, "projects", "project", id, expected, now)
}

func (p *patch) applyToCycle(ctx context.Context, tx *sql.Tx, id string, expected int64, now time.Time) error {
	return p.apply(ctx, tx, "cycles", "cycle", id, expected, now)
}

func (p *patch) applyToMilestone(ctx context.Context, tx *sql.Tx, id string, expected int64, now time.Time) error {
	return p.apply(ctx, tx, "milestones", "milestone", id, expected, now)
}

func (p *patch) applyToKeyResult(ctx context.Context, tx *sql.Tx, id string, expected int64, now time.Time) error {
	return p.apply(ctx, tx, "key_results", "key result", id, expected, now)
}

func (p *patch) apply(ctx context.Context, tx *sql.Tx, table, label, id string, expected int64, now time.Time) error {
	if p.empty() {
		return protocol.BadInput("no fields to update")
	}
	// Column names come from this package only; values are always bound.
	query := "UPDATE " + table + " SET " + strings.Join(p.columns, ", ") +
		", updated_at = ?, version = version + 1 WHERE id = ? AND version = ?"
	args := append(append([]any{}, p.args...), system.FormatTimestamp(now), id, expected)

	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return protocol.VersionConflict(label, expected, -1)
	}
	return nil
}
