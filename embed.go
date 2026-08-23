// Package mycontext embeds build-time assets that ship inside the binary:
// SQL migrations, the JSON Schema protocol definitions (§7.1), and the built
// static frontend (§7.2). Only the module root can embed these top-level
// directories.
package mycontext

import "embed"

//go:embed migrations
var Migrations embed.FS

// MigrationsDirOps is the path inside Migrations holding ops.db steps.
const MigrationsDirOps = "migrations/ops"

// MigrationsDirContext is the path inside Migrations holding context.db steps.
const MigrationsDirContext = "migrations/context"

// WebDist is the built frontend (`make web`). A placeholder index.html ships
// until that runs, so a plain `go build` never fails on a missing directory.
//
//go:embed all:web/dist
var WebDist embed.FS

// WebDistDir is the path inside WebDist holding the site root.
const WebDistDir = "web/dist"
