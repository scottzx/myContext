package library_test

// Fault-injection tests for the §15.2 crash-recovery matrix. Each test
// constructs the exact on-disk + journal state a real crash at that point in
// the commit sequence would leave behind — either by interrupting a real
// Commit call through a fake Journal configured to fail at a specific step,
// or (where Commit's own sequencing can't produce the shape, e.g. a
// database restored from an older backup) by directly manipulating the
// filesystem/journal to the documented shape — and then asserts Verify's
// classification and recovery action.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/scottzx/mycontext/internal/library"
)

func findEntry(t *testing.T, report *library.Report, packageID string) library.Entry {
	t.Helper()
	for _, e := range report.Entries {
		if e.PackageID == packageID {
			return e
		}
	}
	t.Fatalf("no report entry for package %q", packageID)
	return library.Entry{}
}

// Row 1: 无记录 / staging 有 / final 无 -> orphaned.
func TestVerify_Row1_NoRecordStagingOnly_Orphaned(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hello")

	// Crash before MarkStaging (§15.1 step 5): bytes and manifest are
	// staged, but the journal never heard about it.
	journal.failMarkStaging = true
	_, err := library.Commit(context.Background(), layout, journal, clock, oneFileInput("req-row1", src))
	if err == nil {
		t.Fatalf("expected injected fault to fail the commit")
	}
	journal.failMarkStaging = false

	entries, err := os.ReadDir(layout.Staging())
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one staged package left behind, got %v (err=%v)", entries, err)
	}
	packageID := entries[0].Name()

	report, err := library.Verify(context.Background(), layout, journal, clock)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	entry := findEntry(t, report, packageID)
	if entry.Action != library.ActionOrphaned {
		t.Fatalf("Action = %q, want orphaned; detail=%q", entry.Action, entry.Detail)
	}

	wantPath := filepath.Join(layout.Library(), "_system", "orphaned", packageID)
	if entry.Path != wantPath {
		t.Fatalf("Path = %q, want %q", entry.Path, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("orphaned package not found at %q: %v", wantPath, err)
	}
	if _, err := os.Stat(filepath.Join(layout.Staging(), packageID)); !os.IsNotExist(err) {
		t.Fatalf("staging copy should have been moved, not left behind")
	}
}

// Row 2 (staging / staging 有 / final 无 -> validate then continue rename)
// is covered by TestVerify_Row2_StagingWithStagingDir_ResumesAndSeals in
// row2_internal_test.go. That boundary — after the journal is told
// "staging" but before the rename runs — sits between two plain filesystem
// calls with no Journal interaction in between, so it cannot be produced by
// failing a Journal method; it needs a white-box test that stops the real
// internal commit sequence at that exact point instead.

// Row 3: staging / staging 无 / final 有 -> validate manifest then complete
// sealed (no file move, only the journal write was missing).
func TestVerify_Row3_StagingWithFinalDir_CompletesSeal(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hello")

	// Crash after the rename (step 6) but before MarkSealed (step 7): the
	// real Commit sequence produces exactly this shape when MarkSealed
	// fails, since rename already happened by then.
	journal.failMarkSealed = true
	_, err := library.Commit(context.Background(), layout, journal, clock, oneFileInput("req-row3", src))
	if err == nil {
		t.Fatalf("expected injected fault to fail the commit")
	}
	journal.failMarkSealed = false

	ids, err := journal.ListPackageIDs(context.Background())
	if err != nil || len(ids) != 1 {
		t.Fatalf("expected exactly one journalled package, got %v (err=%v)", ids, err)
	}
	packageID := ids[0]
	rec, found, _ := journal.Lookup(context.Background(), packageID)
	if !found || rec.State != library.StateStaging {
		t.Fatalf("precondition failed: expected journal state staging, got found=%v state=%q", found, rec.State)
	}
	finalPath := filepath.Join(layout.Library(), "2026", "08", "21", packageID)
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("precondition failed: expected the rename to have already happened: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.Staging(), packageID)); !os.IsNotExist(err) {
		t.Fatalf("precondition failed: expected no staging copy left")
	}

	report, err := library.Verify(context.Background(), layout, journal, clock)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	entry := findEntry(t, report, packageID)
	if entry.Action != library.ActionResumedSealed {
		t.Fatalf("Action = %q, want resumed_sealed; detail=%q", entry.Action, entry.Detail)
	}
	if entry.Path != finalPath {
		t.Fatalf("Path = %q, want %q", entry.Path, finalPath)
	}
	recAfter, found, err := journal.Lookup(context.Background(), packageID)
	if err != nil || !found || recAfter.State != library.StateSealed {
		t.Fatalf("journal should report sealed after Verify, got state=%q found=%v err=%v", recAfter.State, found, err)
	}
}

// Row 4: sealed / staging 无 / final 有 -> normal, nothing to do.
func TestVerify_Row4_SealedNormal_ReportsOK(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hello")

	res, err := library.Commit(context.Background(), layout, journal, clock, oneFileInput("req-row4", src))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	report, err := library.Verify(context.Background(), layout, journal, clock)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	entry := findEntry(t, report, res.PackageID)
	if entry.Action != library.ActionOK {
		t.Fatalf("Action = %q, want ok; detail=%q", entry.Action, entry.Detail)
	}
	if entry.Path != res.FinalPath {
		t.Fatalf("Path = %q, want %q", entry.Path, res.FinalPath)
	}

	// Verify must not mutate anything on the healthy path.
	if _, err := os.Stat(res.FinalPath); err != nil {
		t.Fatalf("final path should be untouched: %v", err)
	}
}

