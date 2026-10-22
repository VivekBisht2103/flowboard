// Package migrations embeds the SQL schema files so the binary is self-contained.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
