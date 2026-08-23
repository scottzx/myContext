// Package system owns the data root, configuration, version reporting,
// backups and doctor checks.
package system

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"github.com/scottzx/mycontext/internal/protocol"
)

// MarkerFile names the file that pins a directory tree to a data root.
const MarkerFile = ".mycontext-root.json"

// EnvRoot is the environment variable checked during root resolution.
const EnvRoot = "MYCONTEXT_ROOT"

// Layout describes the on-disk data root (§8.1). Every path is derived from
// Root so a command can never straddle two instances.
type Layout struct {
	Root string
}

func NewLayout(root string) Layout { return Layout{Root: root} }

func (l Layout) System() string     { return filepath.Join(l.Root, "system") }
func (l Layout) ConfigPath() string { return filepath.Join(l.System(), "config.json") }
func (l Layout) Staging() string    { return filepath.Join(l.System(), "staging") }
func (l Layout) Recovery() string   { return filepath.Join(l.System(), "recovery") }
func (l Layout) Data() string       { return filepath.Join(l.Root, "data") }
func (l Layout) OpsDB() string      { return filepath.Join(l.Data(), "ops.db") }
func (l Layout) ContextDB() string  { return filepath.Join(l.Data(), "context.db") }
func (l Layout) Library() string    { return filepath.Join(l.Root, "library") }
func (l Layout) Snapshots() string  { return filepath.Join(l.Root, "snapshots") }
func (l Layout) Exports() string    { return filepath.Join(l.Root, "exports") }
func (l Layout) Logs() string       { return filepath.Join(l.Root, "logs") }

// Dirs lists every directory `init` creates.
func (l Layout) Dirs() []string {
	return []string{
		l.System(), l.Staging(), l.Recovery(),
		l.Data(), l.Library(),
		filepath.Join(l.Library(), "_system", "orphaned"),
		filepath.Join(l.Library(), "_system", "quarantine"),
		filepath.Join(l.Library(), "_system", "trash"),
		l.Snapshots(), l.Exports(), l.Logs(),
	}
}

// ResolveRoot finds the data root using the fixed order from §8.2:
// flag, environment, upward marker file search, platform default.
// The resolved path is always absolute and symlink-free so that later path
// containment checks are meaningful.
func ResolveRoot(flagValue string) (string, error) {
	if flagValue != "" {
		return normalize(flagValue)
	}
	if env := os.Getenv(EnvRoot); env != "" {
		return normalize(env)
	}
	if marker, ok := findMarker(); ok {
		return marker, nil
	}
	return normalize(DefaultRoot())
}

// DefaultRoot is the platform fallback. iSH (linux/386) is the first-class
// target and uses the Minis phone app's shared directory (B+ design §6.1) —
// that path is owned by Minis, not by this binary, and is not renamed with it.
func DefaultRoot() string {
	if runtime.GOOS == "linux" && runtime.GOARCH == "386" {
		return "/var/minis/shared"
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".mycontext")
	}
	return ".mycontext"
}

type markerFile struct {
	Root string `json:"root"`
}

// findMarker walks up from the working directory looking for a marker file.
// A marker may point elsewhere; a relative target resolves against the
// directory holding the marker.
func findMarker() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		candidate := filepath.Join(dir, MarkerFile)
		if raw, err := os.ReadFile(candidate); err == nil {
			var m markerFile
			if json.Unmarshal(raw, &m) == nil && m.Root != "" {
				target := m.Root
				if !filepath.IsAbs(target) {
					target = filepath.Join(dir, target)
				}
				if resolved, err := normalize(target); err == nil {
					return resolved, true
				}
			}
			if resolved, err := normalize(dir); err == nil {
				return resolved, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func normalize(path string) (string, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", protocol.BadInput("cannot resolve path %q: %v", path, err)
	}
	// EvalSymlinks only works on existing paths; an as-yet-uncreated root is
	// legitimate for `init`, so fall back to the lexical absolute path.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

func expandHome(path string) (string, error) {
	if path != "~" && !hasPrefix(path, "~"+string(filepath.Separator)) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", protocol.BadInput("cannot expand ~: %v", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
