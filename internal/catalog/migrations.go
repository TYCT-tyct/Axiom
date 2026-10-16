package catalog

import (
	"embed"
	"io/fs"

	"axiom/internal/platform/pg"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Migrations are the catalog's tables, policies and grants, for cmd/axiom to hand to pg.Migrate and DB.Check.
var Migrations = pg.Module{Name: "catalog", Migrations: migrationFiles()}

func migrationFiles() fs.FS {
	files, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	return files
}
