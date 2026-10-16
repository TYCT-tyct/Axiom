// Command removable checks that every adapter can be removed: for each adapter in the registry, deleting its package
// and its line in the registry must leave the rest of the program building and passing go vet. It is `just
// adapters-removable`; docs/domains/suppliers.md「代码组织」 is the rule it keeps.
//
// The deletion is made with go's -overlay, which replaces files for one build without touching the checkout: the
// registry is swapped for a copy without the adapter and each of its files is mapped to nothing.
//
// usage: go run ./internal/suppliers/internal/removable <repository root>
package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"axiom/internal/platform/jsonx"
)

const (
	adaptersDir  = "internal/suppliers/internal"
	registryFile = adaptersDir + "/registry/registry.go"
	importPrefix = "axiom/" + adaptersDir + "/"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: removable <repository root>")
		os.Exit(2)
	}
	if err := checkAll(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func checkAll(root string) error {
	source, err := os.ReadFile(filepath.Join(root, registryFile)) //nolint:gosec // the registry of this repository
	if err != nil {
		return err
	}
	names, err := adapterNames(string(source))
	if err != nil {
		return fmt.Errorf("%s: %w", registryFile, err)
	}
	if len(names) == 0 {
		slog.Info("no adapters are registered, so none can be removed yet")
		return nil
	}
	for _, name := range names {
		if err := checkRemoval(root, string(source), name); err != nil {
			return fmt.Errorf("removing adapter %s: %w", name, err)
		}
		slog.Info("adapter can be removed", "adapter", name)
	}
	return nil
}

// entry is one adapter of the registry's list: its package and the lines its constructor call occupies.
type entry struct {
	name                string
	firstLine, lastLine int
}

// parseRegistry reads the adapters list with go/ast. Anything it does not understand is an error, not an entry it
// skips: an adapter missed here would be one whose removal is never checked. An entry is `pkg.New(),` on lines of
// its own, with a comment allowed after it.
func parseRegistry(source string) (entries []entry, imports map[string]entry, err error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "registry.go", source, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}
	imports = map[string]entry{}
	for _, spec := range file.Imports {
		line := fset.Position(spec.Pos()).Line
		imports[strings.Trim(spec.Path.Value, `"`)] = entry{firstLine: line, lastLine: fset.Position(spec.End()).Line}
	}
	list, err := adaptersList(file)
	if err != nil {
		return nil, nil, err
	}
	last := fset.Position(list.Lbrace).Line
	for _, element := range list.Elts {
		e, err := adapterEntry(fset, element)
		if err != nil {
			return nil, nil, err
		}
		if e.firstLine <= last {
			return nil, nil, fmt.Errorf("line %d: an adapter shares its line with another entry or the opening brace", e.firstLine)
		}
		entries, last = append(entries, e), e.lastLine
	}
	if end := fset.Position(list.Rbrace).Line; len(entries) > 0 && end <= last {
		return nil, nil, fmt.Errorf("line %d: the closing brace shares a line with an entry", end)
	}
	return entries, imports, nil
}

// adaptersList finds the composite literal that `var adapters` is initialised with.
func adaptersList(file *ast.File) (*ast.CompositeLit, error) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			v, ok := spec.(*ast.ValueSpec)
			if ok && len(v.Names) == 1 && v.Names[0].Name == "adapters" && len(v.Values) == 1 {
				if list, ok := v.Values[0].(*ast.CompositeLit); ok {
					return list, nil
				}
				return nil, errors.New("adapters is not initialised with a composite literal")
			}
		}
	}
	return nil, errors.New("the registry has no `var adapters = ...`")
}

func adapterEntry(fset *token.FileSet, element ast.Expr) (entry, error) {
	line := fset.Position(element.Pos()).Line
	call, ok := element.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return entry{}, fmt.Errorf("line %d: an entry must be a call of pkg.New() without arguments", line)
	}
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "New" {
		if pkg, ok := selector.X.(*ast.Ident); ok {
			return entry{pkg.Name, line, fset.Position(element.End()).Line}, nil
		}
	}
	return entry{}, fmt.Errorf("line %d: an entry must be a call of pkg.New() without arguments", line)
}

// adapterNames lists the packages the registry's list constructs adapters from.
func adapterNames(registry string) ([]string, error) {
	entries, _, err := parseRegistry(registry)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.name
	}
	return names, nil
}

// withoutAdapter is the registry with the adapter's lines and import removed. The import is required to be there: a
// registry that constructs an adapter it does not import would not have compiled.
func withoutAdapter(registry, name string) (string, error) {
	entries, imports, err := parseRegistry(registry)
	if err != nil {
		return "", err
	}
	drop := map[int]bool{}
	for _, e := range entries {
		if e.name == name {
			for l := e.firstLine; l <= e.lastLine; l++ {
				drop[l] = true
			}
		}
	}
	imported, ok := imports[importPrefix+name]
	if !ok || len(drop) == 0 {
		return "", fmt.Errorf("the registry does not both import %q and construct it", importPrefix+name)
	}
	for l := imported.firstLine; l <= imported.lastLine; l++ {
		drop[l] = true
	}
	var kept []string
	for i, line := range strings.SplitAfter(registry, "\n") {
		if !drop[i+1] {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, ""), nil
}

// overlay is the JSON go's -overlay takes: the registry replaced by edited, and every file of the adapter deleted.
func overlay(registryPath, edited string, deleted []string) ([]byte, error) {
	replace := map[string]string{registryPath: edited}
	for _, f := range deleted {
		replace[f] = ""
	}
	return jsonx.MarshalCanonical(map[string]map[string]string{"Replace": replace})
}

func checkRemoval(root, registry, name string) error {
	edited, err := withoutAdapter(registry, name)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	deleted, err := filesUnder(filepath.Join(root, filepath.FromSlash(adaptersDir), name))
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "removable-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch) //nolint:errcheck // a leftover temporary directory is not worth failing the check
	editedPath := filepath.Join(scratch, "registry.go")
	configPath := filepath.Join(scratch, "overlay.json")
	config, err := overlay(filepath.Join(root, filepath.FromSlash(registryFile)), editedPath, deleted)
	if err != nil {
		return err
	}
	for path, content := range map[string][]byte{editedPath: []byte(edited), configPath: config} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return err
		}
	}
	var failures []error
	for _, tool := range []string{"build", "vet"} {
		cmd := exec.CommandContext(context.Background(), "go", tool, "-overlay", configPath, "./...") //nolint:gosec // go, a fixed subcommand, and a file made above
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			failures = append(failures, fmt.Errorf("go %s: %w: %s", tool, err, strings.TrimSpace(string(out))))
		}
	}
	return errors.Join(failures...)
}

// filesUnder lists every file below dir.
func filesUnder(dir string) (files []string, err error) {
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error { //nolint:gosec // the developer names the repository root
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return err
	})
	return files, err
}
