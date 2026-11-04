// Package operations owns the operations module's data: the business targets the owner sets in the admin and the
// operations dashboard compares with the actual figures (docs/domains/operations.md「运营看板」), and the operation log
// of every call of the operations API (「审计」). Approvals, money caps and node management are still to be built.
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
