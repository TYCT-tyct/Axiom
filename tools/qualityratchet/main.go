// Command qualityratchet measures the quality ratchet's numbers for the tree named by -root: nolint directives per
// linter, functions over gocognit's configured threshold, files over the length limit, and, with -coverage, statement
// coverage per package from a `go test -coverprofile` profile. Test functions are counted separately from non-test
// functions, so the same threshold can ratchet both while the test hard ceiling stays lower.
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
// raise nor lower a package's number; a package with a hand-written function body containing statements but no test files
// is reported as zero, so a new package without tests cannot look measured. A package with no statements has no coverage
// to rate and is omitted, including one whose only profile blocks have zero statements. The withcluster test command is
// left out because its test skips when Ryuk is disabled, so its coverage would differ between CI and a local run.
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
	"golang.org/x/mod/modfile"
)

const (
	cognitiveLimit = 15
	fileLimit      = 500
)

// coverageExcluded is test tooling whose tests skip when Ryuk is disabled, so its coverage differs between CI
// (TESTCONTAINERS_RYUK_DISABLED=true) and a local run. It is still measured for nolint, complexity and file length.
var coverageExcluded = map[string]bool{
	"internal/platform/pgtest/cmd/withcluster": true,
}

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
	NolintDirectives    int                        `json:"nolint_directives"`
	Nolint              map[string]int             `json:"nolint"`
	FunctionsOver15     []functionMetric           `json:"functions_over_15"`
	TestFunctionsOver15 []functionMetric           `json:"test_functions_over_15"`
	FilesOver500        []fileMetric               `json:"files_over_500"`
	Coverage            map[string]packageCoverage `json:"coverage"`
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
	code      map[string]bool // directories with a statement in a hand-written function body, so zero coverage is a measurement
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

// own reports whether dir is measured: it belongs to the module rather than to a nested one such as tools/, and it is
// not test tooling left out of coverage.
func (s *sourceTree) own(dir string) bool {
	if coverageExcluded[dir] {
		return false
	}
	for nested := range s.nested {
		if dir == nested || strings.HasPrefix(dir, nested+"/") {
			return false
		}
	}
	return true
}

func measure(root, coverage string) (measurement, error) {
	result := measurement{
		Nolint:              map[string]int{},
		FunctionsOver15:     []functionMetric{},
		TestFunctionsOver15: []functionMetric{},
		FilesOver500:        []fileMetric{},
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
	sort.Slice(result.TestFunctionsOver15, func(i, j int) bool {
		return result.TestFunctionsOver15[i].Name < result.TestFunctionsOver15[j].Name
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
	if !strings.HasSuffix(filePath, ".go") {
		return nil
	}
	if strings.HasSuffix(filePath, ".gen.go") {
		sources.generated[filePath] = true
		return nil
	}
	return measureFile(tree, fset, filePath, strings.HasSuffix(filePath, "_test.go"), result, sources)
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

func measureFile(
	tree fs.FS, fset *token.FileSet, filePath string, isTest bool, result *measurement, sources *sourceTree,
) error {
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
	if isTest {
		countTestComplexity(file, filePath, result)
		return nil
	}
	measureProduction(file, filePath, source, sources.own(path.Dir(filePath)), result, sources)
	return nil
}

// measureProduction records a non-test file's nolint directives, length and functions over the limit, and whether the
// file holds code that coverage can measure.
func measureProduction(file *ast.File, filePath string, source []byte, measured bool, result *measurement, sources *sourceTree) {
	dir := path.Dir(filePath)
	if measured {
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
		if function.Body != nil && len(function.Body.List) > 0 && measured {
			sources.code[dir] = true
		}
		if complexity := gocognit.Complexity(function); complexity > cognitiveLimit {
			result.FunctionsOver15 = append(result.FunctionsOver15, functionMetric{
				Name:       filePath + ":" + functionName(function),
				Complexity: complexity,
			})
		}
	}
}

// countTestComplexity records the test functions over the limit. Nolint directives and file length stay non-test: the
// ratchet's existing counts are about production code, and the test half is only about how hard a test is to read.
func countTestComplexity(file *ast.File, filePath string, result *measurement) {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if complexity := gocognit.Complexity(function); complexity > cognitiveLimit {
			result.TestFunctionsOver15 = append(result.TestFunctionsOver15, functionMetric{
				Name:       filePath + ":" + functionName(function),
				Complexity: complexity,
			})
		}
	}
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
// named by the tree with statements but absent from the profile has no test files and is reported as zero; generated
// code is dropped from both numerator and denominator so it can neither raise nor lower the number.
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
	// A package whose profile records only zero-statement blocks (an empty function, for example) has no coverage to
	// ratchet. Remember that the profile named it: adding it back below would turn a package with nothing to measure
	// into a new package below the floor.
	seen := make(map[string]bool, len(coverage))
	for name := range coverage {
		seen[name] = true
		if coverage[name].Total == 0 {
			delete(coverage, name)
		}
	}
	// A package with statements in a hand-written function body but no test files never reaches the profile. It is
	// measured at zero so that a new package without tests cannot pass as unmeasured.
	for dir := range sources.code {
		name := packageName(module, dir)
		if _, ok := coverage[name]; ok || seen[name] {
			continue
		}
		coverage[name] = packageCoverage{}
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
	if coverageExcluded[dir] {
		return nil
	}
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
	if module := modfile.ModulePath(source); module != "" {
		return module, nil
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
