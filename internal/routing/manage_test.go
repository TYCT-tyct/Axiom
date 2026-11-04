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

// noErr stops the test on an error.
func noErr(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// made, changed and defaulted are create, update and setDefault that must succeed.
func (m manager) made(t *testing.T, slug, rules string) routing.StoredPreset {
	t.Helper()
	p, err := m.create(t, slug, rules)
	noErr(t, "create "+slug, err)
	return p
}

func (m manager) changed(t *testing.T, id routing.PresetID, revision int64, name, rules string) routing.StoredPreset {
	t.Helper()
	p, err := m.update(t, id, revision, name, rules)
	noErr(t, "update", err)
	return p
}

func (m manager) defaulted(t *testing.T, id *routing.PresetID, version int64) routing.Default {
	t.Helper()
	d, err := m.setDefault(t, id, version)
	noErr(t, "set the default", err)
	return d
}

// wantErr checks that err is target.
func wantErr(t *testing.T, what string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s: %v, want %v", what, err, target)
	}
}

// wantAt checks a preset's revision and version.
func wantAt(t *testing.T, what string, p routing.StoredPreset, revision, version int64) {
	t.Helper()
	if p.Revision != revision || p.Version != version {
		t.Errorf("%s: revision %d version %d, want %d and %d", what, p.Revision, p.Version, revision, version)
	}
}

// TestPresetVersions: an account makes a preset, renames it without a new version, and changes its rules into a new
// one that the next snapshot routes by; a write that changes nothing changes nothing, and one made against an older
// revision or with a slug the account has is refused.
func TestPresetVersions(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	rules := `{"default": {"classes": ["official"]}}`
	p := m.made(t, "strict", rules)
	wantAt(t, "created", p, 1, 1)
	if p.Scope != routing.ScopeAccount || string(p.Rules) != rules {
		t.Errorf("created %+v", p)
	}
	_, err := m.create(t, "strict", allowed)
	wantErr(t, "the slug again", err, routing.ErrPresetSlugTaken)
	wantAt(t, "renamed", m.changed(t, p.ID, 1, "Renamed", rules), 2, 1)
	wantAt(t, "unchanged", m.changed(t, p.ID, 2, "Renamed", rules), 2, 1)
	wantAt(t, "new rules", m.changed(t, p.ID, 2, "Renamed", `{"default": {"sort": "price"}}`), 3, 2)
	_, err = m.update(t, p.ID, 2, "Stale", allowed)
	wantErr(t, "a stale revision", err, routing.ErrPresetStale)
	loaded, _ := byID(m.load(t, &routing.Presets{}), uuid.UUID(p.ID))
	if loaded.Version != 2 || loaded.Strategy != routing.Cheapest {
		t.Errorf("the snapshot holds %+v, want version 2", loaded)
	}
	want := []string{"routing.preset.create", "routing.preset.update", "routing.preset.update"}
	if got := m.actions(t); !slices.Equal(got, want) {
		t.Errorf("audited %v, want %v", got, want)
	}
}

