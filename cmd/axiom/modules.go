package main

import (
	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// modules are the modules that have tables, whose migrations the program applies and whose versions it checks at start.
func modules() []pg.Module { return []pg.Module{catalog.Migrations, suppliers.Module()} }
