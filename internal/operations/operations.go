// Package operations owns the operations module's data. Today that is the business targets the owner sets in the
// admin and the operations dashboard compares with the actual figures (docs/domains/operations.md「运营看板」); the
// rest of the module — the agent interface, approvals and node management — is still to be built.
package operations

import (
	"embed"
	"io/fs"

	"axiom/internal/platform/pg"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Module is the module's migrations, for cmd/axiom to give pg.Migrate and pg.DB.Check.
func Module() pg.Module {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	return pg.Module{Name: "operations", Migrations: files}
}
