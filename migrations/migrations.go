// Package migrations is plinth's migration stream (spec.md §4). A product applies it before its
// own, with db.Migrate. Released files are never edited: a change is a new file.
package migrations

import (
	"embed"

	"github.com/nickwhiteley/plinth/db"
)

//go:embed *.sql
var files embed.FS

// Plinth is the stream, with its own version table.
var Plinth = db.Stream{Name: "plinth", FS: files, Table: "plinth_schema_version"}
