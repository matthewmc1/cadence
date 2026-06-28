// Package migrations embeds Cadence's SQL migration files so the server can
// apply them at startup (CADENCE_AUTO_MIGRATE) or via tooling.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
