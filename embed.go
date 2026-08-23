// Package mycontext embeds build-time assets that ship inside the binary:
// SQL migrations and the JSON Schema protocol definitions (§7.1).
// Only the module root can embed these top-level directories.
package mycontext

import "embed"

//go:embed migrations
var Migrations embed.FS

// MigrationsDirOps is the path inside Migrations holding ops.db steps.
const MigrationsDirOps = "migrations/ops"

// MigrationsDirContext is the path inside Migrations holding context.db steps.
const MigrationsDirContext = "migrations/context"
