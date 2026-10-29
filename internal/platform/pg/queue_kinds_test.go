package pg

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestEveryJobKindNamesOneOfTheQueues is the table of every job kind of the program and the queue it runs on. The kinds
// are read out of the source, so a kind added without a queue fails here: River would put it on its default queue, whose
// single worker only drains old jobs, so the kind would lose the isolation the split exists for
// (internal/platform/README.md「任务队列」).
func TestEveryJobKindNamesOneOfTheQueues(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"accounts.password_reset":         QueueNotifications,
		"accounts.password_reset_cleanup": QueueArchive,
		"billing.expire_credit":           QueueMoney,
		"billing.reconcile":               QueueMoney,
		"catalog.sync":                    QueueSyncs,
		"mail.send":                       QueueNotifications,
		"mail.send_tracked":               QueueNotifications,
		"observability.alerts":            QueueNotifications,
		"observability.webhook":           QueueNotifications,
		"payments.poll":                   QueueMoney,
		"queue.discard_scan":              QueueArchive,
		"suppliers.reconcile":             QueueMoney,
		"suppliers.sync":                  QueueSyncs,
		"suppliers.sync_sweep":            QueueSyncs,
		"suppliers.sync_alerts":           QueueNotifications,
	}
	got := jobKinds(t, repoRoot(t))
	named := map[string]bool{}
	for _, queue := range queues {
		named[queue.name] = true
	}
	for _, kind := range slices.Sorted(maps.Keys(got)) {
		switch queue := got[kind]; {
		case queue == "":
			t.Errorf("job kind %q names no queue: name one in its InsertOpts", kind)
		case !named[queue]:
			t.Errorf("job kind %q is on queue %q, not one of the queues a kind may name", kind, queue)
		case want[kind] == "":
			t.Errorf("job kind %q is on queue %q but has no row in the table: add it with the queue it runs on", kind, queue)
		case queue != want[kind]:
			t.Errorf("job kind %q is on queue %q, want %q", kind, queue, want[kind])
		}
	}
	for _, kind := range slices.Sorted(maps.Keys(want)) {
		if _, ok := got[kind]; !ok {
			t.Errorf("job kind %q is in the table but no Kind() method of the source returns it", kind)
		}
	}
}

// TestTheKindScanFailsWhereItCannotSee: the two inputs a scan that stays quiet would let through (internal/platform/
// README.md「任务队列」). A Kind() it cannot read is an error, not a skipped kind; and a type's own InsertOpts counts
// even when it names no queue, since it shadows the one of an embedded type and the job lands on River's default queue.
func TestTheKindScanFailsWhereItCannotSee(t *testing.T) {
	t.Parallel()
	const embedded = `package x
import "github.com/riverqueue/river"
type single struct{}
func (single) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: "money"} }
`
	tests := []struct {
		name         string
		source       string
		wantProblems int
		wantQueue    string // of kind "k"
	}{
		{"a literal kind and the embedded queue", embedded + `type a struct{ single }
func (a) Kind() string { return "k" }`, 0, "money"},
		{"a constant kind", embedded + `const kindK = "k"
type a struct{ single }
func (a) Kind() string { return kindK }`, 0, "money"},
		{"a kind returned from a call", embedded + `type a struct{ single }
func (a) Kind() string { return name() }`, 1, ""},
		{"an own InsertOpts without a queue shadows the embedded one", embedded + `type a struct{ single }
func (a) Kind() string { return "k" }
func (a) InsertOpts() river.InsertOpts { return river.InsertOpts{} }`, 0, ""},
		{"an own InsertOpts with a queue", embedded + `type a struct{ single }
func (a) Kind() string { return "k" }
func (a) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: "syncs"} }`, 0, "syncs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "x.go")
			if err := os.WriteFile(path, []byte(tt.source), 0o600); err != nil {
				t.Fatal(err)
			}
			consts := map[string]string{}
			readConstants(t, []string{path}, consts)
			kinds, problems := packageJobKinds(t, []string{path}, consts)
			if len(problems) != tt.wantProblems {
				t.Errorf("problems = %q, want %d", problems, tt.wantProblems)
			}
			if tt.wantProblems == 0 && kinds["k"] != tt.wantQueue {
				t.Errorf("kind k is on queue %q, want %q", kinds["k"], tt.wantQueue)
			}
		})
	}
}