// TestPresetDefaultAndDisable: an account points its default at its preset, and disables the preset only once no
// default does; a disabled preset is no longer listed, changed or chosen, but is still read. Each change is audited.
func TestPresetDefaultAndDisable(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	p := m.made(t, "strict", allowed)
	d := m.defaulted(t, &p.ID, 0)
	if d.Version < 1 || d.Preset == nil || *d.Preset != p.ID {
		t.Fatalf("the default %+v", d)
	}
	wantErr(t, "disable the default", m.disable(t, p.ID, 1), routing.ErrPresetInUse)
	if d = m.defaulted(t, nil, d.Version); d.Preset != nil || d.Version != 0 {
		t.Errorf("cleared %+v", d)
	}
	noErr(t, "disable", m.disable(t, p.ID, 1))
	_, err := m.update(t, p.ID, 2, "After", allowed)
	wantErr(t, "change a disabled preset", err, routing.ErrPresetNotFound)
	_, err = m.setDefault(t, &p.ID, 0)
	wantErr(t, "a disabled preset as the default", err, routing.ErrPresetNotSelectable)
	var listed []routing.StoredPreset
	var got routing.StoredPreset
	noErr(t, "read", m.as(t, func(ctx context.Context, tx pg.Tx) (err error) {
		if listed, err = routing.ListPresets(ctx, tx, m.admission()); err != nil {
			return err
		}
		got, err = routing.GetPreset(ctx, tx, p.ID)
		return err
	}))
	if len(listed) != 0 || !got.Disabled || got.Revision != 2 {
		t.Errorf("after disabling: listed %d, read %+v", len(listed), got)
	}
	want := []string{"routing.preset.create", "routing.default.update", "routing.default.update", "routing.preset.disable"}
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

// TestDefaultVersionsAreNeverReused: a default cleared and set again gets a version it never had, so a page holding
// the version it read before the clear cannot overwrite the new default.
func TestDefaultVersionsAreNeverReused(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	first, second := m.made(t, "first", allowed), m.made(t, "second", allowed)
	read := m.defaulted(t, &first.ID, 0)
	cleared := m.defaulted(t, nil, read.Version)
	again := m.defaulted(t, &second.ID, cleared.Version)
	if again.Version == read.Version {
		t.Fatalf("the default set again is at version %d, the one read before the clear", again.Version)
	}
	_, err := m.setDefault(t, nil, read.Version)
	wantErr(t, "a clear made against the version before", err, routing.ErrPresetStale)
	var d routing.Default
	noErr(t, "read", m.as(t, func(ctx context.Context, tx pg.Tx) (err error) {
		d, err = routing.ReadDefault(ctx, tx, m.layer())
		return err
	}))
	if d.Preset == nil || *d.Preset != second.ID {
		t.Errorf("the default is %v, want %v", d.Preset, second.ID)
	}
}

// TestPresetKeepsItsLastVersions: rules that change without growing, such as two orders of the same classes, keep
// only the preset's last 50 versions, the current one among them, and the snapshot still routes by the current one.
func TestPresetKeepsItsLastVersions(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	rules := [2]string{`{"default": {"classes": ["official", "cloud"]}}`, `{"default": {"classes": ["cloud", "official"]}}`}
	p := m.made(t, "turns", rules[1])
	for i := range 55 {
		p = m.changed(t, p.ID, p.Revision, p.Name, rules[i%2])
	}
	var kept, oldest int64
	noErr(t, "count the versions", m.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*), min(version) FROM routing.preset_versions WHERE preset_id = $1`,
			uuid.UUID(p.ID)).Scan(&kept, &oldest)
	}))
	if p.Version != 56 || kept != 50 || oldest != 7 {
		t.Errorf("at version %d the preset keeps %d versions from %d, want 50 from 7", p.Version, kept, oldest)
	}
	if loaded, ok := byID(m.load(t, &routing.Presets{}), uuid.UUID(p.ID)); !ok || loaded.Version != 56 {
		t.Errorf("the snapshot holds %+v %v, want version 56", loaded, ok)
	}
}

// TestPresetQuota: an account holds at most 20 live presets and makes at most 200 in all, the disabled ones counted,
// so making and disabling in turn stops at the 201st while another account still makes its own.
func TestPresetQuota(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	var last routing.StoredPreset
	for i := range 20 {
		last = m.made(t, "live-"+string(rune('a'+i)), allowed)
	}
	_, err := m.create(t, "twenty-first", allowed)
	wantErr(t, "the 21st live preset", err, routing.ErrPresetQuota)
	for range 180 {
		noErr(t, "disable", m.disable(t, last.ID, last.Revision))
		last = m.made(t, "turn-"+uuid.NewV7().String()[24:], allowed)
	}
	noErr(t, "disable", m.disable(t, last.ID, last.Revision))
	_, err = m.create(t, "two-hundred-first", allowed)
	wantErr(t, "the 201st preset", err, routing.ErrPresetQuota)
	other := m
	other.account = m.otherAcct
	other.made(t, "own", allowed)
}

// TestPresetGlobalBudget: a full global budget refuses a new preset and larger rules, and lets smaller rules through,
// so rules can always be made smaller.
func TestPresetGlobalBudget(t *testing.T) {
	t.Parallel()
	m := newManager(t)
	sized := func(n int) string { return `{"default": {"classes": ["` + strings.Repeat("a", n) + `"]}}` }
	p := m.made(t, "grows", sized(30))
	noErr(t, "fill the budget", m.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE routing.preset_budget SET presets = 100000, bytes = 256 * 1024 * 1024`)
		return err
	}))
	_, err := m.create(t, "full", allowed)
	wantErr(t, "a preset over the global budget", err, routing.ErrPresetQuota)
	_, err = m.update(t, p.ID, 1, p.Name, sized(31))
	wantErr(t, "larger rules over the global budget", err, routing.ErrPresetQuota)
	m.changed(t, p.ID, 1, p.Name, allowed)
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
