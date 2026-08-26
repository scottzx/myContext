package library_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scottzx/mycontext/internal/library"
)

func TestCommit_HappyPath(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hello world")

	res, err := library.Commit(context.Background(), layout, journal, clock, oneFileInput("req-1", src))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Replayed {
		t.Fatalf("fresh commit should not be Replayed")
	}
	if res.StorageDate != "2026-08-21" {
		t.Fatalf("storage_date = %q, want 2026-08-21", res.StorageDate)
	}
	wantFinal := filepath.Join(layout.Library(), "2026", "08", "21", res.PackageID)
	if res.FinalPath != wantFinal {
		t.Fatalf("FinalPath = %q, want %q", res.FinalPath, wantFinal)
	}
	if _, err := os.Stat(filepath.Join(res.FinalPath, "manifest.json")); err != nil {
		t.Fatalf("manifest.json missing at final path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.FinalPath, "original", "note.md")); err != nil {
		t.Fatalf("asset missing at final path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.Staging(), res.PackageID)); !os.IsNotExist(err) {
		t.Fatalf("staging directory should be gone after seal, stat err = %v", err)
	}

	rec, found, err := journal.Lookup(context.Background(), res.PackageID)
	if err != nil || !found {
		t.Fatalf("journal record missing after commit: found=%v err=%v", found, err)
	}
	if rec.State != library.StateSealed {
		t.Fatalf("journal state = %q, want sealed", rec.State)
	}

	if len(res.Manifest.Assets) != 1 {
		t.Fatalf("manifest assets = %d, want 1", len(res.Manifest.Assets))
	}
	asset := res.Manifest.Assets[0]
	if asset.RelativePath != filepath.Join("original", "note.md") {
		t.Fatalf("asset relative path = %q", asset.RelativePath)
	}
	if asset.SizeBytes != int64(len("hello world")) {
		t.Fatalf("asset size = %d", asset.SizeBytes)
	}
}

func TestCommit_IdempotentRetry_SameRequestID(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hello world")
	in := oneFileInput("req-dup", src)

	first, err := library.Commit(context.Background(), layout, journal, clock, in)
	if err != nil {
		t.Fatalf("first Commit: %v", err)
	}
	second, err := library.Commit(context.Background(), layout, journal, clock, in)
	if err != nil {
		t.Fatalf("second Commit: %v", err)
	}
	if !second.Replayed {
		t.Fatalf("second commit with same request_id should be Replayed")
	}
	if second.PackageID != first.PackageID {
		t.Fatalf("second commit produced a different package: %q vs %q", second.PackageID, first.PackageID)
	}

	// No duplicate package directory anywhere under the Library.
	dayDir := filepath.Join(layout.Library(), "2026", "08", "21")
	entries, err := os.ReadDir(dayDir)
	if err != nil {
		t.Fatalf("read day dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("day directory has %d entries, want exactly 1 (no duplicate package)", len(entries))
	}
}

func TestCommit_RejectsPathTraversalInAssetName(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hello world")

	in := library.CaptureInput{
		RequestID:  "req-traversal",
		CapturedAt: fixedNow,
		Files: []library.InputFile{
			{SourcePath: src, Role: library.RoleOriginal, Name: "../../etc/evil.md"},
		},
	}
	_, err := library.Commit(context.Background(), layout, journal, clock, in)
	if err == nil {
		t.Fatalf("expected traversal in asset name to be rejected")
	}

	// Nothing should have escaped the staging root.
	if _, statErr := os.Stat(filepath.Join(layout.Root, "..", "etc", "evil.md")); !os.IsNotExist(statErr) {
		t.Fatalf("traversal appears to have written outside the root")
	}
}

func TestCommit_RejectsUnknownRole(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hello world")

	in := library.CaptureInput{
		RequestID:  "req-role",
		CapturedAt: fixedNow,
		Files: []library.InputFile{
			{SourcePath: src, Role: "not-a-real-role", Name: "note.md"},
		},
	}
	if _, err := library.Commit(context.Background(), layout, journal, clock, in); err == nil {
		t.Fatalf("expected unknown role to be rejected")
	}
}

func TestCommit_TimezoneDerivesStorageDate(t *testing.T) {
	layout := newTestLayout(t)
	journal := newFakeJournal()
	clock := fixedClock{at: fixedNow}
	src := writeSourceFile(t, "note.md", "hi")

	// 2026-08-21T20:00:00Z is 2026-08-22T04:00:00+08:00 in Shanghai: the
	// UTC calendar day and the Shanghai calendar day disagree, so this
	// proves storage_date is derived from the configured timezone and not
	// from UTC or the host's local zone.
	in := oneFileInput("req-tz", src)
	in.CapturedAt = time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC)

	res, err := library.Commit(context.Background(), layout, journal, clock, in)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.StorageDate != "2026-08-22" {
		t.Fatalf("storage_date = %q, want 2026-08-22 (Shanghai calendar day, not UTC's 2026-08-21)", res.StorageDate)
	}
}