// repoRoot is the repository root, three directories above this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("the repository root is not at %s: %v", root, err)
	}
	return root
}

// jobKinds is the queue every job kind of the source names: its Kind() string method, and the Queue of its InsertOpts,
// its own or the one it gets from an embedded type.
func jobKinds(t *testing.T, root string) map[string]string {
	t.Helper()
	files := sourceFiles(t, root)
	consts := map[string]string{} // the source's string constants, such as QueueMoney -> "money"
	// Every constant first: a queue field in one package names a constant of platform/pg, which may be walked after it.
	for _, dir := range slices.Sorted(maps.Keys(files)) {
		readConstants(t, files[dir], consts)
	}
	found := map[string]string{}
	for _, dir := range slices.Sorted(maps.Keys(files)) {
		kinds, problems := packageJobKinds(t, files[dir], consts)
		maps.Copy(found, kinds)
		for _, problem := range problems {
			t.Error(problem)
		}
	}
	return found
}

// sourceFiles is every non-test .go file under internal/ and cmd/, by directory.
func sourceFiles(t *testing.T, root string) map[string][]string {
	t.Helper()
	files := map[string][]string{}
	for _, tree := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			files[filepath.Dir(path)] = append(files[filepath.Dir(path)], path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

// parse is one file of the source, for its declarations.
func parse(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

// readConstants records the string constants of one package's files, such as QueueMoney = "money".
func readConstants(t *testing.T, files []string, consts map[string]string) {
	t.Helper()
	for _, path := range files {
		for _, spec := range constSpecs(parse(t, path)) {
			constant(spec, consts)
		}
	}
}

// constSpecs is one file's constant declarations.
func constSpecs(file *ast.File) []*ast.ValueSpec {
	var specs []*ast.ValueSpec
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			if spec, ok := spec.(*ast.ValueSpec); ok {
				specs = append(specs, spec)
			}
		}
	}
	return specs
}

// packageJobs is what one package's source says about its jobs.
type packageJobs struct {
	kinds    map[string]string   // args type -> kind
	problems []string            // Kind methods whose string the scan cannot read
	queues   map[string]string   // args type -> the queue its InsertOpts names
	embedded map[string][]string // args type -> its embedded types
}

// packageJobKinds reads one package's job kinds, resolving a queue field such as pg.QueueMoney through consts, and lists
// the Kind methods it cannot resolve: a kind the scan skips would escape the table.
func packageJobKinds(t *testing.T, files []string, consts map[string]string) (map[string]string, []string) {
	t.Helper()
	p := &packageJobs{kinds: map[string]string{}, queues: map[string]string{}, embedded: map[string][]string{}}
	for _, path := range files {
		for _, decl := range parse(t, path).Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				p.readMethod(decl, consts)
			case *ast.GenDecl:
				p.readTypes(decl)
			}
		}
	}
	found := map[string]string{}
	for typ, kind := range p.kinds {
		found[kind] = p.queueOf(typ, consts)
	}
	return found, p.problems
}

// readMethod records a job args type's kind, or whether its InsertOpts names a queue: an InsertOpts method of the type
// itself is recorded even when it names none, since it shadows the one of an embedded type.
func (p *packageJobs) readMethod(decl *ast.FuncDecl, consts map[string]string) {
	if decl.Recv == nil || len(decl.Recv.List) != 1 || decl.Body == nil {
		return
	}
	typ := receiverName(decl.Recv.List[0].Type)
	switch decl.Name.Name {
	case "Kind":
		if !isKindSignature(decl.Type) {
			return
		}
		if kind, ok := kindValue(decl, consts); ok {
			p.kinds[typ] = kind
		} else {
			p.problems = append(p.problems, fmt.Sprintf("the Kind method of %s returns something the table test cannot read: return a string literal or a constant", typ))
		}
	case "InsertOpts":
		queue, _ := queueField(decl)
		p.queues[typ] = queue
	}
}

