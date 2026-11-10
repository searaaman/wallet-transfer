// Package migrations embeds the SQL schema so the server and tests apply the same file.
package migrations

import _ "embed"

//go:embed 001_init.sql
var Init string
