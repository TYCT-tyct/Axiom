package main

import (
	"net/http"
	"testing"
	"uuid"

	"axiom/internal/platform/consoleapi"
	"axiom/internal/routing"
)

// TestNewKeysTakeTheDefaultPreset: a new call key is bound to the routing preset of the deepest layer that has a
// default, by the console and by a management key alike; a later change of the default binds the next keys only, and a
// default the key cannot use refuses the key instead of binding it to a wider layer's (docs/domains/routing.md「默认链与
// key 落章」). A management key is never bound.
func TestNewKeysTakeTheDefaultPreset(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	account, site := f.account(s)
	presetOf := func(name string) string {
		t.Helper()
		return f.text(`SELECT coalesce(preset_id::text, 'none') FROM accounts.api_keys WHERE name = '` + name + `'`)
	}
	f.createKey(s, `{"name":"before"}`)
	if got := presetOf("before"); got != uuid.UUID(routing.SystemPreset).String() {
		t.Errorf("a key with only the platform's default: %s, want the system preset", got)
	}
	manager := f.managementKey(s, `"name":"manager"`)
	if got := presetOf("manager"); got != "none" {
		t.Errorf("a management key is bound to %s", got)
	}

	strict, disabled := uuid.NewV7(), uuid.NewV7()
	f.run(`INSERT INTO routing.presets (id, scope, site_id, account_id, slug, name, current_version, deleted_at) VALUES
			('` + strict.String() + `', 'account', '` + site.String() + `', '` + account.String() + `', 'strict', 'Strict', 1, NULL),
			('` + disabled.String() + `', 'account', '` + site.String() + `', '` + account.String() + `', 'gone', 'Gone', 1, now());
		INSERT INTO routing.preset_versions (preset_id, version, rules) VALUES
			('` + strict.String() + `', 1, '{"default": {"classes": ["official"]}}'), ('` + disabled.String() + `', 1, '{"default": {}}');
		INSERT INTO routing.preset_defaults (scope, site_id, account_id, preset_id)
			VALUES ('account', '` + site.String() + `', '` + account.String() + `', '` + strict.String() + `')`)
	f.createKey(s, `{"name":"after"}`)
	if _, secret := readManaged(t, f.managementRequest(http.MethodPost, "", `{"name":"managed"}`, manager.Secret), http.StatusCreated); secret == "" {
		t.Fatal("the management key made no key")
	}
	for name, want := range map[string]string{
		"before": uuid.UUID(routing.SystemPreset).String(), "after": strict.String(), "managed": strict.String(),
	} {
		if got := presetOf(name); got != want {
			t.Errorf("key %s is bound to %s, want %s", name, got, want)
		}
	}

	f.run(`UPDATE routing.preset_defaults SET preset_id = '` + disabled.String() + `' WHERE account_id = '` + account.String() + `'`)
	expect(t, "a key under a disabled default", f.do(http.MethodPost, "/keys", `{"name":"refused"}`, as(s)),
		http.StatusConflict, consoleapi.ErrorCodePresetDefaultInvalid)
	if rec := f.managementRequest(http.MethodPost, "", `{"name":"refused too"}`, manager.Secret); rec.Code != http.StatusConflict {
		t.Errorf("a managed key under a disabled default: %d %s", rec.Code, rec.Body)
	}
	if got := f.text(`SELECT count(*)::text FROM accounts.api_keys WHERE name LIKE 'refused%'`); got != "0" {
		t.Errorf("%s refused keys were made", got)
	}
}
