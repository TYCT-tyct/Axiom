// Command checkmodules fails when Go code crosses a module boundary that docs/architecture.md does not allow.
//
// The table under tableHeading in docs/architecture.md is the only statement of which module may import which. Rows
// go from the bottom layer up: a row may list only modules in rows above it, so the table cannot have a cycle, and the
// first row, platform, may be imported by every module without being listed. Every .go file of the main module is
// read with the Go parser, whatever its build constraints, so code selected only by a tag or an OS is checked too.
// Go code lives under cmd/, which assembles the program and may import any module, or under internal/<module>/; nothing
// outside cmd/ may import a package outside internal/<module>/. Go's internal/ rule already stops one module from
// importing another's implementation.
//
// usage: go run -C tools ./checkmodules <repository root>
package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	modulePath   = "axiom"
	tableHeading = "### 模块之间的依赖"
	shared       = "platform"
)

// excluded names directories that hold no code of the main module: .git is not code, vendor and node_modules hold
// third-party code. Skipping them is safe because no package may import from them.
var excluded = map[string]bool{".git": true, "vendor": true, "node_modules": true}

var (
	row  = regexp.MustCompile("^\\|\\s*`([a-z]+)`\\s*\\|([^|]*)\\|")
	name = regexp.MustCompile("`([a-z]+)`")
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: checkmodules <repository root>")
		os.Exit(2)
	}
	problems, err := check(os.DirFS(os.Args[1]))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, problem := range problems {
		fmt.Println(problem)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}

func check(repo fs.FS) ([]string, error) {
	doc, err := fs.ReadFile(repo, "docs/architecture.md")
	if err != nil {
		return nil, fmt.Errorf("read the dependency table: %w", err)
	}
	table, problems := readTable(string(doc))
	if len(problems) > 0 {
		return problems, nil
	}
	packages, links, err := packageImports(repo)
	if err != nil {
		return nil, err
	}
	return append(links, importProblems(table, packages)...), nil
}

type tableRow struct {
	module string
	listed []string
}

// readTable returns, for each module in the dependency table, the modules it may import.
func readTable(text string) (map[string]map[string]bool, []string) {
	rows, found := tableRows(text)
	switch {
	case !found:
		return nil, []string{fmt.Sprintf("docs/architecture.md: no heading %q", tableHeading)}
	case len(rows) == 0:
		return nil, []string{fmt.Sprintf("docs/architecture.md: no module rows under %q", tableHeading)}
	}
	var problems []string
	if rows[0].module != shared {
		problems = append(problems, fmt.Sprintf("the first row must be %s, not %s", shared, rows[0].module))
	}
	table := map[string]map[string]bool{}
	for _, r := range rows {
		if table[r.module] != nil {
			problems = append(problems, r.module+" has two rows")
			continue
		}
		allowed := map[string]bool{}
		if len(table) > 0 {
			allowed[shared] = true
		}
		for _, listed := range r.listed {
			if table[listed] == nil {
				problems = append(problems, fmt.Sprintf("%s may import %s, which is not a row above it", r.module, listed))
			}
			allowed[listed] = true
		}
		table[r.module] = allowed
	}
	return table, problems
}

// tableRows returns the rows under tableHeading up to the next heading, and whether the heading exists.
func tableRows(text string) ([]tableRow, bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start := slices.Index(lines, tableHeading)
	if start < 0 {
		return nil, false
	}
	var rows []tableRow
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "#") {
			break
		}
		if match := row.FindStringSubmatch(line); match != nil {
			r := tableRow{module: match[1]}
			for _, listed := range name.FindAllStringSubmatch(match[2], -1) {
				r.listed = append(r.listed, listed[1])
			}
			rows = append(rows, r)
		}
	}
	return rows, true
}

