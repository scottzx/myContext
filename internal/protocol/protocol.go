// Package protocol defines the stable CLI wire contract: the success/error
// envelope, error codes and process exit codes described in the technical
// design (§11).
package protocol

// Version is the wire protocol identifier. Field additions are backwards
// compatible; removals or semantic changes require a major bump.
const Version = "minis-cli/v1"

// Exit codes (§11.4).
const (
	ExitOK            = 0
	ExitBadInput      = 2
	ExitNotFound      = 3
	ExitConflict      = 4
	ExitIncompatible  = 5
	ExitBusy          = 6
	ExitForbidden     = 7
	ExitIntegrity     = 8
	ExitExternal      = 9
	ExitNeedsRecovery = 10
)

// Error codes. Agents branch on these, never on the message text.
const (
	CodeBadInput        = "BAD_INPUT"
	CodeNotFound        = "NOT_FOUND"
	CodeAmbiguous       = "AMBIGUOUS_MATCH"
	CodeVersionConflict = "VERSION_CONFLICT"
	CodeIdempotency     = "IDEMPOTENCY_CONFLICT"
	CodeIncompatible    = "SCHEMA_INCOMPATIBLE"
	CodeBusy            = "DATABASE_BUSY"
	CodeForbidden       = "CONFIRMATION_REQUIRED"
	CodeIntegrity       = "INTEGRITY_FAILED"
	CodeExternal        = "EXTERNAL_FAILED"
	CodeNeedsRecovery   = "RECOVERY_REQUIRED"
	CodeInternal        = "INTERNAL"
)

// Envelope is the single response shape for every command, in both success
// and failure. Callers can always read protocol/ok/command/meta.
type Envelope struct {
	Protocol  string   `json:"protocol"`
	OK        bool     `json:"ok"`
	Command   string   `json:"command"`
	RequestID string   `json:"request_id,omitempty"`
	Data      any      `json:"data,omitempty"`
	Changes   []Change `json:"changes,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Error     *Error   `json:"error,omitempty"`
	Meta      Meta     `json:"meta"`
}

// Change summarises one persisted mutation so a caller can refresh precisely
// the affected projections without re-reading everything.
type Change struct {
	EntityType     string   `json:"entity_type"`
	EntityID       string   `json:"entity_id"`
	EventType      string   `json:"event_type"`
	Version        int64    `json:"version,omitempty"`
	ProjectionKeys []string `json:"projection_keys,omitempty"`
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Details   any    `json:"details,omitempty"`
	Retryable bool   `json:"retryable"`
}

type Meta struct {
	Root           string           `json:"root"`
	CLIVersion     string           `json:"cli_version"`
	SchemaVersions map[string]int64 `json:"schema_versions,omitempty"`
	DurationMS     int64            `json:"duration_ms"`
	Actor          string           `json:"actor,omitempty"`
	DryRun         bool             `json:"dry_run,omitempty"`
}