// readTypes records the types a struct type embeds.
func (p *packageJobs) readTypes(decl *ast.GenDecl) {
	for _, spec := range decl.Specs {
		if spec, ok := spec.(*ast.TypeSpec); ok {
			p.embed(spec)
		}
	}
}

// kindValue is the string a Kind() method returns, written as a literal or as a constant.
func kindValue(fn *ast.FuncDecl, consts map[string]string) (string, bool) {
	for _, stmt := range fn.Body.List {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		if kind, ok := stringLiteral(ret.Results[0]); ok {
			return kind, true
		}
		if name, ok := ret.Results[0].(*ast.Ident); ok {
			kind, ok := consts[name.Name]
			return kind, ok
		}
	}
	return "", false
}

// isKindSignature is whether a method takes nothing and returns one string, the shape of Kind().
func isKindSignature(signature *ast.FuncType) bool {
	if signature.Params != nil && len(signature.Params.List) != 0 {
		return false
	}
	if signature.Results == nil || len(signature.Results.List) != 1 {
		return false
	}
	result, ok := signature.Results.List[0].Type.(*ast.Ident)
	return ok && result.Name == "string"
}

// stringLiteral is the text of a string literal, such as "money".
func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	text, err := strconv.Unquote(lit.Value)
	return text, err == nil
}

// queueField is the queue an InsertOpts method names in its river.InsertOpts literal.
func queueField(fn *ast.FuncDecl) (string, bool) {
	var queue string
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		lit, ok := node.(*ast.CompositeLit)
		if !ok || queue != "" {
			return queue == ""
		}
		if typ, ok := lit.Type.(*ast.SelectorExpr); !ok || typ.Sel.Name != "InsertOpts" {
			return true
		}
		for _, element := range lit.Elts {
			field, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := field.Key.(*ast.Ident); ok && key.Name == "Queue" {
				queue = queueName(field.Value)
			}
		}
		return true
	})
	return queue, queue != ""
}

// queueName is what a queue field's value is written as: the constant's name or the literal's text.
func queueName(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.SelectorExpr:
		return expr.Sel.Name
	case *ast.Ident:
		return expr.Name
	case *ast.BasicLit:
		if value, err := strconv.Unquote(expr.Value); err == nil {
			return value
		}
	}
	return ""
}

// embed records the types a struct embeds, so that a job args type inherits its InsertOpts the way the compiler makes
// it inherit the method.
func (p *packageJobs) embed(spec *ast.TypeSpec) {
	structure, ok := spec.Type.(*ast.StructType)
	if !ok {
		return
	}
	for _, field := range structure.Fields.List {
		if len(field.Names) == 0 {
			p.embedded[spec.Name.Name] = append(p.embedded[spec.Name.Name], receiverName(field.Type))
		}
	}
}

// constant records a string constant, such as QueueMoney = "money".
func constant(spec *ast.ValueSpec, consts map[string]string) {
	if len(spec.Names) != 1 || len(spec.Values) != 1 {
		return
	}
	lit, ok := spec.Values[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return
	}
	if value, err := strconv.Unquote(lit.Value); err == nil {
		consts[spec.Names[0].Name] = value
	}
}

// queueOf is the queue a job args type names, following the embedded types it inherits InsertOpts from.
func (p *packageJobs) queueOf(typ string, consts map[string]string) string {
	seen := map[string]bool{}
	for typ != "" && !seen[typ] {
		seen[typ] = true
		if queue, ok := p.queues[typ]; ok {
			if value, ok := consts[queue]; ok {
				return value
			}
			return queue
		}
		if embedded := p.embedded[typ]; len(embedded) > 0 {
			typ = embedded[0]
		} else {
			typ = ""
		}
	}
	return ""
}

// receiverName is the type a method or embedded field is declared on.
func receiverName(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.StarExpr:
		return receiverName(expr.X)
	case *ast.IndexExpr:
		return receiverName(expr.X)
	case *ast.IndexListExpr:
		return receiverName(expr.X)
	}
	return ""
}
