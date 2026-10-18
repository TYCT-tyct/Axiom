package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"axiom/internal/platform/pgtest"
)

// TestBillingCommand: an operation's file runs in the platform scope and a refused one fails; a listing writes a line
// per row; an unknown name or a missing file is a usage error. The operations are the two of the fallback prices and
// the unknown items: closing a hold went with the correction window (docs/domains/billing.md「用量来源与暂估」).
func TestBillingCommand(t *testing.T) {
	environ := []string{"AXIOM_DATABASE_URL=" + pgtest.New(t, modules()...).App}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := billingCommand(t.Context(), environ, args, &out)
		return out.String(), err
	}
	file := func(doc string) string {
		f := filepath.Join(t.TempDir(), "request.json")
		if err := os.WriteFile(f, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	save := `{"currency":"CNY","markup":"1.3","per_use":{"code_execution":"0.3","web_search":"0.2"},"reason":"CNY site"}`
	if out, err := run("save-fallbacks", file(save)); err != nil || out != `{"done":"save-fallbacks"}`+"\n" {
		t.Errorf("save-fallbacks = %q, %v", out, err)
	}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"a refused row":          {[]string{"save-fallbacks", file(strings.Replace(save, `"1.3"`, `"0.5"`, 1))}, "markup"},
		"nothing to clear":       {[]string{"clear-unknown-item", file(`{"model":"0198c0a8-0000-7000-8000-000000000009","field":"f","reason":"r"}`)}, "not found"},
		"an unknown operation":   {[]string{"drop-everything", file(`{}`)}, "unknown operation"},
		"a closing that is gone": {[]string{"close-hold", file(`{"request":"0198c0a8-0000-7000-8000-000000000009","reason":"r"}`)}, "unknown operation"},
		"an unknown listing":     {[]string{"everything"}, "unknown listing"},
		"an operation, no file":  {[]string{"clear-unknown-item"}, "unknown listing"},
		"too many arguments":     {[]string{"a", "b", "c"}, "usage"},
	} {
		if _, err := run(c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	for _, listing := range []string{"unknown-items"} {
		if out, err := run(listing); err != nil || out != "" {
			t.Errorf("%s = %q, %v; want no lines", listing, out, err)
		}
	}
}

// TestBillingCommandInANodeEnvironment: the command runs in a node's own environment (docker compose exec), where the
// node's variables are set too: only an invalid variable it reads is a fault.
func TestBillingCommandInANodeEnvironment(t *testing.T) {
	database := "AXIOM_DATABASE_URL=" + pgtest.New(t, modules()...).App
	run := func(extra ...string) error {
		return billingCommand(t.Context(), append([]string{database}, extra...), []string{"unknown-items"}, &bytes.Buffer{})
	}
	if err := run("AXIOM_ADMIN_ADDR=127.0.0.1:8082", "AXIOM_PUBLIC_ADDR=:8080"); err != nil {
		t.Errorf("a node's environment: %v", err)
	}
	if err := run("AXIOM_STARTUP_TIMEOUT=soon"); err == nil || !strings.Contains(err.Error(), "AXIOM_STARTUP_TIMEOUT") {
		t.Errorf("an invalid variable it reads: %v, want it named", err)
	}
}
