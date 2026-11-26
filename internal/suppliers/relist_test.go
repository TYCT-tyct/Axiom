package suppliers

import (
	"context"
	"log/slog"
	"testing"
)

// TestPastedLinesFollowTheirKeysAndTheCatalog: the re-list and the re-projection (docs/domains/suppliers-direct.md
// 「模型重读与余额」). Once the provider lists a new model for a key and the catalog lists it, lists a model a name was
// waiting for and gains a new price, the hourly re-list gives the line an endpoint for each, priced at the type's
// price times the line's ratio, the key serves the new name, and a key the provider now refuses is rejected; a later
// revision of the catalog is projected again by the minute job. An account's own key is never touched.
func TestPastedLinesFollowTheirKeysAndTheCatalog(t *testing.T) {
	t.Parallel()
	w := newPastedWorld(t)
	w.provider.set("sk-good-listing", providerKey{models: []string{"gravel", "stone"}})
	w.provider.set("sk-good-revoked", providerKey{models: []string{"stone"}})
	line := w.connect("pasted", "0.8", "sk-good-listing", "sk-good-revoked")
	_, ownPlans := w.ownKey()
	before := ownPlans()
	jobs := &lines{db: w.db, scope: platformScope, admin: w.admin, cat: w.cat, logger: slog.Default()}

	prices := map[string]string{
		"stone":  `{"v":1,"currency":"USD","rates":{"input":"0.000004","output":"0.00002","cache_read":"0.0000004"}}`,
		"gravel": `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_read":"0.0000001"}}`,
		"pebble": `{"v":1,"currency":"USD","rates":{"input":"0.000002","output":"0.000008","cache_read":"0.000000123456789"}}`,
	}
	for _, step := range []struct {
		name   string
		change func()
		run    func(context.Context) error
		priced []string // the models whose endpoints are priced at prices after the step
	}{
		{"re-listed", func() {
			w.provider.set("sk-good-listing", providerKey{models: []string{"gravel", "pebble", "stone"}})
			w.provider.set("sk-good-revoked", providerKey{})
			w.list("gravel", prices["gravel"])
			w.list("pebble", prices["pebble"])
			w.price(w.models["stone"], prices["stone"])
		}, jobs.relist, []string{"stone", "gravel", "pebble"}},
		{"projected at a new revision", func() {
			prices["stone"] = `{"v":1,"currency":"USD","rates":{"input":"0.000005","output":"0.000025","cache_read":"0.0000005"}}`
			w.price(w.models["stone"], prices["stone"])
		}, jobs.reproject, []string{"stone"}},
	} {
		step.change()
		if err := step.run(t.Context()); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		for _, model := range step.priced {
			w.expectPriced(step.name+": "+model, line, w.endpoint(line, model), prices[model], "0.8")
		}
	}
	if got := w.text("SELECT array_to_string(allowed_models, ',') FROM suppliers.credentials WHERE supplier_id = $1 AND status = 'active'", line); got != "gravel,pebble,stone" {
		t.Errorf("the key that listed again serves %s", got)
	}
	if got := w.text("SELECT string_agg(status, ',' ORDER BY status) FROM suppliers.credentials WHERE supplier_id = $1", line); got != "active,rejected" {
		t.Errorf("the line's keys are %s, want the refused one rejected", got)
	}
	if after := ownPlans(); after != before {
		t.Errorf("an account's own key went from plans %s to %s", before, after)
	}
}
