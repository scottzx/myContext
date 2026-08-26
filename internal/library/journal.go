package library

import (
	"context"
	"time"
)

// State is a capture package's position in the commit sequence (§15.1).
type State string

const (
	// StateStaging means steps 2-5 completed: bytes and manifest are on
	// disk under system/staging/<id> and the journal knows about them, but
	// the package has not yet been renamed into the Library.
	StateStaging State = "staging"

	// StateSealed means step 7 completed: the package has been renamed
	// into library/YYYY/MM/DD/cap_<id> and is never appended to or
	// rewritten again.
	StateSealed State = "sealed"
)

// JournalRecord is the database-side state of one capture package. It
// carries StorageDate and ManifestHash because recovery (§15.2) needs both
// without re-deriving them: StorageDate is fixed at capture time and picks
// out the deterministic final path, and ManifestHash detects a manifest.json
// that was altered after the fact.
type JournalRecord struct {
	PackageID    string
	RequestID    string
	State        State
	StorageDate  string // YYYY-MM-DD, immutable once set (B+ design §9.2)
	ManifestHash string // sha256 of manifest.json's bytes at MarkStaging time
	StagedAt     time.Time
	SealedAt     time.Time // zero until MarkSealed
}

// Journal is the database half of the recoverable commit (§15.1 steps 5 and
// 7). This package never touches SQL directly: a caller backed by
// context.db (or a fake, in tests) implements this interface, which is what
// lets file-transaction logic be developed and tested before the ops/context
// schema exists.
//
// Every method must be safe to call more than once for the same
// PackageID/RequestID: Commit and Verify both retry through this interface
// after a crash, and a second call must not be treated as an error or
// produce a second logical record.
type Journal interface {
	// MarkStaging records that a package's bytes and manifest are staged
	// and known-good (§15.1 step 5), before the atomic rename into the
	// Library. Calling it again for the same PackageID with the same
	// fields must succeed without creating a duplicate record.
	MarkStaging(ctx context.Context, rec JournalRecord) error

	// MarkSealed records that a package has been renamed into its final
	// Library location (§15.1 step 7). Calling it again for an
	// already-sealed PackageID must succeed as a no-op.
	MarkSealed(ctx context.Context, packageID string, sealedAt time.Time) error

	// Lookup returns the current record for a package, or
	// (JournalRecord{}, false, nil) if the journal has no record of it at
	// all (the "无记录" rows of §15.2's matrix).
	Lookup(ctx context.Context, packageID string) (JournalRecord, bool, error)

	// FindByRequestID returns the package a RequestID previously committed
	// (in any state), or (\"\", false, nil) if RequestID has never been
	// used. It is what makes Commit idempotent.
	FindByRequestID(ctx context.Context, requestID string) (packageID string, found bool, err error)

	// ListPackageIDs returns every package the journal currently knows
	// about, regardless of state. Verify needs this to find records whose
	// files are missing on disk entirely (§15.2's "sealed, 无 staging, 无
	// final" row) — a case a filesystem scan alone can never discover.
	ListPackageIDs(ctx context.Context) ([]string, error)
}
