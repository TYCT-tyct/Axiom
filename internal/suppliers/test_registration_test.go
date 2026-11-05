package suppliers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestEveryProblemVariantIsRegistered: every cause the adapter can return and every fault a stream can report has one
// registration: an instance in ProblemCauses' table, whose String() is the name the contract checks, or an entry in
// faultNames. The variants are read from their declarations rather than listed here, so a variant added without that
// registration fails this test instead of reaching the admin API as a cause outside its enum
// (internal/suppliers/README.md「用真实生成测试端点」).
func TestEveryProblemVariantIsRegistered(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	causes := declaredCauses(t, filepath.Join(root, "internal", "suppliers", "internal", "adapter"))
	registered := registeredCauses(t, filepath.Join(root, "internal", "suppliers"))
	if !slices.Equal(causes, registered) {
		t.Errorf("the adapter declares causes %q but ProblemCauses registers %q: register every cause once so that its "+
			"String() reaches the contract", causes, registered)
	}
	faults := declaredFaults(t, filepath.Join(root, "internal", "protocol"))
	mapped := mappedFaults(t, filepath.Join(root, "internal", "suppliers"))
	if !slices.Equal(faults, mapped) {
		t.Errorf("the protocol declares faults %q but faultNames maps %q: map every fault to its contract cause",
			faults, mapped)
	}
}

// declaredCauses is every type of the adapter package that declares the closed set's marker method, in name order.
func declaredCauses(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	for _, path := range packageFiles(t, dir) {
		for _, decl := range parseGo(t, path).Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "cause" {
				continue
			}
			names = append(names, receiverType(t, path, fn))
		}
	}
	if len(names) == 0 {
		t.Fatalf("%s: no cause() method is declared: the scan cannot see the closed set", dir)
	}
	slices.Sort(names)
	return names
}

// registeredCauses is every cause type the sample table of ProblemCauses holds, in name order.
func registeredCauses(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	found := false
	for _, path := range packageFiles(t, dir) {
		for _, decl := range parseGo(t, path).Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "ProblemCauses" {
				continue
			}
			found = true
			for _, element := range causeSamples(t, path, fn).Elts {
				names = append(names, sampleType(t, path, element))
			}
		}
	}
	if !found {
		t.Fatalf("%s: ProblemCauses is not declared: the scan cannot see its table", dir)
	}
	slices.Sort(names)
	return names
}

// causeSamples finds the []adapter.Cause literal a ProblemCauses body walks.
func causeSamples(t *testing.T, path string, fn *ast.FuncDecl) *ast.CompositeLit {
	t.Helper()
	var list *ast.CompositeLit
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		lit, ok := node.(*ast.CompositeLit)
		if !ok || list != nil {
			return list == nil
		}
		array, ok := lit.Type.(*ast.ArrayType)
		if !ok {
			return true
		}
		element, ok := array.Elt.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if base, ok := element.X.(*ast.Ident); ok && base.Name == "adapter" && element.Sel.Name == "Cause" {
			list = lit
		}
		return true
	})
	if list == nil {
		t.Fatalf("%s: ProblemCauses has no []adapter.Cause sample table: the scan cannot read the registrations", path)
	}
	return list
}

// sampleType is the cause type a sample element instantiates: RateLimited{} or &RateLimited{}.
func sampleType(t *testing.T, path string, element ast.Expr) string {
	t.Helper()
	if unary, ok := element.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		element = unary.X
	}
	lit, ok := element.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("%s: a ProblemCauses sample is not a composite literal: write each sample as Type{}, so the scan can "+
			"name its variant", path)
	}
	return exprType(t, path, lit.Type)
}

// declaredFaults is every member of the protocol's Fault constant set except FaultNone, in name order.
func declaredFaults(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	found := false
	for _, path := range packageFiles(t, dir) {
		for _, block := range constBlocks(parseGo(t, path), "Fault") {
			found = true
			names = append(names, specNames(block, "FaultNone")...)
		}
	}
	if !found {
		t.Fatalf("%s: no Fault constant block: the scan cannot see the closed set", dir)
	}
	slices.Sort(names)
	return names
}

// mappedFaults is every Fault the faultNames table maps, in name order.
func mappedFaults(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	found := false
	for _, path := range packageFiles(t, dir) {
		for _, spec := range faultNameSpecs(parseGo(t, path)) {
			found = true
			names = append(names, faultKeys(t, path, spec)...)
		}
	}
	if !found {
		t.Fatalf("%s: faultNames is not declared: the scan cannot see its table", dir)
	}
	slices.Sort(names)
	return names
}

// constBlocks is every constant declaration of a file whose first member names typ; the rest of the block inherits it.
func constBlocks(file *ast.File, typ string) []*ast.GenDecl {
	var blocks []*ast.GenDecl
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST && declaresType(gen, typ) {
			blocks = append(blocks, gen)
		}
	}
	return blocks
}

// specNames is every name a constant block declares except skip.
func specNames(gen *ast.GenDecl, skip string) []string {
	var names []string
	for _, spec := range gen.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, name := range value.Names {
			if name.Name != skip {
				names = append(names, name.Name)
			}
		}
	}
	return names
}

// faultNameSpecs is the faultNames variable declaration of a file.
func faultNameSpecs(file *ast.File) []*ast.ValueSpec {
	var specs []*ast.ValueSpec
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if ok && len(value.Names) == 1 && value.Names[0].Name == "faultNames" && len(value.Values) == 1 {
				specs = append(specs, value)
			}
		}
	}
	return specs
}

// faultKeys is every Fault key of the faultNames table.
func faultKeys(t *testing.T, path string, spec *ast.ValueSpec) []string {
	t.Helper()
	lit, ok := spec.Values[0].(*ast.CompositeLit)
	if !ok {
		t.Fatalf("%s: faultNames is not a composite literal: the scan cannot read its keys", path)
	}
	var names []string
	for _, element := range lit.Elts {
		entry, ok := element.(*ast.KeyValueExpr)
		if !ok {
			t.Fatalf("%s: faultNames has an entry without a key: the scan cannot name its fault", path)
		}
		names = append(names, exprType(t, path, entry.Key))
	}
	return names
}

// declaresType is whether a constant declaration's first member names the given type; the rest of the block inherits it.
func declaresType(gen *ast.GenDecl, typ string) bool {
	if len(gen.Specs) == 0 {
		return false
	}
	first, ok := gen.Specs[0].(*ast.ValueSpec)
	if !ok || first.Type == nil {
		return false
	}
	named, ok := first.Type.(*ast.Ident)
	return ok && named.Name == typ
}

// exprType is the type an expression names, with a package qualifier or pointer removed.
func exprType(t *testing.T, path string, expr ast.Expr) string {
	t.Helper()
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.SelectorExpr:
		return expr.Sel.Name
	case *ast.StarExpr:
		return exprType(t, path, expr.X)
	}
	t.Fatalf("%s: the scan cannot name the type of %T", path, expr)
	return ""
}

// receiverType is the type a method is declared on.
func receiverType(t *testing.T, path string, fn *ast.FuncDecl) string {
	t.Helper()
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		t.Fatalf("%s: %s has no single receiver", path, fn.Name.Name)
	}
	return exprType(t, path, fn.Recv.List[0].Type)
}

// packageFiles is the non-test Go files of one package directory.
func packageFiles(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, path := range paths {
		if !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
	}
	if len(files) == 0 {
		t.Fatalf("%s: no Go files: the scan cannot see the package", dir)
	}
	return files
}

// parseGo is one source file's declarations.
func parseGo(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}
