package main

import (
	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/observability"
	"axiom/internal/payments"
	"axiom/internal/platform/pg"
	"axiom/internal/quality"
	"axiom/internal/suppliers"
)

// modules are the modules that have tables, whose migrations the program applies and whose versions it checks at start.
func modules() []pg.Module {
	return []pg.Module{
		accounts.Module(), catalog.Migrations, observability.Module(), suppliers.Module(), billing.Migrations(),
		payments.Migrations(), quality.Module(),
	}
}
