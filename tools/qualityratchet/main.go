// Command qualityratchet measures the quality ratchet's numbers for the tree named by -root: nolint directives per
// linter, functions over gocognit's configured threshold, files over the length limit, and, with -coverage, statement
// coverage per package from a `go test -coverprofile` profile.
//
// gocognit is the library golangci-lint's gocognit linter uses, pinned in tools/go.mod, so the complexity numbers match
// lint. //gocognit:ignore is deliberately not honored: the ratchet must see what lint was told to skip. File length is
// every physical line, comments and blank lines included, which is stricter than revive's file-length-limit and is what
// the 2026-11-04 review counted.
//
// Tests are files ending in _test.go and anything under a testdata directory. Generated code is a file named *.gen.go
// (sqlc's output_files_suffix is .gen) or a Go file whose first declaration is preceded by the
// `// Code generated ... DO NOT EDIT.` header, which is what go/ast.IsGenerated and golangci-lint's generated-code
// exclusion recognize. Coverage drops generated code from both sides of the fraction, so a generated table can neither
// raise nor lower a package's number; a package with hand-written code but no test files is reported as zero, so a new
// package without tests cannot look measured. A package with no statements has no coverage to rate and is omitted.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

// packageCoverage is one package's statement coverage from the profile, generated code excluded.
type packageCoverage struct {
	Covered int `json:"covered"`
	Total   int `json:"total"`
}

type measurement struct {
	NolintDirectives int                        `json:"nolint_directives"`
	Nolint           map[string]int             `json:"nolint"`
	FunctionsOver15  []functionMetric           `json:"functions_over_15"`
	FilesOver500     []fileMetric               `json:"files_over_500"`
	Coverage         map[string]packageCoverage `json:"coverage"`
}

