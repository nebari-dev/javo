// Package dialect holds the sqlc output the one-query-set check runs on both
// engines. Only tests import it, so it is not part of the javo binary.
package dialect

import _ "embed"

// Schema is the DDL both engines execute before the queries run.
//
//go:embed schema.sql
var Schema string
