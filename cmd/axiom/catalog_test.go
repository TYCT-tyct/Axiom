package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pgtest"
)

// catalogRunner runs `axiom catalog` against a fresh database with the request in a file, and returns the result line.
func catalogRunner(t *testing.T) func(operation, doc string) (map[string]any, error) {
	environ := []string{"AXIOM_DATABASE_URL=" + pgtest.New(t, modules()...).App}
	return func(operation, doc string) (map[string]any, error) {
		file := filepath.Join(t.TempDir(), "request.json")
		if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := catalogCommand(t.Context(), environ, []string{operation, file}, &out); err != nil {
			return nil, err
		}
		var result map[string]any
		if err := jsonx.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result, nil
	}
}

// TestCatalogCommand: the operations a file asks for run in the platform scope and are recorded, and a retry with the
// same key gets the first result.
func TestCatalogCommand(t *testing.T) {
	t.Parallel()
	run := catalogRunner(t)
	const author = `{"op_key":"0198c0a8-0000-7000-8000-000000000001","reason":"first vendor","slug":"acme","name":"Acme"}`
	first, err := run("save-author", author)
	if err != nil || first["replayed"] != false || first["subject"] != "acme" {
		t.Fatalf("first run: %v, %v", first, err)
	}
	again, err := run("save-author", author)
	if err != nil || again["replayed"] != true || again["change"] != first["change"] {
		t.Errorf("retry: %v, %v, want the first result replayed", again, err)
	}
	model, err := run("create-model", `{"op_key":"0198c0a8-0000-7000-8000-000000000002","reason":"draft","slug":"acme/alpha",
		"version_basis":"dated","price_source":"manual","spec":{"items":["input"],"tiers":[]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run("add-name", `{"op_key":"0198c0a8-0000-7000-8000-000000000003","reason":"standard id","model":"`+
		model["subject"].(string)+`","name":"acme/alpha","visibility":"public"}`); err != nil {
		t.Fatal(err)
	}
	_, err = run("list-model", `{"op_key":"0198c0a8-0000-7000-8000-00000000000a","reason":"r","model":"`+
		model["subject"].(string)+`","rev":0}`)
	if err == nil || !strings.Contains(err.Error(), "no reference price") {
		t.Errorf("listing a model without a price: %v", err)
	}
}

// TestCatalogCommandRefusals: a member the request does not name, an unknown operation and a missing file are errors.
func TestCatalogCommandRefusals(t *testing.T) {
	t.Parallel()
	run := catalogRunner(t)
	for _, c := range []struct{ name, operation, doc, want string }{
		{"an unknown member", "save-author", `{"op_key":"0198c0a8-0000-7000-8000-000000000009","reason":"r","slug":"x","name":"X","extra":1}`, "extra"},
		{"an unknown operation", "delete-everything", `{}`, "unknown operation"},
		{"a dataset request with an unknown member", "select-dataset-source", `{"op_key":"0198c0a8-0000-7000-8000-000000000007","reason":"r","model":"0198c0a8-0000-7000-8000-000000000006","source":"litellm","prices":[]}`, "prices"},
		{"a missing reason", "save-author", `{"op_key":"0198c0a8-0000-7000-8000-000000000008","slug":"x","name":"X"}`, "reason"},
	} {
		if _, err := run(c.operation, c.doc); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if err := catalogCommand(t.Context(), nil, []string{"retire"}, new(bytes.Buffer)); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("no file: %v", err)
	}
}

// dialectCatalog is a database with the vendor and a draft model that reasons, and the means to write a request file and
// run `axiom catalog` on it.
type dialectCatalog struct {
	t       *testing.T
	environ []string
}

func newDialectCatalog(t *testing.T) *dialectCatalog {
	d := &dialectCatalog{t: t, environ: []string{"AXIOM_DATABASE_URL=" + pgtest.New(t, modules()...).App}}
	for operation, doc := range map[string]string{
		"save-author": `{"op_key":"0198c0a8-0000-7000-8000-000000000001","reason":"vendor","slug":"anthropic","name":"Anthropic"}`,
		"create-model": `{"op_key":"0198c0a8-0000-7000-8000-000000000002","reason":"draft","slug":"anthropic/alpha","version_basis":"dated",
			"price_source":"manual","spec":{"items":["input"],"tiers":[],"abilities":["reasoning"]}}`,
	} {
		if _, err := d.run(operation, d.write(doc)); err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
	}
	return d
}

func (d *dialectCatalog) write(doc string) string {
	d.t.Helper()
	file := filepath.Join(d.t.TempDir(), "request.json")
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		d.t.Fatal(err)
	}
	return file
}

func (d *dialectCatalog) run(args ...string) (string, error) {
	var out bytes.Buffer
	err := catalogCommand(d.t.Context(), d.environ, args, &out)
	return out.String(), err
}

// TestCatalogApplyDialect: the command shows what the dialect file would write and writes nothing without --apply, writes
// it with --apply through the audited spec update, and a second run with it finds nothing to do.
func TestCatalogApplyDialect(t *testing.T) {
	t.Parallel()
	d := newDialectCatalog(t)
	good := d.write(`{"models":{"anthropic/alpha":{"thinking":"adaptive","mid_system":false},"anthropic/absent":{"mid_system":true}}}`)
	out, err := d.run("apply-dialect", good)
	if err != nil || !strings.Contains(out, `"applied":false`) || !strings.Contains(out, `"outcome":"set"`) || !strings.Contains(out, `"outcome":"absent"`) {
		t.Fatalf("the plan: %v, %s", err, out)
	}
	if out, err = d.run("apply-dialect", "--apply", good); err != nil || !strings.Contains(out, `"applied":true`) {
		t.Fatalf("the run: %v, %s", err, out)
	}
	if out, err = d.run("apply-dialect", "--apply", good); err != nil || strings.Contains(out, `"outcome":"set"`) || !strings.Contains(out, `"outcome":"unchanged"`) {
		t.Errorf("the second run: %v, %s", err, out)
	}
}

// TestCatalogApplyDialectRefusals: a model that does not take its keys fails the command, and so do a file it cannot read
// and a file with a model that sets no key.
func TestCatalogApplyDialectRefusals(t *testing.T) {
	t.Parallel()
	d := newDialectCatalog(t)
	if _, err := d.run("apply-dialect", "--apply", d.write(`{"models":{"anthropic/alpha":{"thinking":"none"}}}`)); err == nil || !strings.Contains(err.Error(), "1 models failed") {
		t.Errorf("a model that does not take its keys: %v", err)
	}
	for _, args := range [][]string{{"apply-dialect", "--apply"}, {"apply-dialect", filepath.Join(t.TempDir(), "none.json")}, {"apply-dialect", d.write(`{"models":{"a/b":{}}}`)}} {
		if _, err := d.run(args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}
