package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// TestAdminPendingOperationsAnswerNotImplemented: an operation whose handler has not landed (#948) sits behind the guard
// like every other and answers a valid request of a session with its permission with 501 not_implemented, in the
// contract's error body. The operations are the methods the admin_pending_*.go files declare, read from the source, so
// a unit that lands its handlers and deletes its file edits nothing here.
func TestAdminPendingOperationsAnswerNotImplemented(t *testing.T) {
	t.Parallel()
	pending := pendingOperations(t)
	f := newAdminFixture(t)
	s := f.owner()
	for _, op := range adminOperations {
		if !pending[strings.ToLower(op.name)] {
			continue
		}
		delete(pending, strings.ToLower(op.name))
		mods := []mod{as(s)}
		if op.idempotent {
			mods = append(mods, idempotent(newKey()))
		}
		expectAdmin(t, op.name, f.do(op.method, op.path, op.body, mods...), http.StatusNotImplemented, codeNotImplemented)
	}
	for name := range pending {
		t.Errorf("the pending %s is in no test table", name)
	}
}

// pendingOperations are the methods of the admin_pending_*.go files, by lower-case name.
func pendingOperations(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("admin_pending_*.go")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil {
				names[strings.ToLower(fn.Name.Name)] = true
			}
		}
	}
	return names
}
