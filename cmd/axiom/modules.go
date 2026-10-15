package main

import (
	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
)

// modules are the modules that have migrations of their own, in no particular order (pg runs them in the order of
// docs/architecture.md). migrate applies them, and a node checks them at start.
var modules = []pg.Module{catalog.Migrations}
