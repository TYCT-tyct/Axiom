// Command qualityratchet measures the quality ratchet's numbers for the tree named by -root: nolint directives per
// linter, functions over gocognit's configured threshold, and files over the length limit.
//
// gocognit is the library golangci-lint's gocognit linter uses, pinned in tools/go.mod, so the complexity numbers match
// lint. //gocognit:ignore is deliberately not honored: the ratchet must see what lint was told to skip. File length is
// every physical line, comments and blank lines included, which is stricter than revive's file-length-limit and is what
// the 2026-11-04 review counted.
//
// Tests are files ending in _test.go and anything under a testdata directory. Generated code is a file named *.gen.go
// (sqlc's output_files_suffix is .gen) or a Go file whose first declaration is preceded by the
// `// Code generated ... DO NOT EDIT.` header, which is what go/ast.IsGenerated and golangci-lint's generated-code
// exclusion recognize.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/uudashr/gocognit"
)

const (
	cognitiveLimit = 15
	fileLimit      = 500
)

type functionMetric struct {
	Name       string `json:"name"` // path:function, stable when lines move
	Complexity int    `json:"complexity"`
}

type fileMetric struct {
	Path  string `json:"path"`
	Lines int    `json:"lines"`
}

type measurement struct {
	NolintDirectives int              `json:"nolint_directives"`
	Nolint           map[string]int   `json:"nolint"`
	FunctionsOver15  []functionMetric `json:"functions_over_15"`
	FilesOver500     []fileMetric     `json:"files_over_500"`
}

func main() {
	root := flag.String("root", ".", "the tree to measure")
	flag.Parse()

	result, err := measure(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qualityratchet:", err)
		os.Exit(1)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "qualityratchet:", err)
		os.Exit(1)
	}
}

func measure(root string) (measurement, error) {
	result := measurement{
		Nolint:          map[string]int{},
		FunctionsOver15: []functionMetric{},
		FilesOver500:    []fileMetric{},
	}
	fset := token.NewFileSet()
	tree := os.DirFS(root)
	err := fs.WalkDir(tree, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".gen.go") {
			return nil
		}
		return measureFile(tree, fset, path, &result)
	})
	if err != nil {
		return measurement{}, err
	}

	sort.Slice(result.FunctionsOver15, func(i, j int) bool {
		return result.FunctionsOver15[i].Name < result.FunctionsOver15[j].Name
	})
	sort.Slice(result.FilesOver500, func(i, j int) bool {
		return result.FilesOver500[i].Path < result.FilesOver500[j].Path
	})
	return result, nil
}

func measureFile(tree fs.FS, fset *token.FileSet, path string, result *measurement) error {
	source, err := fs.ReadFile(tree, path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	file, err := parser.ParseFile(fset, path, source, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if ast.IsGenerated(file) {
		return nil
	}

	countNolint(file, result)
	if lines := lineCount(source); lines > fileLimit {
		result.FilesOver500 = append(result.FilesOver500, fileMetric{Path: path, Lines: lines})
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if complexity := gocognit.Complexity(function); complexity > cognitiveLimit {
			result.FunctionsOver15 = append(result.FunctionsOver15, functionMetric{
				Name:       path + ":" + functionName(function),
				Complexity: complexity,
			})
		}
	}
	return nil
}

func countNolint(file *ast.File, result *measurement) {
	for _, group := range file.Comments {
		for _, comment := range group.List {
			linters := nolintLinters(comment.Text)
			if len(linters) == 0 {
				continue
			}
			result.NolintDirectives++
			for _, linter := range linters {
				result.Nolint[linter]++
			}
		}
	}
}

var nolintPattern = regexp.MustCompile(`^nolint( |:|$)`)

// nolintLinters reads a comment the way golangci-lint's nolint filter does, and returns "all" for a directive that
// names no linter or all of them.
func nolintLinters(text string) []string {
	text = strings.TrimLeft(text, "/ ")
	if !nolintPattern.MatchString(text) {
		return nil
	}
	if strings.HasPrefix(text, "nolint:all") || !strings.HasPrefix(text, "nolint:") {
		return []string{"all"}
	}
	text, _, _ = strings.Cut(text, "//")

	seen := map[string]bool{}
	var linters []string
	for item := range strings.SplitSeq(strings.TrimPrefix(text, "nolint:"), ",") {
		linter := strings.ToLower(strings.TrimSpace(item))
		switch {
		case linter == "":
			continue
		case linter == "all":
			return []string{"all"}
		case !seen[linter]:
			seen[linter] = true
			linters = append(linters, linter)
		}
	}
	return linters
}

func lineCount(source []byte) int {
	lines := bytes.Count(source, []byte("\n"))
	if len(source) > 0 && source[len(source)-1] != '\n' {
		lines++
	}
	return lines
}

// functionName is gocognit's name for a function. It is copied because gocognit keeps its version unexported and
// ComplexityStats drops functions carrying //gocognit:ignore, which this measurement must not.
func functionName(function *ast.FuncDecl) string {
	if function.Recv != nil && function.Recv.NumFields() > 0 {
		return fmt.Sprintf("(%s).%s", receiver(function.Recv.List[0].Type), function.Name)
	}
	return function.Name.Name
}

func receiver(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return "*" + receiver(typed.X)
	case *ast.IndexExpr:
		return receiver(typed.X)
	case *ast.IndexListExpr:
		return receiver(typed.X)
	}
	return "BADRECV"
}
