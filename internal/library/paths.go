package library

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
)

// validRole reports whether role is one of the four fixed Capture Package
// sub-directories (B+ design §9.4). Nothing else is accepted: this package
// does not invent new physical layout on the caller's say-so.
func validRole(role string) bool {
	switch role {
	case RoleOriginal, RoleAttachments, RoleDerived, RolePreviews:
		return true
	default:
		return false
	}
}

// validAssetName enforces §15.3's "normalise every input path; reject `..`
// traversal and symlink escape" for the destination file name supplied by
// the caller. A name must be a single path element: no directories, no
// traversal, not absolute. This also forecloses symlink-escape through a
// crafted multi-segment name, since there is never more than one segment to
// resolve.
func validAssetName(name string) error {
	if name == "" {
		return protocol.BadInput("asset file name is required")
	}
	if filepath.IsAbs(name) {
		return protocol.BadInput("asset file name %q must be relative", name)
	}
	clean := filepath.Clean(name)
	if clean != name || clean == "." || clean == ".." {
		return protocol.BadInput("asset file name %q must be a single path element without traversal", name)
	}
	if filepath.Base(clean) != clean {
		return protocol.BadInput("asset file name %q must not contain a path separator", name)
	}
	return nil
}

// ensureRealDir makes sure path exists as an ordinary directory, refusing to
// follow or silently adopt a symlink planted there (§15.3 "symlink
// escape"). A pre-existing non-symlink directory is left alone; a missing
// path is created.
func ensureRealDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(path, 0o700)
		}
		return protocol.Wrap(err, protocol.CodeIntegrity, fmt.Sprintf("cannot stat %q", path))
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return protocol.Integrity("refusing to use %q: path is a symlink", path)
	}
	if !info.IsDir() {
		return protocol.Integrity("refusing to use %q: path exists and is not a directory", path)
	}
	return nil
}

// deriveStorageDate computes the immutable storage_date (B+ design §9.2)
// from captured_at in the given IANA timezone.
func deriveStorageDate(capturedAt time.Time, timezone string) (string, error) {
	if timezone == "" {
		timezone = DefaultTimezone
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return "", protocol.BadInput("unknown timezone %q: %v", timezone, err)
	}
	return capturedAt.In(loc).Format("2006-01-02"), nil
}

// finalDir computes the deterministic sealed location of a package from its
// (immutable) storage_date and package ID: library/YYYY/MM/DD/<packageID>.
func finalDir(libraryRoot, storageDate, packageID string) (string, error) {
	t, err := time.Parse("2006-01-02", storageDate)
	if err != nil {
		return "", protocol.Integrity("invalid storage_date %q: %v", storageDate, err)
	}
	return filepath.Join(
		libraryRoot,
		fmt.Sprintf("%04d", t.Year()),
		fmt.Sprintf("%02d", int(t.Month())),
		fmt.Sprintf("%02d", t.Day()),
		packageID,
	), nil
}
