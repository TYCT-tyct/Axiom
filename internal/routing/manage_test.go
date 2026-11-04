package routing_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

// manager writes presets as one account's console does, and reads what was recorded.
type manager struct {
	presetDB
	actor audit.Actor
}

func newManager(t *testing.T) manager {
	t.Helper()
	m := manager{presetDB: newPresetDB(t), actor: audit.User(uuid.NewV7())}
	err := m.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return errors.Join(audit.RegisterSite(t.Context(), tx, m.site), audit.RegisterAccount(t.Context(), tx, m.site, m.account),
			audit.RegisterAccount(t.Context(), tx, m.site, m.otherAcct))
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (m manager) layer() routing.Layer {
	return routing.Layer{Scope: routing.ScopeAccount, Site: routing.SiteID(m.site), Account: routing.AccountID(m.account)}
}

func (m manager) admission() routing.Admission {
	return routing.Admission{Site: routing.SiteID(m.site), Account: routing.AccountID(m.account), Currency: usd}
}

// as runs fn in the account's scope.
func (m manager) as(t *testing.T, fn func(ctx context.Context, tx pg.Tx) error) error {
	t.Helper()
	return m.db.Tx(t.Context(), pg.AccountScopeIn(m.site, m.account), func(tx pg.Tx) error { return fn(t.Context(), tx) })
}

func (m manager) create(t *testing.T, slug, rules string) (routing.StoredPreset, error) {
	t.Helper()
	var p routing.StoredPreset
	err := m.as(t, func(ctx context.Context, tx pg.Tx) (err error) {
		p, err = routing.CreatePreset(ctx, tx, m.actor, m.layer(), slug, routing.PresetContent{Name: "N " + slug, Rules: []byte(rules)})
		return err
	})
	return p, err
}

func (m manager) update(t *testing.T, id routing.PresetID, revision int64, name, rules string) (routing.StoredPreset, error) {
	t.Helper()
	var p routing.StoredPreset
	err := m.as(t, func(ctx context.Context, tx pg.Tx) (err error) {
		p, err = routing.UpdatePreset(ctx, tx, m.actor, id, revision, routing.PresetContent{Name: name, Rules: []byte(rules)})
		return err
	})
	return p, err
}

func (m manager) disable(t *testing.T, id routing.PresetID, revision int64) error {
	t.Helper()
	return m.as(t, func(ctx context.Context, tx pg.Tx) error {
		return routing.DisablePreset(ctx, tx, m.actor, id, revision)
	})
}

func (m manager) setDefault(t *testing.T, id *routing.PresetID, version int64) (routing.Default, error) {
	t.Helper()
	var d routing.Default
	err := m.as(t, func(ctx context.Context, tx pg.Tx) (err error) {
		d, err = routing.SetDefault(ctx, tx, m.actor, m.layer(), usd, id, version)
		return err
	})
	return d, err
}

func (m manager) actions(t *testing.T) []string {
	t.Helper()
	var out []string
	err := m.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT action FROM platform.audit_log WHERE module = 'routing' ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var action string
			if err := rows.Scan(&action); err != nil {
				return err
			}
			out = append(out, action)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPresetLifecycle: an account makes a preset, renames it without a new version, changes its rules into a new one
// that the next snapshot routes by, points its default at it, and disables it only once no default does; a disabled
// preset is no longer listed, changed or chosen. Each change is in the audit log, and a write that changes nothing is
// not.
func TestPresetLifecycle(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	p, err := m.create(t, "strict", `{"default": {"classes": ["official"]}}`)
	if err != nil || p.Revision != 1 || p.Version != 1 || p.Scope != routing.ScopeAccount || string(p.Rules) != `{"default": {"classes": ["official"]}}` {
		t.Fatalf("created %+v %v", p, err)
	}
	if _, err := m.create(t, "strict", allowed); !errors.Is(err, routing.ErrPresetSlugTaken) {
		t.Errorf("the slug again: %v", err)
	}
	if p, err = m.update(t, p.ID, 1, "Renamed", string(p.Rules)); err != nil || p.Revision != 2 || p.Version != 1 {
		t.Fatalf("renamed: %+v %v", p, err)
	}
	if same, err := m.update(t, p.ID, 2, "Renamed", string(p.Rules)); err != nil || same.Revision != 2 {
		t.Errorf("an unchanged write: %+v %v", same, err)
	}
	if p, err = m.update(t, p.ID, 2, "Renamed", `{"default": {"sort": "price"}}`); err != nil || p.Revision != 3 || p.Version != 2 {
		t.Fatalf("new rules: %+v %v", p, err)
	}
	if _, err := m.update(t, p.ID, 2, "Stale", allowed); !errors.Is(err, routing.ErrPresetStale) {
		t.Errorf("a stale revision: %v", err)
	}
	if loaded, ok := byID(m.load(t, &routing.Presets{}), uuid.UUID(p.ID)); !ok || loaded.Version != 2 || loaded.Strategy != routing.Cheapest {
		t.Errorf("the snapshot holds %+v %v, want version 2", loaded, ok)
	}

	d, err := m.setDefault(t, &p.ID, 0)
	if err != nil || d.Version != 1 || *d.Preset != p.ID {
		t.Fatalf("set the default: %+v %v", d, err)
	}
	if err := m.disable(t, p.ID, 3); !errors.Is(err, routing.ErrPresetInUse) {
		t.Errorf("disable the default: %v", err)
	}
	if d, err = m.setDefault(t, nil, 1); err != nil || d.Preset != nil || d.Version != 0 {
		t.Fatalf("clear the default: %+v %v", d, err)
	}
	if err := m.disable(t, p.ID, 3); err != nil {
		t.Fatal(err)
	}
	_, err = m.update(t, p.ID, 4, "After", allowed)
	_, defErr := m.setDefault(t, &p.ID, 0)
	if !errors.Is(err, routing.ErrPresetNotFound) || !errors.Is(defErr, routing.ErrPresetNotSelectable) {
		t.Errorf("a disabled preset changed: %v, made the default: %v", err, defErr)
	}
	var listed []routing.StoredPreset
	var got routing.StoredPreset
	if err := m.as(t, func(ctx context.Context, tx pg.Tx) (err error) {
		if listed, err = routing.ListPresets(ctx, tx, m.admission()); err != nil {
			return err
		}
		got, err = routing.GetPreset(ctx, tx, p.ID)
		return err
	}); err != nil || len(listed) != 0 || !got.Disabled || got.Revision != 4 {
		t.Errorf("after disabling: listed %d, read %+v %v", len(listed), got, err)
	}
	want := []string{"routing.preset.create", "routing.preset.update", "routing.preset.update", "routing.default.update",
		"routing.default.update", "routing.preset.disable"}
	if got := m.actions(t); !slices.Equal(got, want) {
		t.Errorf("audited %v, want %v", got, want)
	}
}

// TestPresetWritesRefused: rules that break the format, widen what they inherit or set what only the platform may are
// refused; an account cannot change a preset of another layer or see another account's; a default or a key cannot be
// pointed at the system preset, a disabled preset or one of another tenant or currency.
func TestPresetWritesRefused(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	platform := m.must(t, stored{scope: "platform", rules: allowed})
	priced := m.must(t, stored{scope: "platform", rules: `{"default":{"max_price":{"prompt":"1"}}}`, currency: new("CNY")})
	other := m.must(t, stored{scope: "account", site: &m.site, account: &m.otherAcct, rules: allowed})
	gone := m.must(t, stored{scope: "account", site: &m.site, account: &m.account, rules: allowed, deleted: true})
	workspace := m.must(t, stored{scope: "workspace", site: &m.site, account: &m.account, workspace: &m.workspace, rules: allowed})
	for name, rules := range map[string]string{
		"not JSON":             `{"default":`,
		"a member it lacks":    `{"default": {"colour": "red"}}`,
		"an override widening": `{"default": {"classes": ["official"]}, "rules": [{"author": "openai", "classes": ["relay"]}]}`,
		"advanced settings":    `{"default": {}, "advanced": {"max_attempts": 2}}`,
	} {
		if _, err := m.create(t, "bad", rules); !errors.Is(err, routing.ErrRules) {
			t.Errorf("%s: %v, want ErrRules", name, err)
		}
	}
	for name, c := range map[string]struct {
		id   uuid.UUID
		want error
	}{
		"the platform's":    {platform, routing.ErrPresetReadOnly},
		"the system preset": {uuid.UUID(routing.SystemPreset), routing.ErrPresetReadOnly},
		"another account's": {other, routing.ErrPresetNotFound},
		"an unknown preset": {uuid.NewV7(), routing.ErrPresetNotFound},
		"a disabled preset": {gone, routing.ErrPresetNotFound},
	} {
		if _, err := m.update(t, routing.PresetID(c.id), 1, "X", allowed); !errors.Is(err, c.want) {
			t.Errorf("change %s: %v, want %v", name, err, c.want)
		}
	}
	if _, err := m.setDefault(t, nil, 3); !errors.Is(err, routing.ErrPresetStale) {
		t.Errorf("a default at a version it is not at: %v", err)
	}
	for name, c := range map[string]struct {
		id     uuid.UUID
		forKey bool // a key of the account's workspace may choose it; the account's own default may not
	}{
		"the system preset":            {uuid.UUID(routing.SystemPreset), false},
		"a disabled preset":            {gone, false},
		"another account's":            {other, false},
		"a preset in another currency": {priced, false},
		"an unknown preset":            {uuid.NewV7(), false},
		"a workspace's":                {workspace, true},
	} {
		id := routing.PresetID(c.id)
		if _, err := m.setDefault(t, &id, 0); !errors.Is(err, routing.ErrPresetNotSelectable) {
			t.Errorf("the account's default at %s: %v", name, err)
		}
		key := m.admission()
		key.Workspace = routing.WorkspaceID(m.workspace)
		err := m.as(t, func(ctx context.Context, tx pg.Tx) error { return routing.SelectablePreset(ctx, tx, key, id) })
		if c.forKey != (err == nil) || err != nil && !errors.Is(err, routing.ErrPresetNotSelectable) {
			t.Errorf("a key bound to %s: %v", name, err)
		}
	}
}

// TestPresetQuota: an account holds at most 20 live presets and makes at most 200 in all, the disabled ones counted,
// so making and disabling in turn stops at the 201st while another account still makes its own; and the global
// budget refuses a new preset or larger rules when full, while rules that shrink still pass.
func TestPresetQuota(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	var last routing.StoredPreset
	for i := range 20 {
		var err error
		if last, err = m.create(t, "live-"+string(rune('a'+i)), allowed); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := m.create(t, "twenty-first", allowed); !errors.Is(err, routing.ErrPresetQuota) {
		t.Fatalf("the 21st live preset: %v", err)
	}
	for i := 20; i < 200; i++ {
		if err := m.disable(t, last.ID, last.Revision); err != nil {
			t.Fatal(i, err)
		}
		var err error
		if last, err = m.create(t, "turn-"+uuid.NewV7().String()[24:], allowed); err != nil {
			t.Fatal(i, err)
		}
	}
	if err := m.disable(t, last.ID, last.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := m.create(t, "two-hundred-first", allowed); !errors.Is(err, routing.ErrPresetQuota) {
		t.Errorf("the 201st preset: %v", err)
	}
	other := m
	other.account = m.otherAcct
	if _, err := other.create(t, "own", allowed); err != nil {
		t.Errorf("another account: %v", err)
	}

	sized := func(n int) string { return `{"default": {"classes": ["` + strings.Repeat("a", n) + `"]}}` }
	big := sized(30)
	p, err := other.create(t, "grows", big)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE routing.preset_budget SET presets = 100000, bytes = 256 * 1024 * 1024`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.create(t, "full", allowed); !errors.Is(err, routing.ErrPresetQuota) {
		t.Errorf("a preset over the global budget: %v", err)
	}
	if _, err := other.update(t, p.ID, 1, p.Name, sized(31)); !errors.Is(err, routing.ErrPresetQuota) {
		t.Errorf("larger rules over the global budget: %v", err)
	}
	if _, err := other.update(t, p.ID, 1, p.Name, allowed); err != nil {
		t.Errorf("smaller rules over the global budget: %v", err)
	}
}

// TestDefaultWaitsForTheDisable: a disable of a preset in a transaction not yet committed makes a default pointed at
// it wait and then refuse, instead of both committing and leaving the default on a disabled preset. (The other order
// is held by the default's foreign key, whose lock on the preset's row the disable waits for.)
func TestDefaultWaitsForTheDisable(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	p, err := m.create(t, "contested", allowed)
	if err != nil {
		t.Fatal(err)
	}
	disabled, commit := make(chan error, 1), make(chan struct{})
	go func() {
		disabled <- m.as(t, func(ctx context.Context, tx pg.Tx) error {
			err := routing.DisablePreset(ctx, tx, m.actor, p.ID, p.Revision)
			disabled <- err
			<-commit
			return err
		})
	}()
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	set := make(chan error, 1)
	go func() {
		_, err := m.setDefault(t, &p.ID, 0)
		set <- err
	}()
	m.waitForALockWaiter(t)
	close(commit)
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	if err := <-set; !errors.Is(err, routing.ErrPresetNotSelectable) {
		t.Errorf("the default that waited: %v, want ErrPresetNotSelectable", err)
	}
}

// waitForALockWaiter returns once a transaction of this database waits for a lock another holds.
func (m manager) waitForALockWaiter(t *testing.T) {
	t.Helper()
	for {
		var waiting bool
		err := m.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
				WHERE NOT l.granted AND a.datname = current_database())`).Scan(&waiting)
		})
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
	}
}
