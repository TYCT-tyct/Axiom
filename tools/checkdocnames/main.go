// Command checkdocnames fails when a Markdown file names, in code formatting, a Go test or a Go declaration that does
// not exist.
//
// Docs that cite a test as the evidence for a rule, or a function as where a rule lives, rot silently when the code is
// renamed: three blocking review findings in one batch (#371, #375, #377) were exactly that. Two forms are checked, the
// ones that cannot be anything else:
//   - a test, benchmark, fuzz or example name (`TestX`, `TestX/sub`, `BenchmarkY`) must be declared in some Go file;
//     a trailing * (`TestDiffHarness*`) matches any declaration with that prefix. Any declaration, not only functions:
//     production code has exported names that start with Test (suppliers' Service.TestEndpoint, TestRequest), and a
//     doc naming one of them cannot be told apart from a doc naming a test
//   - `pkg.Name` with an exported Name, where pkg is the name of one of our Go packages, must be a package-level
//     declaration of that package or a method of one of its types
//
// Everything else in backticks (SQL objects, planned designs, other languages' names) is left alone: a lowercase
// `schema.table` may be a design not built yet, and telling it apart would take reading the prose. Declarations come
// from the Go parser, so struct fields and local names never count.
//
// usage: go run -C tools ./checkdocnames <repository root>
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	code      = regexp.MustCompile("`([^`\n]+)`")
	test      = regexp.MustCompile(`\b((?:Test|Benchmark|Fuzz|Example)[A-Z_]\w*)(\*?)`)
	qualified = regexp.MustCompile(`^(\w+)\.([A-Z]\w*)(?:\(.*\))?$`)
	skipped   = []string{".git", "node_modules", "testdata"} // testdata may hold Go that does not parse on purpose
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: checkdocnames <repository root>")
		os.Exit(2)
	}
	declared, docs, err := read(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	found := problems(docs, declared)
	for _, p := range found {
		fmt.Println(p)
	}
	if len(found) > 0 {
		fmt.Println("rename the reference to what the code declares now, or drop the code formatting for a name that is only planned")
		os.Exit(1)
	}
}

// read returns the declarations of every Go file under root, by package name, and every Markdown file's text by path.
func read(root string) (map[string]map[string]bool, map[string]string, error) {
	declared, docs := map[string]map[string]bool{}, map[string]string{}
	tree := os.DirFS(root)
	err := fs.WalkDir(tree, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && slices.Contains(skipped, d.Name()) {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".md") {
			return nil
		}
		text, err := fs.ReadFile(tree, path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".md") {
			docs[path] = string(text)
			return nil
		}
		return declareFile(declared, path, text)
	})
	return declared, docs, err
}

// declareFile adds a Go file's package-level names under its package; an external test package counts as its package.
func declareFile(declared map[string]map[string]bool, path string, text []byte) error {
	file, err := parser.ParseFile(token.NewFileSet(), path, text, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	pkg := strings.TrimSuffix(file.Name.Name, "_test")
	if declared[pkg] == nil {
		declared[pkg] = map[string]bool{}
	}
	declare(declared[pkg], file)
	return nil
}

// declare adds the file's package-level names: functions and methods, types, and every name of a var or const spec.
func declare(names map[string]bool, file *ast.File) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			names[d.Name.Name] = true
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					names[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, n := range s.Names {
						names[n.Name] = true
					}
				}
			}
		}
	}
}

// problems returns one line per unknown name, in the docs' path order.
func problems(docs map[string]string, declared map[string]map[string]bool) []string {
	var out []string
	paths := make([]string, 0, len(docs))
	for p := range docs {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, path := range paths {
		for n, line := range strings.Split(docs[path], "\n") {
			for _, m := range code.FindAllStringSubmatch(line, -1) {
				out = append(out, check(fmt.Sprintf("%s:%d", path, n+1), m[1], declared)...)
			}
		}
	}
	return out
}

func check(where, text string, declared map[string]map[string]bool) []string {
	for _, r := range text {
		if r > 127 { // a placeholder such as 包路径.TestName
			return nil
		}
	}
	var out []string
	for _, m := range test.FindAllStringSubmatch(text, -1) {
		if !anyDeclared(declared, m[1], m[2] == "*") {
			out = append(out, fmt.Sprintf("%s: `%s` names test %s%s, which no Go file declares", where, text, m[1], m[2]))
		}
	}
	if m := qualified.FindStringSubmatch(text); m != nil && declared[m[1]] != nil && !declared[m[1]][m[2]] {
		out = append(out, fmt.Sprintf("%s: `%s`: package %s declares no %s", where, text, m[1], m[2]))
	}
	return out
}

func anyDeclared(declared map[string]map[string]bool, name string, prefix bool) bool {
	for _, names := range declared {
		if names[name] {
			return true
		}
		if prefix {
			for n := range names {
				if strings.HasPrefix(n, name) {
					return true
				}
			}
		}
	}
	return false
}