func main() {
	root := flag.String("root", ".", "the tree to measure")
	coverage := flag.String("coverage", "", "a `go test -coverprofile` profile to read statement coverage from")
	flag.Parse()

	result, err := measure(*root, *coverage)
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

// sourceTree is what the walk learns about the tree beyond the counts: the packages coverage can measure, the
// generated files it must drop, and the nested modules it must leave to their own go.mod.
type sourceTree struct {
	packages  map[string]bool // directories with hand-written, non-test Go
	code      map[string]bool // directories with a hand-written function body, so zero coverage is a measurement
	generated map[string]bool // files coverage must drop entirely
	nested    map[string]bool // directories holding another module's go.mod
}

func newSourceTree() *sourceTree {
	return &sourceTree{
		packages:  map[string]bool{},
		code:      map[string]bool{},
		generated: map[string]bool{},
		nested:    map[string]bool{},
	}
}

// own reports whether dir belongs to the measured module rather than to a nested one such as tools/.
func (s *sourceTree) own(dir string) bool {
	for nested := range s.nested {
		if dir == nested || strings.HasPrefix(dir, nested+"/") {
			return false
		}
	}
	return true
}

func measure(root, coverage string) (measurement, error) {
	result := measurement{
		Nolint:          map[string]int{},
		FunctionsOver15: []functionMetric{},
		FilesOver500:    []fileMetric{},
	}
	sources := newSourceTree()
	fset := token.NewFileSet()
	tree := os.DirFS(root)
	err := fs.WalkDir(tree, ".", func(filePath string, entry fs.DirEntry, walkErr error) error {
		return walk(tree, fset, filePath, entry, walkErr, &result, sources)
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
	if coverage != "" {
		result.Coverage, err = measureCoverage(root, coverage, sources)
		if err != nil {
			return measurement{}, err
		}
	}
	return result, nil
}

// walk measures one file and records the directories coverage can measure; nested modules stay out of coverage because
// the root test run does not build them.
func walk(
	tree fs.FS, fset *token.FileSet, filePath string, entry fs.DirEntry, walkErr error,
	result *measurement, sources *sourceTree,
) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		return walkDir(tree, filePath, entry, sources)
	}
	if !strings.HasSuffix(filePath, ".go") || strings.HasSuffix(filePath, "_test.go") {
		return nil
	}
	if strings.HasSuffix(filePath, ".gen.go") {
		sources.generated[filePath] = true
		return nil
	}
	return measureFile(tree, fset, filePath, result, sources)
}

func walkDir(tree fs.FS, filePath string, entry fs.DirEntry, sources *sourceTree) error {
	switch entry.Name() {
	case ".git", "node_modules", "vendor", "testdata":
		return fs.SkipDir
	}
	if filePath != "." {
		if _, err := fs.Stat(tree, path.Join(filePath, "go.mod")); err == nil {
			sources.nested[filePath] = true
		}
	}
	return nil
}

func measureFile(tree fs.FS, fset *token.FileSet, filePath string, result *measurement, sources *sourceTree) error {
	source, err := fs.ReadFile(tree, filePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", filePath, err)
	}
	file, err := parser.ParseFile(fset, filePath, source, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse %s: %w", filePath, err)
	}
	if ast.IsGenerated(file) {
		sources.generated[filePath] = true
		return nil
	}
	dir := path.Dir(filePath)
	if sources.own(dir) {
		sources.packages[dir] = true
	}

	countNolint(file, result)
	if lines := lineCount(source); lines > fileLimit {
		result.FilesOver500 = append(result.FilesOver500, fileMetric{Path: filePath, Lines: lines})
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if function.Body != nil && sources.own(dir) {
			sources.code[dir] = true
		}
		if complexity := gocognit.Complexity(function); complexity > cognitiveLimit {
			result.FunctionsOver15 = append(result.FunctionsOver15, functionMetric{
				Name:       filePath + ":" + functionName(function),
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

var profileLine = regexp.MustCompile(`^(.+):([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+) ([0-9]+) ([0-9]+)$`)

// measureCoverage reads a `go test -coverprofile` profile and sums each package's hand-written statements. A package
// named by the tree but absent from the profile has no test files and is reported as zero; generated code is dropped
// from both numerator and denominator so it can neither raise nor lower the number.
func measureCoverage(root, profile string, sources *sourceTree) (map[string]packageCoverage, error) {
	module, err := modulePath(root)
	if err != nil {
		return nil, err
	}
	source, err := fs.ReadFile(os.DirFS(filepath.Dir(profile)), filepath.Base(profile))
	if err != nil {
		return nil, fmt.Errorf("read the coverage profile: %w", err)
	}
	coverage, err := readProfile(bytes.NewReader(source), profile, module, sources)
	if err != nil {
		return nil, err
	}
	// A package with no statements (only types, or tests over generated code) has no coverage to ratchet.
	for name := range coverage {
		if coverage[name].Total == 0 {
			delete(coverage, name)
		}
	}
	// A package with hand-written function bodies but no test files never reaches the profile. It is measured at zero
	// so that a new package without tests cannot pass as unmeasured.
	for dir := range sources.code {
		if _, ok := coverage[packageName(module, dir)]; !ok {
			coverage[packageName(module, dir)] = packageCoverage{}
		}
	}
	return coverage, nil
}

// readProfile parses the profile's mode line and one block per line, skipping other modules and generated files.
func readProfile(reader io.Reader, profile, module string, sources *sourceTree) (map[string]packageCoverage, error) {
	coverage := map[string]packageCoverage{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if first {
			if !strings.HasPrefix(line, "mode: ") {
				return nil, fmt.Errorf("%s is not a coverage profile: first line %q", profile, line)
			}
			first = false
			continue
		}
		if err := addBlock(coverage, module, sources, profile, line); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read the coverage profile: %w", err)
	}
	if first {
		return nil, fmt.Errorf("%s is empty; it has no mode line", profile)
	}
	return coverage, nil
}

// addBlock adds one profile line to its package unless it belongs to another module or to generated code.
func addBlock(coverage map[string]packageCoverage, module string, sources *sourceTree, profile, line string) error {
	match := profileLine.FindStringSubmatch(line)
	if match == nil {
		return fmt.Errorf("%s: cannot read coverage line %q", profile, line)
	}
	statements, err := strconv.Atoi(match[6])
	if err != nil {
		return fmt.Errorf("%s: cannot read the statement count in %q: %w", profile, line, err)
	}
	count, err := strconv.Atoi(match[7])
	if err != nil {
		return fmt.Errorf("%s: cannot read the execution count in %q: %w", profile, line, err)
	}
	relative, ok := moduleRelative(module, match[1])
	if !ok {
		return nil // a dependency the profile happened to include, not this module
	}
	if sources.generated[relative] {
		return nil
	}
	dir := path.Dir(relative)
	if !sources.packages[dir] {
		return fmt.Errorf("%s names %s, but %s is not a hand-written package in the tree", profile, match[1], dir)
	}
	name := packageName(module, dir)
	entry := coverage[name]
	entry.Total += statements
	if count > 0 {
		entry.Covered += statements
	}
	coverage[name] = entry
	return nil
}

// packageName is the import path the profile uses for a directory under the module.
func packageName(module, dir string) string {
	if dir == "." {
		return module
	}
	return module + "/" + dir
}

// modulePath is the module line of the root go.mod, the prefix coverage profiles use for the tree.
func modulePath(root string) (string, error) {
	source, err := fs.ReadFile(os.DirFS(root), "go.mod")
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for line := range strings.SplitSeq(string(source), "\n") {
		line = strings.TrimSpace(line)
		if module, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(module), nil
		}
	}
	return "", fmt.Errorf("%s/go.mod has no module line", root)
}

// moduleRelative turns an import path in the profile into a path under the tree, and reports false for another module.
func moduleRelative(module, file string) (string, bool) {
	relative, ok := strings.CutPrefix(file, module+"/")
	if !ok || relative == "" {
		return "", false
	}
	return relative, true
}
