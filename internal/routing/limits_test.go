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
		{"every bound", routing.Limits{
			Platform: routing.LayerLimits{Active: 500, Total: 5000}, Workspace: routing.LayerLimits{Active: 1, Total: 1},
			KeptVersions: 500, Presets: 1_000_000, Bytes: 2 << 30,
		}, ""},
		{"a live limit over its bound", routing.Limits{Site: routing.LayerLimits{Active: 501, Total: 5000}}, "site/active"},
		{"a total over its bound", routing.Limits{Account: routing.LayerLimits{Total: 5001}}, "account/total"},
		{"a negative total", routing.Limits{Workspace: routing.LayerLimits{Total: -1}}, "workspace/total"},
		{"fewer versions than the table keeps", routing.Limits{KeptVersions: 49}, "kept_versions"},
		{"more versions than the bound", routing.Limits{KeptVersions: 501}, "kept_versions"},
		{"a live limit above the default total", routing.Limits{Account: routing.LayerLimits{Active: 201}}, "account/active"},
		{"a total below the default live limit", routing.Limits{Platform: routing.LayerLimits{Total: 49}}, "platform/active"},
		{"a global budget of no preset", routing.Limits{Presets: -1}, "global/presets"},
		{"a global budget over its ceiling", routing.Limits{Presets: 1_000_001}, "global/presets"},
		{"global bytes below a mebibyte", routing.Limits{Bytes: 1 << 19}, "global/bytes"},
		{"global bytes over two gibibytes", routing.Limits{Bytes: 2<<30 + 1}, "global/bytes"},
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

// setLimits saves l at rev in the scope and returns the new revision.
func (m manager) setLimits(t *testing.T, scope pg.Scope, l routing.Limits, rev int64) (int64, error) {
	t.Helper()
	var next int64
	err := m.db.Tx(t.Context(), scope, func(tx pg.Tx) (err error) {
		next, err = routing.SetLimits(t.Context(), tx, m.actor, l, rev, "tune")
		return err
	})
	return next, err
}

// limits reads the saved limits and their revision in the account's scope.
func (m manager) limits(t *testing.T) (l routing.Limits, rev int64) {
	t.Helper()
	noErr(t, "read the limits", m.as(t, func(ctx context.Context, tx pg.Tx) (err error) {
		l, rev, err = routing.ReadLimits(ctx, tx)
		return err
	}))
	return l, rev
}

// TestPresetLimitsApply: the limits the platform saves hold the next writes: an account's live and total presets, and
// the global number of presets. A tenant cannot save them, but reads them; saving none goes back to the defaults; each
// change is audited.
func TestPresetLimitsApply(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	saved := routing.Limits{Account: routing.LayerLimits{Active: 2, Total: 3}}
	_, err := m.setLimits(t, pg.PlatformScope(), saved, 0)
	noErr(t, "save the limits", err)
	_, err = m.setLimits(t, pg.AccountScopeIn(m.site, m.account), routing.Limits{KeptVersions: 100}, 1)
	wantErr(t, "an account saving limits", err, routing.ErrPresetReadOnly)
	if read, rev := m.limits(t); read != saved || rev != 1 {
		t.Errorf("an account reads %+v at %d, want %+v at 1", read, rev, saved)
	}

	first := m.made(t, "one", allowed)
	m.made(t, "two", allowed)
	_, err = m.create(t, "three", allowed)
	wantErr(t, "a third live preset", err, routing.ErrPresetQuota)
	noErr(t, "disable", m.disable(t, first.ID, first.Revision))
	last := m.made(t, "three", allowed)
	noErr(t, "disable", m.disable(t, last.ID, last.Revision))
	_, err = m.create(t, "four", allowed)
	wantErr(t, "a fourth preset in all", err, routing.ErrPresetQuota)

	// The system preset and the account's three fill a global budget of four.
	_, err = m.setLimits(t, pg.PlatformScope(), routing.Limits{Presets: 4}, 1)
	noErr(t, "lower the global budget", err)
	other := m
	other.account = m.otherAcct
	_, err = other.create(t, "own", allowed)
	wantErr(t, "a preset over the global budget", err, routing.ErrPresetQuota)

	_, err = m.setLimits(t, pg.PlatformScope(), routing.Limits{}, 2)
	noErr(t, "go back to the defaults", err)
	m.made(t, "back", allowed) // 20 live and 200 in all again: the account is over neither
	other.made(t, "own", allowed)
	var updates int
	for _, a := range m.actions(t) {
		if a == "routing.limits.update" {
			updates++
		}
	}
	if updates != 3 {
		t.Errorf("%d limit changes audited, want 3", updates)
	}
}

// TestLimitsRevision: two admins read the same limits; the second change, made against what both read, is refused
// instead of putting back what the first changed. Clearing every value keeps counting, so a revision read before the
// clear never matches again.
func TestLimitsRevision(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	first := routing.Limits{Account: routing.LayerLimits{Active: 2}}
	rev, err := m.setLimits(t, pg.PlatformScope(), first, 0)
	noErr(t, "the first change", err)
	_, err = m.setLimits(t, pg.PlatformScope(), routing.Limits{KeptVersions: 60}, 0)
	wantErr(t, "a change made against the older read", err, routing.ErrPresetStale)
	if read, at := m.limits(t); read != first || at != rev {
		t.Errorf("the record is %+v at %d, want the first change at %d", read, at, rev)
	}
	cleared, err := m.setLimits(t, pg.PlatformScope(), routing.Limits{}, rev)
	noErr(t, "clear every value", err)
	for _, stale := range []int64{0, rev} {
		_, err = m.setLimits(t, pg.PlatformScope(), first, stale)
		wantErr(t, "a change against a revision before the clear", err, routing.ErrPresetStale)
	}
	if read, at := m.limits(t); read != (routing.Limits{}) || at != cleared || cleared != rev+1 {
		t.Errorf("after the clear the record is %+v at %d, want none at %d", read, at, rev+1)
	}
}

// TestKeptVersionsPruneOnNewRules: the kept versions the platform saves decide how many a preset keeps when its rules
// change; lowering them deletes nothing until the next rule change, and a rename alone deletes nothing.
func TestKeptVersionsPruneOnNewRules(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	rev, err := m.setLimits(t, pg.PlatformScope(), routing.Limits{KeptVersions: 60}, 0)
	noErr(t, "keep 60 versions", err)
	rules := [2]string{`{"default": {"classes": ["official", "cloud"]}}`, `{"default": {"classes": ["cloud", "official"]}}`}
	p := m.made(t, "turns", rules[1])
	for i := range 62 {
		p = m.changed(t, p.ID, p.Revision, p.Name, rules[i%2])
	}
	wantKept := func(what string, want int64) {
		t.Helper()
		if kept, oldest := m.versions(t, p.ID); kept != want || oldest != p.Version-want+1 {
			t.Errorf("%s: at version %d the preset keeps %d versions from %d, want %d", what, p.Version, kept, oldest, want)
		}
	}
	wantKept("with 60 kept", 60)
	_, err = m.setLimits(t, pg.PlatformScope(), routing.Limits{KeptVersions: 50}, rev)
	noErr(t, "keep 50 versions", err)
	p = m.changed(t, p.ID, p.Revision, "renamed", rules[1])
	wantKept("after a rename", 60)
	p = m.changed(t, p.ID, p.Revision, p.Name, rules[0])
	wantKept("after new rules", 50)
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
