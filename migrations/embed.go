// Package migrations embeds the versioned SQL migrations. Files are named
// NNNN_description.sql and applied in lexical order; each runs in one
// transaction and is recorded in schema_migrations.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
