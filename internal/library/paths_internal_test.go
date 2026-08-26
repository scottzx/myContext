package library

// White-box tests for the §15.3 file-safety guards. These live in package
// library (not library_test) because the guards themselves are unexported:
// they are enforcement details of stageFiles, not part of the public API.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
)

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm
}

func TestValidAssetName_RejectsTraversal(t *testing.T) {
	cases := []string{
		"..",
		"../evil.md",
		"a/../../b",
		"/etc/passwd",
		"sub/dir/name.md",
		"",
	}
	for _, name := range cases {
		if err := validAssetName(name); err == nil {
			t.Errorf("validAssetName(%q): expected rejection, got nil", name)
		}
	}
}

func TestValidAssetName_AcceptsPlainNames(t *testing.T) {
	cases := []string{"note.md", "photo-1.jpg", "report_v2.pdf"}
	for _, name := range cases {
		if err := validAssetName(name); err != nil {
			t.Errorf("validAssetName(%q): unexpected rejection: %v", name, err)
		}
	}
}

// TestEnsureRealDir_RejectsSymlinkEscape plants a symlink where a Capture
// Package role directory would normally be created and asserts stageFiles'
// directory guard refuses to write through it, rather than silently
// following it outside the staging root (§15.3 "symlink escape").
func TestEnsureRealDir_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // an attacker-controlled location outside the package

	stagingDir := filepath.Join(root, "system", "staging", "cap_evil")
	if err := os.MkdirAll(filepath.Dir(stagingDir), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Plant "original" as a symlink pointing outside the package, as if a
	// previous crash (or an attacker with local access) left it there.
	roleDir := filepath.Join(stagingDir, "original")
	if err := os.Symlink(outside, roleDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := ensureRealDir(roleDir)
	if err == nil {
		t.Fatalf("expected ensureRealDir to reject a symlinked role directory")
	}
	var appErr *protocol.AppError
	if !errors.As(err, &appErr) || appErr.Code != protocol.CodeIntegrity {
		t.Fatalf("expected an INTEGRITY_FAILED error, got %v", err)
	}

	// Nothing should have been written through the symlink.
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatalf("read outside dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("expected outside directory to remain empty, found %d entries", len(entries))
	}
}

func TestEnsureRealDir_CreatesMissingPlainDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a", "b")
	if err := ensureRealDir(target); err != nil {
		t.Fatalf("ensureRealDir: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		t.Fatalf("expected %q to exist as a directory", target)
	}
}

func TestDeriveStorageDate_UsesConfiguredTimezone(t *testing.T) {
	capturedAt := mustParseRFC3339(t, "2026-08-21T20:00:00Z")
	date, err := deriveStorageDate(capturedAt, "Asia/Shanghai")
	if err != nil {
		t.Fatalf("deriveStorageDate: %v", err)
	}
	if date != "2026-08-22" {
		t.Fatalf("storage_date = %q, want 2026-08-22", date)
	}
}

func TestDeriveStorageDate_RejectsUnknownTimezone(t *testing.T) {
	capturedAt := mustParseRFC3339(t, "2026-08-21T20:00:00Z")
	if _, err := deriveStorageDate(capturedAt, "Not/A_Zone"); err == nil {
		t.Fatalf("expected unknown timezone to be rejected")
	}
}
