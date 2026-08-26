package library_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scottzx/mycontext/internal/library"
	"github.com/scottzx/mycontext/internal/system"
)

var (
	errInjectedFault = errors.New("fake journal: injected fault")
	errNoSuchPackage = errors.New("fake journal: no such package")
)

var fixedNow = time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }
func (c fixedClock) Today() string  { return c.at.Format(system.DateLayout) }

// newTestLayout builds a fresh, fully-initialised data root under t.TempDir()
// so nothing ever touches the repo or the user's real ~/.1agents directory.
func newTestLayout(t *testing.T) system.Layout {
	t.Helper()
	root := t.TempDir()
	layout := system.NewLayout(root)
	for _, dir := range layout.Dirs() {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return layout
}

// writeSourceFile creates a file with the given content outside the data
// root (as a real import source would be) and returns its path.
func writeSourceFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	return path
}

func oneFileInput(requestID, srcPath string) library.CaptureInput {
	return library.CaptureInput{
		RequestID:  requestID,
		CapturedAt: fixedNow,
		Files: []library.InputFile{
			{SourcePath: srcPath, Role: library.RoleOriginal, Name: "note.md"},
		},
	}
}
