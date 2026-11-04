package routing_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/routing"
)

// presetDB is a database with routing's tables, and the IDs of two sites, two accounts of the first and a workspace of
// each account.
type presetDB struct {
	db                   *pg.DB
	site, otherSite      uuid.UUID
	account, otherAcct   uuid.UUID
	workspace, otherWork uuid.UUID
}

func newPresetDB(t *testing.T) presetDB {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t, routing.Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return presetDB{
		db: db, site: uuid.NewV7(), otherSite: uuid.NewV7(), account: uuid.NewV7(), otherAcct: uuid.NewV7(),
		workspace: uuid.NewV7(), otherWork: uuid.NewV7(),
	}
}

// stored is a preset row and its one version.
type stored struct {
	scope                    string
	site, account, workspace *uuid.UUID
	rules                    string
	currency                 *string
	deleted                  bool
}

// insert writes the preset in the scope and returns its ID.
func (d presetDB) insert(t *testing.T, scope pg.Scope, p stored) (uuid.UUID, error) {
	t.Helper()
	id := uuid.NewV7()
	err := d.db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO routing.presets (id, scope, site_id, account_id, workspace_id, slug, name,
			current_version, deleted_at) VALUES ($1, $2, $3, $4, $5, $6, 'P', 1, CASE WHEN $7 THEN now() END)`,
			id, p.scope, p.site, p.account, p.workspace, "p-"+id.String()[30:], p.deleted); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO routing.preset_versions (preset_id, version, rules, currency) VALUES ($1, 1, $2, $3)`,
			id, p.rules, p.currency)
		return err
	})
	return id, err
}

