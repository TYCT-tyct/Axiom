// Package store holds the suppliers module's tables: their migrations and the queries sqlc generates from
// queries/. Only the module's own code reaches it (internal/suppliers/README.md「表」).
package store

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Migrations are the module's goose files, at the root of the returned file system.
func Migrations() fs.FS {
	files, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	return files
}
