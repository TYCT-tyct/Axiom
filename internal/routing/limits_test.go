package routing_test

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

// TestLimitsCheck: a value outside its bounds, and a live limit above the layer's total once the defaults fill the
// rest, are refused by the field the admin API names; zero is the default and always passes.
func TestLimitsCheck(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		l     routing.Limits
		field string
	}{
		{"all defaults", routing.Limits{}, ""},
		{"every bound", routing.Limits{Platform: routing.LayerLimits{Active: 500, Total: 5000}, Workspace: routing.LayerLimits{Active: 1, Total: 1}, KeptVersions: 500}, ""},
		{"a live limit over its bound", routing.Limits{Site: routing.LayerLimits{Active: 501, Total: 5000}}, "site/active"},
		{"a total over its bound", routing.Limits{Account: routing.LayerLimits{Total: 5001}}, "account/total"},
		{"a negative total", routing.Limits{Workspace: routing.LayerLimits{Total: -1}}, "workspace/total"},
		{"fewer versions than the table keeps", routing.Limits{KeptVersions: 49}, "kept_versions"},
		{"more versions than the bound", routing.Limits{KeptVersions: 501}, "kept_versions"},
		{"a live limit above the default total", routing.Limits{Account: routing.LayerLimits{Active: 201}}, "account/active"},
		{"a total below the default live limit", routing.Limits{Platform: routing.LayerLimits{Total: 49}}, "platform/active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.l.Check()
			var limit *routing.LimitError
			if errors.As(err, &limit) != (tc.field != "") || tc.field != "" && limit.Field != tc.field {
				t.Errorf("Check = %v, want a refusal of %q", err, tc.field)
			}
		})
	}
}

// TestPresetLimitsApply: the limits the platform saves hold the next writes: an account's live and total presets, and
// the versions a preset keeps. A tenant cannot save them, and saving none goes back to the defaults; each change is
// audited.
func TestPresetLimitsApply(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	set := func(scope pg.Scope, l routing.Limits) error {
		return m.db.Tx(t.Context(), scope, func(tx pg.Tx) error { return routing.SetLimits(t.Context(), tx, m.actor, l, "tune") })
	}
	saved := routing.Limits{Account: routing.LayerLimits{Active: 2, Total: 3}, KeptVersions: 60}
	noErr(t, "save the limits", set(pg.PlatformScope(), saved))
	wantErr(t, "an account saving limits", set(pg.AccountScopeIn(m.site, m.account), routing.Limits{KeptVersions: 100}), routing.ErrPresetReadOnly)
	noErr(t, "read the limits", m.as(t, func(ctx context.Context, tx pg.Tx) error {
		read, err := routing.ReadLimits(ctx, tx)
		if read != saved {
			t.Errorf("an account reads %+v, want %+v", read, saved)
		}
		return err
	}))

	first := m.made(t, "one", allowed)
	m.made(t, "two", allowed)
	_, err := m.create(t, "three", allowed)
	wantErr(t, "a third live preset", err, routing.ErrPresetQuota)
	noErr(t, "disable", m.disable(t, first.ID, first.Revision))
	last := m.made(t, "three", allowed)
	noErr(t, "disable", m.disable(t, last.ID, last.Revision))
	_, err = m.create(t, "four", allowed)
	wantErr(t, "a fourth preset in all", err, routing.ErrPresetQuota)

	rules := [2]string{`{"default": {"classes": ["official", "cloud"]}}`, `{"default": {"classes": ["cloud", "official"]}}`}
	other := m // the first account has used its three
	other.account = m.otherAcct
	turns := other.made(t, "turns", rules[1])
	for i := range 62 {
		turns = other.changed(t, turns.ID, turns.Revision, turns.Name, rules[i%2])
	}
	if kept, oldest := m.versions(t, turns.ID); kept != 60 || oldest != turns.Version-59 {
		t.Errorf("at version %d the preset keeps %d versions from %d, want 60", turns.Version, kept, oldest)
	}

	noErr(t, "go back to the defaults", set(pg.PlatformScope(), routing.Limits{}))
	m.made(t, "back", allowed) // 20 live and 200 in all again: the account is over neither
	noErr(t, "count the rows", m.operate(t, func(ctx context.Context, tx pg.Tx) error {
		var n int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM routing.preset_limits`).Scan(&n)
		if n != 0 {
			t.Errorf("%d rows of limits left after saving none", n)
		}
		return err
	}))
	var updates int
	for _, a := range m.actions(t) {
		if a == "routing.limits.update" {
			updates++
		}
	}
	if updates != 2 {
		t.Errorf("%d limit changes audited, want 2", updates)
	}
}

// versions is how many rule versions the preset keeps, and the oldest.
func (m manager) versions(t *testing.T, id routing.PresetID) (kept, oldest int64) {
	t.Helper()
	noErr(t, "count the versions", m.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*), min(version) FROM routing.preset_versions WHERE preset_id = $1`,
			uuid.UUID(id)).Scan(&kept, &oldest)
	}))
	return kept, oldest
}