func (d presetDB) must(t *testing.T, p stored) uuid.UUID {
	t.Helper()
	id, err := d.insert(t, pg.PlatformScope(), p)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// setDefault points a layer at the preset, in the platform scope.
func (d presetDB) setDefault(t *testing.T, scope string, site, account, workspace *uuid.UUID, preset uuid.UUID) {
	t.Helper()
	err := d.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO routing.preset_defaults (scope, site_id, account_id, workspace_id, preset_id)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (site_id, account_id, workspace_id) DO UPDATE SET preset_id = excluded.preset_id`,
			scope, site, account, workspace, preset)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (d presetDB) load(t *testing.T, source *routing.Presets) []routing.Preset {
	t.Helper()
	var out []routing.Preset
	err := d.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) (err error) {
		out, err = source.Load(t.Context(), tx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func byID(presets []routing.Preset, id uuid.UUID) (routing.Preset, bool) {
	i := slices.IndexFunc(presets, func(p routing.Preset) bool { return uuid.UUID(p.ID) == id })
	if i < 0 {
		return routing.Preset{}, false
	}
	return presets[i], true
}

// TestPresetsLoad: the snapshot reads the system preset the migration wrote, with the rules the code-built allow-all
// has; a preset whose rules this node cannot read is left out alone; a new version is read at the next load, and a
// disabled preset still loads, because the keys bound to it still route by it.
func TestPresetsLoad(t *testing.T) {
	t.Parallel()
	d := newPresetDB(t)
	source := &routing.Presets{}
	checkSystem(t, d.load(t, source))
	good := d.must(t, stored{scope: "site", site: &d.site, rules: `{"default":{"sort":"price"}}`})
	bad := d.must(t, stored{scope: "account", site: &d.site, account: &d.account, rules: `{"default":{"from_a_newer_node":true}}`})
	gone := d.must(t, stored{scope: "account", site: &d.site, account: &d.account, rules: allowed, deleted: true})
	presets := d.load(t, source)
	if p, ok := byID(presets, good); !ok || p.Strategy != routing.Cheapest || p.Site != routing.SiteID(d.site) {
		t.Errorf("good preset %+v, %v", p, ok)
	}
	if _, ok := byID(presets, bad); ok {
		t.Error("a preset with rules this node cannot read was loaded")
	}
	if _, ok := byID(presets, gone); !ok {
		t.Error("a disabled preset was not loaded")
	}
	d.newVersion(t, good, `{"default":{"sort":"latency"}}`)
	if p, _ := byID(d.load(t, source), good); p.Strategy != routing.LowestLatency || p.Version != 2 {
		t.Errorf("after a new version: %+v", p)
	}
}

// checkSystem: the system preset is loaded with the code-built allow-all's rules.
func checkSystem(t *testing.T, presets []routing.Preset) {
	t.Helper()
	system, _ := byID(presets, uuid.UUID(routing.SystemPreset))
	want := routing.Preset{
		Version: 1, Classes: routing.AllClasses, Policies: routing.AllPolicies, MaxAttempts: 4,
	}
	if !reflect.DeepEqual(system, want) {
		t.Errorf("system preset %+v, want %+v", system, want)
	}
}

// newVersion gives the preset a second version, as an edit of its rules does.
func (d presetDB) newVersion(t *testing.T, id uuid.UUID, rules string) {
	t.Helper()
	err := d.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO routing.preset_versions (preset_id, version, rules) VALUES ($1, 2, $2)`, id, rules); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE routing.presets SET current_version = 2 WHERE id = $1`, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestDefaultPresetLayers: a new key is bound to the default of the deepest layer that sets one, from its workspace up
// to the platform; a default the key cannot use is refused rather than passed over for a wider layer's.
func TestDefaultPresetLayers(t *testing.T) {
	t.Parallel()
	d := newPresetDB(t)
	resolve := func(account, workspace uuid.UUID) (routing.PresetID, error) {
		var id routing.PresetID
		err := d.db.Tx(t.Context(), pg.AccountScopeIn(d.site, account), func(tx pg.Tx) (err error) {
			id, err = routing.DefaultPreset(t.Context(), tx, routing.SiteID(d.site), routing.AccountID(account), routing.WorkspaceID(workspace), usd)
			return err
		})
		return id, err
	}
	expect := func(step string, account, workspace, want uuid.UUID) {
		t.Helper()
		if got, err := resolve(account, workspace); err != nil || uuid.UUID(got) != want {
			t.Errorf("%s: %v %v, want %v", step, uuid.UUID(got), err, want)
		}
	}
	expect("only the platform's", d.account, d.workspace, uuid.UUID(routing.SystemPreset))
	sitePreset := d.must(t, stored{scope: "site", site: &d.site, rules: allowed})
	d.setDefault(t, "site", &d.site, nil, nil, sitePreset)
	expect("the site's", d.account, d.workspace, sitePreset)
	accountPreset := d.must(t, stored{scope: "account", site: &d.site, account: &d.account, rules: allowed})
	d.setDefault(t, "account", &d.site, &d.account, nil, accountPreset)
	expect("the account's", d.account, d.workspace, accountPreset)
	expect("another account's is not this one's", d.otherAcct, d.otherWork, sitePreset)
	workPreset := d.must(t, stored{scope: "workspace", site: &d.site, account: &d.account, workspace: &d.otherWork, rules: allowed})
	d.setDefault(t, "workspace", &d.site, &d.account, &d.otherWork, workPreset)
	expect("another workspace's is not this one's", d.account, d.workspace, accountPreset)
	expect("the workspace's", d.account, d.otherWork, workPreset)

	for name, p := range map[string]stored{
		"a disabled preset":            {scope: "account", site: &d.site, account: &d.account, rules: allowed, deleted: true},
		"another account's preset":     {scope: "account", site: &d.site, account: &d.otherAcct, rules: allowed},
		"a preset in another currency": {scope: "platform", rules: `{"default":{"max_price":{"prompt":"1"}}}`, currency: new("CNY")},
		"a preset of another site":     {scope: "site", site: &d.otherSite, rules: allowed},
	} {
		d.setDefault(t, "account", &d.site, &d.account, nil, d.must(t, p))
		if _, err := resolve(d.account, d.workspace); !errors.Is(err, routing.ErrDefaultInvalid) {
			t.Errorf("an account default naming %s: %v, want ErrDefaultInvalid", name, err)
		}
	}
}

// TestPresetTablesKeepTenantsApart: an account reads the platform's presets, its site's and its own, and writes only its
// own; a site writes only its own; nobody but a migration writes or changes the system preset.
func TestPresetTablesKeepTenantsApart(t *testing.T) {
	t.Parallel()
	d := newPresetDB(t)
	platformID := d.must(t, stored{scope: "platform", rules: allowed})
	siteID := d.must(t, stored{scope: "site", site: &d.site, rules: allowed})
	otherSiteID := d.must(t, stored{scope: "site", site: &d.otherSite, rules: allowed})
	ownID := d.must(t, stored{scope: "workspace", site: &d.site, account: &d.account, workspace: &d.workspace, rules: allowed})
	otherID := d.must(t, stored{scope: "account", site: &d.site, account: &d.otherAcct, rules: allowed})
	account := pg.AccountScopeIn(d.site, d.account)
	seen := d.visible(t, account)
	for id, want := range map[uuid.UUID]bool{
		uuid.UUID(routing.SystemPreset): true, platformID: true, siteID: true, ownID: true, otherSiteID: false, otherID: false,
	} {
		if slices.Contains(seen, id) != want {
			t.Errorf("preset %v seen %v, want %v", id, !want, want)
		}
	}
	for name, w := range map[string]struct {
		scope pg.Scope
		p     stored
		ok    bool
	}{
		"its own account's":     {account, stored{scope: "account", site: &d.site, account: &d.account, rules: allowed}, true},
		"another account's":     {account, stored{scope: "account", site: &d.site, account: &d.otherAcct, rules: allowed}, false},
		"its site's":            {account, stored{scope: "site", site: &d.site, rules: allowed}, false},
		"the platform's":        {account, stored{scope: "platform", rules: allowed}, false},
		"a site its own":        {pg.SiteScope(d.site), stored{scope: "site", site: &d.site, rules: allowed}, true},
		"a site another site's": {pg.SiteScope(d.site), stored{scope: "site", site: &d.otherSite, rules: allowed}, false},
	} {
		if _, err := d.insert(t, w.scope, w.p); (err == nil) != w.ok {
			t.Errorf("writing %s: %v", name, err)
		}
	}
	err := d.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		tag, err := tx.Exec(t.Context(), `UPDATE routing.presets SET name = 'Mine' WHERE id = $1`, uuid.UUID(routing.SystemPreset))
		if err == nil && tag.RowsAffected() != 0 {
			return errors.New("the system preset was renamed")
		}
		return err
	})
	if err != nil {
		t.Error(err)
	}
}

// visible is the presets, with their versions, that the scope reads.
func (d presetDB) visible(t *testing.T, scope pg.Scope) []uuid.UUID {
	t.Helper()
	var seen []uuid.UUID
	err := d.db.ReadTx(t.Context(), scope, func(tx pg.Tx) error {
		rows, err := tx.Query(context.Background(), `SELECT p.id FROM routing.presets p JOIN routing.preset_versions v ON v.preset_id = p.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seen = append(seen, id)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return seen
}
