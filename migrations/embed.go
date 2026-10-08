package migrations

import "embed"

// FS is embedded so migrations work from a read-only container.
//
//go:embed *.sql
var FS embed.FS