// packageImports returns the imports of every package in the main module, skipping nested modules, and a problem for
// every symbolic link: Go follows them on import but a directory walk does not, so they could hide code.
func packageImports(repo fs.FS) (map[string]map[string]bool, []string, error) {
	packages, links := map[string]map[string]bool{}, []string(nil)
	fset := token.NewFileSet()
	err := fs.WalkDir(repo, ".", func(file string, entry fs.DirEntry, err error) error {
		switch base := path.Base(file); {
		case err != nil:
			return err
		case entry.Type()&fs.ModeSymlink != 0:
			links = append(links, file+": symbolic links are not allowed in the main module")
			return nil
		case entry.IsDir():
			return skipDir(repo, file)
		case !strings.HasSuffix(base, ".go") || strings.HasPrefix(base, "_") || strings.HasPrefix(base, "."):
			return nil // the go command ignores files that start with _ or .
		}
		src, err := fs.ReadFile(repo, file)
		if err != nil {
			return err
		}
		imports, err := fileImports(fset, file, src)
		if err != nil {
			return err
		}
		pkg := path.Join(modulePath, path.Dir(file))
		if packages[pkg] == nil {
			packages[pkg] = map[string]bool{}
		}
		for _, imported := range imports {
			packages[pkg][imported] = true
		}
		return nil
	})
	return packages, links, err
}

// fileImports returns the import paths of one Go file, decoded the way the compiler decodes them.
func fileImports(fset *token.FileSet, file string, src []byte) ([]string, error) {
	parsed, err := parser.ParseFile(fset, file, src, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("parse imports: %w", err)
	}
	paths := make([]string, 0, len(parsed.Imports))
	for _, spec := range parsed.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: import %s: %w", file, spec.Path.Value, err)
		}
		paths = append(paths, imported)
	}
	return paths, nil
}

func skipDir(repo fs.FS, dir string) error {
	if dir == "." {
		return nil
	}
	if excluded[path.Base(dir)] {
		return fs.SkipDir
	}
	if info, err := fs.Stat(repo, path.Join(dir, "go.mod")); err == nil && !info.IsDir() {
		return fs.SkipDir // a nested module, such as tools/, has its own dependencies
	}
	return nil
}

// importProblems returns every import the table does not allow, sorted.
func importProblems(table, packages map[string]map[string]bool) []string {
	var problems []string
	for pkg, imports := range packages {
		problems = append(problems, packageProblems(table, pkg, imports)...)
	}
	sort.Strings(problems)
	return problems
}

func packageProblems(table map[string]map[string]bool, pkg string, imports map[string]bool) []string {
	if strings.HasPrefix(pkg, modulePath+"/cmd/") {
		return nil // the composition root wires every module together
	}
	module := moduleOf(pkg)
	switch {
	case module == "":
		return []string{pkg + ": Go code belongs under cmd/ or internal/<module>/"}
	case table[module] == nil:
		return []string{fmt.Sprintf("%s: module %s has no row in the table under %q", pkg, module, tableHeading)}
	}
	var problems []string
	for imported := range imports {
		if imported != modulePath && !strings.HasPrefix(imported, modulePath+"/") {
			continue
		}
		target := moduleOf(imported)
		if slices.ContainsFunc(strings.Split(imported, "/"), func(part string) bool { return excluded[part] }) {
			problems = append(problems, fmt.Sprintf("%s imports %s: this check does not read vendor or node_modules, so nothing may import from them", pkg, imported))
		} else if target == "" {
			problems = append(problems, fmt.Sprintf("%s imports %s: only cmd/ may import packages outside internal/<module>/", pkg, imported))
		} else if target != module && !table[module][target] {
			problems = append(problems, fmt.Sprintf("%s imports %s: %s may not import %s", pkg, imported, module, target))
		}
	}
	return problems
}

// moduleOf returns the module an axiom/internal/<module>/... package belongs to, or "" for any other package.
func moduleOf(pkg string) string {
	parts := strings.Split(pkg, "/")
	if len(parts) >= 3 && parts[0] == modulePath && parts[1] == "internal" {
		return parts[2]
	}
	return ""
}
