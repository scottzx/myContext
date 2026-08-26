package library

// White-box test for §15.2 row 2 (staging / staging dir present / final
// absent). This is a package library (not library_test) file because
// producing this exact shape requires stopping the real commit sequence
// between recordStaging (step 5, a Journal call) and renameToFinal (step 6,
// a plain filesystem call): there is no Journal method to fail in order to
// interrupt at that boundary, so the test calls the unexported internal
// steps directly instead of simulating the crash by rearranging files after
// the fact.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scottzx/mycontext/internal/system"
)

// minimalFakeJournal is a small in-memory Journal, duplicated from the
// black-box tests' fakeJournal because that one lives in package
// library_test and unexported test types cannot cross the package
// boundary.
type minimalFakeJournal struct {
	byPackage map[string]JournalRecord
	byRequest map[string]string
}

func newMinimalFakeJournal() *minimalFakeJournal {
	return &minimalFakeJournal{byPackage: map[string]JournalRecord{}, byRequest: map[string]string{}}
}

func (f *minimalFakeJournal) MarkStaging(ctx context.Context, rec JournalRecord) error {
	f.byPackage[rec.PackageID] = rec
	f.byRequest[rec.RequestID] = rec.PackageID
	return nil
}

func (f *minimalFakeJournal) MarkSealed(ctx context.Context, packageID string, sealedAt time.Time) error {
	rec := f.byPackage[packageID]
	rec.State = StateSealed
	rec.SealedAt = sealedAt
	f.byPackage[packageID] = rec
	return nil
}

func (f *minimalFakeJournal) Lookup(ctx context.Context, packageID string) (JournalRecord, bool, error) {
	rec, ok := f.byPackage[packageID]
	return rec, ok, nil
}

func (f *minimalFakeJournal) FindByRequestID(ctx context.Context, requestID string) (string, bool, error) {
	id, ok := f.byRequest[requestID]
	return id, ok, nil
}

func (f *minimalFakeJournal) ListPackageIDs(ctx context.Context) ([]string, error) {
	ids := make([]string, 0, len(f.byPackage))
	for id := range f.byPackage {
		ids = append(ids, id)
	}
	return ids, nil
}

func TestVerify_Row2_StagingWithStagingDir_ResumesAndSeals(t *testing.T) {
	root := t.TempDir()
	layout := system.NewLayout(root)
	for _, dir := range layout.Dirs() {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	journal := newMinimalFakeJournal()
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "note.md")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	in := CaptureInput{
		RequestID:  "req-row2",
		CapturedAt: now,
		Files:      []InputFile{{SourcePath: srcPath, Role: RoleOriginal, Name: "note.md"}},
	}

	packageID := system.NewID("cap")
	storageDate, err := deriveStorageDate(now, in.Timezone)
	if err != nil {
		t.Fatalf("deriveStorageDate: %v", err)
	}

	// Run the real commit sequence up through recordStaging (§15.1 steps
	// 2-5) and stop there — exactly the crash point row 2 documents.
	_, m, _, manifestHash, err := stageFiles(layout, packageID, in, storageDate, now)
	if err != nil {
		t.Fatalf("stageFiles: %v", err)
	}
	if err := recordStaging(context.Background(), journal, m, manifestHash, now); err != nil {
		t.Fatalf("recordStaging: %v", err)
	}
	// Deliberately do not call renameToFinal or MarkSealed: this is the
	// simulated crash.

	stagingPath := filepath.Join(layout.Staging(), packageID)
	finalPath := filepath.Join(layout.Library(), "2026", "08", "21", packageID)
	if _, err := os.Stat(stagingPath); err != nil {
		t.Fatalf("precondition failed: staged bytes should exist: %v", err)
	}
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("precondition failed: final copy should not exist yet")
	}
	rec, found, err := journal.Lookup(context.Background(), packageID)
	if err != nil || !found || rec.State != StateStaging {
		t.Fatalf("precondition failed: journal should say staging, got found=%v state=%q err=%v", found, rec.State, err)
	}

	clock := system.FixedClock{At: now}
	report, err := Verify(context.Background(), layout, journal, clock)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	var entry Entry
	ok := false
	for _, e := range report.Entries {
		if e.PackageID == packageID {
			entry, ok = e, true
		}
	}
	if !ok {
		t.Fatalf("no report entry for package %q", packageID)
	}
	if entry.Action != ActionResumedSealed {
		t.Fatalf("Action = %q, want resumed_sealed; detail=%q", entry.Action, entry.Detail)
	}
	if entry.Path != finalPath {
		t.Fatalf("Path = %q, want %q", entry.Path, finalPath)
	}
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("expected package to be sealed at %q: %v", finalPath, err)
	}
	if _, err := os.Stat(stagingPath); !os.IsNotExist(err) {
		t.Fatalf("staging copy should be gone after the completed rename")
	}
	recAfter, found, err := journal.Lookup(context.Background(), packageID)
	if err != nil || !found || recAfter.State != StateSealed {
		t.Fatalf("journal should report sealed after Verify, got state=%q found=%v err=%v", recAfter.State, found, err)
	}
}
