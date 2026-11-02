package gateway

import (
	"embed"
	"io/fs"

	"axiom/internal/platform/pg"
)

// migrations are the gateway's own tables: the response cache is the only mutable state this module keeps.
//
//go:embed migrations/*.sql
var migrations embed.FS

// Module is the gateway's schema for the migration runner.
func Module() pg.Module {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	return pg.Module{Name: "gateway", Migrations: files}
}
