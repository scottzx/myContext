// Package library implements the Library file transaction described in the
// technical design (§15): a "recoverable commit" for capture packages,
// because SQLite transactions cannot span a file copy. The database half of
// the commit is expressed as the Journal interface so this package never
// depends on the ops/context schema; a caller wires in a real implementation
// once that schema exists.
//
// The unit of work is a Capture Package (B+ design §9.2/§9.4): a
// self-contained folder of Assets plus an immutable manifest.json, filed
// under library/YYYY/MM/DD/cap_<id> by storage_date, which is derived once
// from captured_at and never changes.
package library

import (
	"encoding/json"
	"time"
)

// Asset roles, matching the physical sub-directories of a Capture Package
// (B+ design §9.4).
const (
	RoleOriginal    = "original"
	RoleAttachments = "attachments"
	RoleDerived     = "derived"
	RolePreviews    = "previews"
)

// DefaultTimezone is used when a caller does not supply one. It matches the
// instance default in system/config.go.
const DefaultTimezone = "Asia/Shanghai"

// ManifestVersion is the manifest.json format version. Bump it only for a
// breaking change to the file's shape.
const ManifestVersion = 1

// InputFile is one asset to copy into a Capture Package.
type InputFile struct {
	// SourcePath is where to copy bytes from. It is read-only input: the
	// path itself is never treated as a long-term source of truth (§15.3).
	SourcePath string

	// Role is one of the Role* constants above.
	Role string

	// Name is the destination file name inside the role folder. It must be
	// a single path element: no directories, no "..", not absolute.
	Name string
}

// CaptureInput describes one capture commit (§15.1 step 1 onward).
type CaptureInput struct {
	// RequestID makes the commit idempotent: committing the same
	// RequestID again returns the original result instead of creating a
	// second package.
	RequestID string

	// CapturedAt is when the material was captured. It is combined with
	// Timezone to derive storage_date, which never changes afterwards
	// (B+ design §9.2). Zero means "now".
	CapturedAt time.Time

	// Timezone is an IANA zone name. Empty means DefaultTimezone.
	Timezone string

	// SourceRef and CaptureMethod are opaque, caller-defined identifiers
	// for where this came from and how it entered the system (§9.4's
	// "来源和捕获入口"). Neither is interpreted by this package.
	SourceRef     string
	CaptureMethod string

	// Suggested carries an optional, caller-supplied Item/Component
	// suggestion (§9.4's "捕获时可确定的 Item/Component 建议结构"). It is
	// stored verbatim in the manifest and never interpreted here: building
	// Items is context.db's job, not this package's.
	Suggested json.RawMessage

	// Files lists every asset to stage. At least one is required.
	Files []InputFile
}

// ManifestAsset is one asset entry recorded in manifest.json.
type ManifestAsset struct {
	RelativePath string `json:"relative_path"`
	Role         string `json:"role"`
	SequenceNo   int    `json:"sequence_no"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
	MimeType     string `json:"mime_type,omitempty"`
}

// Manifest is the immutable per-package descriptor written to
// manifest.json (B+ design §9.4). It is written once, before the package is
// sealed, and never rewritten afterwards: business metadata added later
// (tags, Bundles, project links, ...) lives in context.db, not here.
type Manifest struct {
	ManifestVersion int             `json:"manifest_version"`
	PackageID       string          `json:"capture_package_id"`
	RequestID       string          `json:"request_id"`
	CapturedAt      time.Time       `json:"captured_at"`
	Timezone        string          `json:"timezone"`
	StorageDate     string          `json:"storage_date"`
	SourceRef       string          `json:"source_ref,omitempty"`
	CaptureMethod   string          `json:"capture_method,omitempty"`
	Suggested       json.RawMessage `json:"suggested_structure,omitempty"`
	Assets          []ManifestAsset `json:"assets"`
}

// CommitResult is what a successful Commit (fresh or replayed) returns.
type CommitResult struct {
	PackageID   string
	StorageDate string
	FinalPath   string
	Manifest    Manifest

	// Replayed is true when RequestID had already reached "sealed" or
	// "staging" on a previous call and this call resumed or returned that
	// outcome rather than staging new bytes.
	Replayed bool
}