// Row 5: sealed / staging 无 / final 无 -> high-priority integrity error;
// never fabricate the missing file.
func TestVerify_Row5_SealedButFilesGone_IntegrityError(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hello")

	res, err := library.Commit(context.Background(), layout, journal, clock, oneFileInput("req-row5", src))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Simulate the final copy being lost after sealing (disk failure, user
	// deleting it outside the system, etc.) while the journal still
	// believes it is sealed.
	if err := os.RemoveAll(res.FinalPath); err != nil {
		t.Fatalf("remove final path: %v", err)
	}

	report, err := library.Verify(context.Background(), layout, journal, clock)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	entry := findEntry(t, report, res.PackageID)
	if entry.Action != library.ActionIntegrityError {
		t.Fatalf("Action = %q, want integrity_error; detail=%q", entry.Action, entry.Detail)
	}
	if entry.Path != "" {
		t.Fatalf("integrity error must not report a fabricated Path, got %q", entry.Path)
	}
	if _, err := os.Stat(res.FinalPath); !os.IsNotExist(err) {
		t.Fatalf("Verify must never fabricate the missing file")
	}
}

// Row 6: 无记录 / staging 无 / final 有 -> rebuild base index from manifest,
// pending confirmation. Simulates context.db being restored from a backup
// taken before this package was journalled: the sealed files survive, the
// journal record does not.
func TestVerify_Row6_NoRecordFinalOnly_PendingAdoption(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hello")

	res, err := library.Commit(context.Background(), layout, journal, clock, oneFileInput("req-row6", src))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	journal.forget(res.PackageID, "req-row6")

	report, err := library.Verify(context.Background(), layout, journal, clock)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	entry := findEntry(t, report, res.PackageID)
	if entry.Action != library.ActionPendingAdoption {
		t.Fatalf("Action = %q, want pending_adoption; detail=%q", entry.Action, entry.Detail)
	}
	if entry.Path != res.FinalPath {
		t.Fatalf("Path = %q, want %q (row 6 must leave sealed files exactly where they are)", entry.Path, res.FinalPath)
	}
	if entry.Manifest == nil || entry.Manifest.PackageID != res.PackageID {
		t.Fatalf("expected manifest to be read back for pending adoption, got %+v", entry.Manifest)
	}
	// Nothing was moved or deleted.
	if _, err := os.Stat(res.FinalPath); err != nil {
		t.Fatalf("sealed package must be left in place: %v", err)
	}
}

// Extra required case: a corrupted manifest hash on an otherwise sealed
// package must land in quarantine, not be reported OK or silently trusted.
func TestVerify_CorruptedManifestHash_Quarantined(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hello")

	res, err := library.Commit(context.Background(), layout, journal, clock, oneFileInput("req-corrupt", src))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Tamper with the sealed manifest.json after the fact (e.g. bit rot or
	// a manual edit). The journal still remembers the original hash.
	manifestPath := filepath.Join(res.FinalPath, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	tampered := append(append([]byte{}, raw...), []byte("\n// tampered\n")...)
	if err := os.WriteFile(manifestPath, tampered, 0o600); err != nil {
		t.Fatalf("tamper manifest: %v", err)
	}

	report, err := library.Verify(context.Background(), layout, journal, clock)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	entry := findEntry(t, report, res.PackageID)
	if entry.Action != library.ActionQuarantined {
		t.Fatalf("Action = %q, want quarantined; detail=%q", entry.Action, entry.Detail)
	}
	wantPath := filepath.Join(layout.Library(), "_system", "quarantine", res.PackageID)
	if entry.Path != wantPath {
		t.Fatalf("Path = %q, want %q", entry.Path, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("quarantined package not found at %q: %v", wantPath, err)
	}
	if _, err := os.Stat(res.FinalPath); !os.IsNotExist(err) {
		t.Fatalf("original sealed location should be gone once quarantined")
	}
}

// Extra required case: a corrupted asset (bytes no longer match the
// manifest's recorded hash) while still in staging must also be quarantined
// rather than blindly sealed.
func TestVerify_CorruptedAssetInStaging_Quarantined(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hello")

	journal.failMarkSealed = true
	_, err := library.Commit(context.Background(), layout, journal, clock, oneFileInput("req-corrupt-asset", src))
	if err == nil {
		t.Fatalf("expected injected fault to fail the commit")
	}
	journal.failMarkSealed = false

	ids, err := journal.ListPackageIDs(context.Background())
	if err != nil || len(ids) != 1 {
		t.Fatalf("expected exactly one journalled package, got %v (err=%v)", ids, err)
	}
	packageID := ids[0]
	finalPath := filepath.Join(layout.Library(), "2026", "08", "21", packageID)
	stagingPath := filepath.Join(layout.Staging(), packageID)
	if err := os.Rename(finalPath, stagingPath); err != nil {
		t.Fatalf("simulate pre-rename crash: %v", err)
	}
	// Corrupt the asset bytes without touching the manifest.
	assetPath := filepath.Join(stagingPath, "original", "note.md")
	if err := os.WriteFile(assetPath, []byte("corrupted!"), 0o600); err != nil {
		t.Fatalf("corrupt asset: %v", err)
	}

	report, err := library.Verify(context.Background(), layout, journal, clock)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	entry := findEntry(t, report, packageID)
	if entry.Action != library.ActionQuarantined {
		t.Fatalf("Action = %q, want quarantined; detail=%q", entry.Action, entry.Detail)
	}
	if _, err := os.Stat(filepath.Join(layout.Library(), "_system", "quarantine", packageID)); err != nil {
		t.Fatalf("expected package to be quarantined: %v", err)
	}
}

func TestVerify_NoOpOnEmptyRoot(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}

	report, err := library.Verify(context.Background(), layout, journal, clock)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(report.Entries) != 0 {
		t.Fatalf("expected no entries on an empty root, got %d", len(report.Entries))
	}
}
