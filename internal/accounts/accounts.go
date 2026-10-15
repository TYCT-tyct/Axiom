// Package accounts holds sites, accounts, workspaces and the rest of the tenant structure, and is the one place that
// builds a site's or an account's scope (README.md).
package accounts

import (
	"embed"
	"io/fs"

	"axiom/internal/platform/pg"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations is this module's migrations, which cmd/axiom hands to pg.Migrate and pg.DB.Check.
func Migrations() pg.Module {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	return pg.Module{Name: "accounts", Migrations: files}
}
