package routing_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

func (m manager) suspend(t *testing.T, scope pg.Scope, suspend bool) (since time.Time, err error) {
	t.Helper()
	err = m.db.Tx(t.Context(), scope, func(tx pg.Tx) (err error) {
		since, err = routing.SetSuspension(t.Context(), tx, m.actor, suspend, "incident")
		return err
	})
	return since, err
}

// TestPresetSuspension: suspended by the platform, every preset and default write and every key's choice of a preset
// is refused, while a new key is still bound to its layers' default; a tenant cannot suspend them; asking again
// changes nothing; resumed, writes pass, and each change is audited.
func TestPresetSuspension(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	p := m.made(t, "mine", `{"default":{"sort":"price"}}`)
	d := m.defaulted(t, &p.ID, 0)

	if _, err := m.suspend(t, pg.AccountScopeIn(m.site, m.account), true); !errors.Is(err, routing.ErrPresetReadOnly) {
		t.Fatalf("an account suspended presets: %v", err)
	}
	since, err := m.suspend(t, pg.PlatformScope(), true)
	noErr(t, "suspend", err)
	if since.IsZero() {
		t.Fatal("no time of suspension")
	}
	if again, err := m.suspend(t, pg.PlatformScope(), true); err != nil || !again.Equal(since) {
		t.Fatalf("suspending again = %v, %v; want %v", again, err, since)
	}
	m.checkSuspendedWrites(t, p, d)

	if resumed, err := m.suspend(t, pg.PlatformScope(), false); err != nil || !resumed.IsZero() {
		t.Fatalf("resume = %v, %v", resumed, err)
	}
	m.changed(t, p.ID, p.Revision, "renamed", `{"default":{"sort":"price"}}`)
	if got, want := m.actions(t), []string{
		"routing.preset.create", "routing.default.update", "routing.presets.suspend", "routing.presets.resume", "routing.preset.update",
	}; !slices.Equal(got, want) {
		t.Errorf("audit actions %v, want %v", got, want)
	}
}

// checkSuspendedWrites checks that while presets are suspended every preset and default write and every key's choice
// of p is refused, and that a new key is still bound to the account's default, p.
func (m manager) checkSuspendedWrites(t *testing.T, p routing.StoredPreset, d routing.Default) {
	t.Helper()
	refused := map[string]error{}
	_, refused["create"] = m.create(t, "other", allowed)
	_, refused["update"] = m.update(t, p.ID, p.Revision, "renamed", allowed)
	refused["disable"] = m.disable(t, p.ID, p.Revision)
	_, refused["set the default"] = m.setDefault(t, nil, d.Version)
	refused["choose for a key"] = m.as(t, func(ctx context.Context, tx pg.Tx) error {
		return routing.SelectablePreset(ctx, tx, m.admission(), p.ID)
	})
	for what, err := range refused {
		if !errors.Is(err, routing.ErrPresetsSuspended) {
			t.Errorf("%s while suspended: %v", what, err)
		}
	}
	noErr(t, "bind a new key", m.as(t, func(ctx context.Context, tx pg.Tx) error {
		got, err := routing.DefaultPreset(ctx, tx, routing.SiteID(m.site), routing.AccountID(m.account), routing.WorkspaceID(m.workspace), usd)
		if err == nil && got != p.ID {
			t.Errorf("a new key is bound to %v, want the default %v", got, p.ID)
		}
		return err
	}))
}

// TestSuspensionNeedsNoNewSnapshot: the router refuses every key bound to a preset other than the system's from the
// load that reads the suspension, though no snapshot built after it is ever published (a later source of that build
// failed), calls and model lists alike; the system preset still routes; resumed, the same snapshot routes the preset
// again.
func TestSuspensionNeedsNoNewSnapshot(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	p := m.made(t, "mine", allowed)
	loaded := m.load(t, &routing.Presets{})
	system := loaded[slices.IndexFunc(loaded, func(p routing.Preset) bool { return p.ID == routing.SystemPreset })]
	h := newHarness(t, routing.Config{}, system, endpoint(t, 1))
	snap, err := h.r.NewSnapshot(h.now.Add(time.Nanosecond), []routing.Endpoint{endpoint(t, 1)}, loaded)
	noErr(t, "build the snapshot", err)
	h.r.Replace(snap)
	h.now = h.now.Add(time.Nanosecond)
	bound := input()
	bound.Admission = routing.Admission{Site: routing.SiteID(m.site), Account: routing.AccountID(m.account), Currency: usd, Preset: p.ID}
	unbound := bound
	unbound.Admission.Preset = routing.SystemPreset
	h.plan(bound)

	source := &routing.Presets{Router: h.r}
	_, err = m.suspend(t, pg.PlatformScope(), true)
	noErr(t, "suspend", err)
	m.load(t, source) // and the build it was read for is dropped
	if _, _, err := h.r.Plan(bound); !errors.Is(err, routing.ErrPresetUnavailable) {
		t.Errorf("a bound key's call while suspended: %v", err)
	}
	if _, err := h.r.KeyView(bound.Admission); !errors.Is(err, routing.ErrPresetUnavailable) {
		t.Errorf("a bound key's model list while suspended: %v", err)
	}
	h.plan(unbound)

	_, err = m.suspend(t, pg.PlatformScope(), false)
	noErr(t, "resume", err)
	m.load(t, source)
	h.plan(bound)
}
