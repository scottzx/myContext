package ops

import (
	"context"
	"database/sql"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/library"
	"github.com/scottzx/mycontext/internal/system"
)

// LibraryJournal is the ops.db implementation of library.Journal - the
// database half of the Library's recoverable commit (technical design §15).
//
// It lives here rather than in internal/library on purpose: that package
// defines the interface and stays free of any schema, which is what lets its
// crash-recovery matrix be tested against a fake without a database at all.
type LibraryJournal struct {
	db *sqlite.DB
}

// NewLibraryJournal binds the journal to an ops database.
func (s *Store) NewLibraryJournal() *LibraryJournal {
	return &LibraryJournal{db: s.db}
}

var _ library.Journal = (*LibraryJournal)(nil)

// MarkStaging records a staged package before the rename. It is written to be
// safely repeatable: a retry of the same commit updates the same row instead
// of failing on the primary key, because a crash between the write and the
// rename is a normal path here, not an exceptional one.
func (j *LibraryJournal) MarkStaging(ctx context.Context, rec library.JournalRecord) error {
	_, err := j.db.SQL().ExecContext(ctx, `
        INSERT INTO library_packages (id, request_id, storage_date, package_path,
                                      manifest_hash, state, captured_at)
        VALUES (?,?,?,?,?,'staging',?)
        ON CONFLICT(id) DO UPDATE SET
            manifest_hash = excluded.manifest_hash,
            storage_date  = excluded.storage_date`,
		rec.PackageID, rec.RequestID, rec.StorageDate,
		libraryPackagePath(rec.StorageDate, rec.PackageID),
		nullString(rec.ManifestHash), system.FormatTimestamp(rec.StagedAt))
	return sqlite.Classify(err)
}

// MarkSealed promotes a package once its bytes are in their final location.
// Re-sealing an already-sealed package is a no-op rather than an error: the
// recovery path re-runs the tail of a commit it cannot prove finished.
func (j *LibraryJournal) MarkSealed(ctx context.Context, packageID string, sealedAt time.Time) error {
	_, err := j.db.SQL().ExecContext(ctx, `
        UPDATE library_packages
           SET state = 'sealed', sealed_at = ?
         WHERE id = ? AND state <> 'sealed'`,
		system.FormatTimestamp(sealedAt), packageID)
	return sqlite.Classify(err)
}

// Lookup reads one package's record. A missing row is not an error - it is the
// "no record" half of the recovery matrix, and the caller must be able to tell
// that apart from a failure to read.
func (j *LibraryJournal) Lookup(ctx context.Context, packageID string) (library.JournalRecord, bool, error) {
	var (
		rec       library.JournalRecord
		hash      sql.NullString
		staged    string
		sealed    sql.NullString
		stateText string
	)
	err := j.db.SQL().QueryRowContext(ctx, `
        SELECT id, request_id, storage_date, manifest_hash, state, captured_at, sealed_at
          FROM library_packages WHERE id = ?`, packageID).
		Scan(&rec.PackageID, &rec.RequestID, &rec.StorageDate, &hash, &stateText, &staged, &sealed)
	if err == sql.ErrNoRows {
		return library.JournalRecord{}, false, nil
	}
	if err != nil {
		return library.JournalRecord{}, false, sqlite.Classify(err)
	}
	rec.ManifestHash = hash.String
	rec.State = library.State(stateText)
	if t, err := time.Parse(time.RFC3339, staged); err == nil {
		rec.StagedAt = t
	}
	if sealed.Valid {
		if t, err := time.Parse(time.RFC3339, sealed.String); err == nil {
			rec.SealedAt = t
		}
	}
	return rec, true, nil
}

// FindByRequestID is what makes capture idempotent: a replayed request finds
// the package it already produced instead of writing a second copy of the
// same bytes under a new id.
func (j *LibraryJournal) FindByRequestID(ctx context.Context, requestID string) (string, bool, error) {
	var id string
	err := j.db.SQL().QueryRowContext(ctx,
		`SELECT id FROM library_packages WHERE request_id = ?`, requestID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, sqlite.Classify(err)
	}
	return id, true, nil
}

// ListPackageIDs returns every known package. Verify needs it to find records
// whose files have vanished entirely - a case no filesystem scan can discover,
// because there is nothing left on disk to scan.
func (j *LibraryJournal) ListPackageIDs(ctx context.Context) ([]string, error) {
	rows, err := j.db.SQL().QueryContext(ctx, `SELECT id FROM library_packages ORDER BY captured_at`)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, id)
	}
	return out, sqlite.Classify(rows.Err())
}

// libraryPackagePath mirrors the on-disk layout: library/YYYY/MM/DD/cap_<id>.
// Stored so a reader of the database alone can find the bytes without having
// to re-derive the convention.
func libraryPackagePath(storageDate, packageID string) string {
	if len(storageDate) != 10 {
		return packageID
	}
	return storageDate[0:4] + "/" + storageDate[5:7] + "/" + storageDate[8:10] + "/" + packageID
}
