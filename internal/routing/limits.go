package routing

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// Limits are the administrator's limits on routing presets (docs/domains/routing.md「滚动发布、失败与性能」): how many
// presets each layer may hold, live and with the disabled ones, how many rule versions a preset keeps, and the global
// budget every node's snapshot holds, presets and bytes of current rules. A zero value is the default in code, so the
// record holds only deliberate departures; Effective fills the rest.
type Limits struct {
	Platform, Site, Account, Workspace LayerLimits
	KeptVersions                       int64
	Presets, Bytes                     int64
}

// LayerLimits bound the presets of one layer: the live ones, and all of them with the disabled ones.
type LayerLimits struct{ Active, Total int64 }

// DefaultLimits are the limits that apply where the administrator recorded none. The platform's and a site's are wider
// than an account's because every key of the platform or the site chooses among them, and narrow enough that the list
// a key chooses from, every layer's live presets together, stays one a page can show whole.
func DefaultLimits() Limits {
	return Limits{
		Platform: LayerLimits{50, 500}, Site: LayerLimits{50, 500},
		Account: LayerLimits{20, 200}, Workspace: LayerLimits{10, 100},
		KeptVersions: 50, Presets: 100_000, Bytes: 256 << 20,
	}
}

// The bounds a recorded limit must keep, which the table's checks repeat. The live presets of the four layers make the
// list a key chooses from, so they stay at a size a page can show whole. A preset keeps at least the 50 versions the
// table's delete policy keeps, and at most what 500 versions of 16 KiB rules take. The global budget is what every
// node holds in memory, so its ceilings are ten and eight times the defaults: a typo cannot ask a node for more than
// that, and going beyond it is a change of the nodes' sizing, not a setting.
const (
	maxActiveLimit = 500
	maxTotalLimit  = 5000
	minKept        = 50
	maxKept        = 500
	maxPresets     = 1_000_000
	minBytes       = 1 << 20
	maxBytes       = 2 << 30
)

// Effective is the limits a write is held to: every zero value falls back to its code default.
func (l *Limits) Effective() Limits {
	d := DefaultLimits()
	layer := func(v, d LayerLimits) LayerLimits {
		return LayerLimits{orDefault(v.Active, d.Active), orDefault(v.Total, d.Total)}
	}
	return Limits{
		Platform: layer(l.Platform, d.Platform), Site: layer(l.Site, d.Site),
		Account: layer(l.Account, d.Account), Workspace: layer(l.Workspace, d.Workspace),
		KeptVersions: orDefault(l.KeptVersions, d.KeptVersions),
		Presets:      orDefault(l.Presets, d.Presets), Bytes: orDefault(l.Bytes, d.Bytes),
	}
}

// layers names each layer's limits as the admin API does, widest first.
func (l *Limits) layers() []struct {
	name   string
	limits *LayerLimits
} {
	return []struct {
		name   string
		limits *LayerLimits
	}{{"platform", &l.Platform}, {"site", &l.Site}, {"account", &l.Account}, {"workspace", &l.Workspace}}
}

// Check reports the first value outside its bounds, and a layer whose effective live limit is above its total, which
// would let no more presets live than the total does anyway and so says something it does not do.
func (l *Limits) Check() error {
	if err := l.bounds(); err != nil {
		return err
	}
	e := l.Effective()
	for _, layer := range e.layers() {
		if layer.limits.Active > layer.limits.Total {
			return &LimitError{layer.name + "/active", "must be at most the layer's total"}
		}
	}
	return nil
}

// bounds reports the first recorded value outside its bounds.
func (l *Limits) bounds() error {
	for _, layer := range l.layers() {
		if v := layer.limits.Active; v != 0 && (v < 1 || v > maxActiveLimit) {
			return &LimitError{layer.name + "/active", fmt.Sprintf("must be from 1 to %d", maxActiveLimit)}
		}
		if v := layer.limits.Total; v != 0 && (v < 1 || v > maxTotalLimit) {
			return &LimitError{layer.name + "/total", fmt.Sprintf("must be from 1 to %d", maxTotalLimit)}
		}
	}
	for _, b := range []struct {
		field         string
		value, lo, hi int64
	}{
		{"kept_versions", l.KeptVersions, minKept, maxKept},
		{"global/presets", l.Presets, 1, maxPresets},
		{"global/bytes", l.Bytes, minBytes, maxBytes},
	} {
		if b.value != 0 && (b.value < b.lo || b.value > b.hi) {
			return &LimitError{b.field, fmt.Sprintf("must be from %d to %d", b.lo, b.hi)}
		}
	}
	return nil
}

// layerLimit bounds the presets of a layer: the live ones, and all of them with the disabled ones, which keep their
// rows for the keys bound to them (docs/domains/routing.md「滚动发布、失败与性能」). where picks the presets it counts,
// with args. Limits bound no bytes: a version's rules are at most maxRulesBytes, so the most presets a layer holds
// already bound what it can write.
type layerLimit struct {
	owner string
	where string
	args  []any
	LayerLimits
}

// limitsOf are the limits of lim a new preset of the layer is held to. An account's count its workspaces' presets too,
// since the account is the tenant, so a workspace's preset is held to both.
func limitsOf(layer Layer, lim Limits) []layerLimit {
	site, account, workspace := uuid.UUID(layer.Site), uuid.UUID(layer.Account), uuid.UUID(layer.Workspace)
	accountLimit := layerLimit{"account", "site_id = $1 AND account_id = $2", []any{site, account}, lim.Account}
	switch layer.Scope {
	case ScopePlatform:
		return []layerLimit{{"platform", "scope = 'platform'", nil, lim.Platform}}
	case ScopeSite:
		return []layerLimit{{"site", "scope = 'site' AND site_id = $1", []any{site}, lim.Site}}
	case ScopeAccount:
		return []layerLimit{accountLimit}
	case ScopeWorkspace:
		return []layerLimit{accountLimit, {"workspace", "site_id = $1 AND workspace_id = $2", []any{site, workspace}, lim.Workspace}}
	}
	return nil
}

// LimitError is an administrator's limit outside its bounds. Field is its path in the admin API's limits object.
type LimitError struct{ Field, Reason string }

func (e *LimitError) Error() string { return "routing: limit " + e.Field + " " + e.Reason }

const limitColumns = `platform_active, platform_total, site_active, site_total, account_active, account_total,
	workspace_active, workspace_total, kept_versions, max_presets, max_bytes`

// ReadLimits reads the administrator's record, which every scope may read, and its revision, which a change names; no
// row is all defaults at revision 0.
func ReadLimits(ctx context.Context, tx pg.Tx) (Limits, int64, error) {
	var (
		l    Limits
		rev  int64
		cols [11]*int64
	)
	dest := []any{&rev}
	for i := range cols {
		dest = append(dest, &cols[i])
	}
	err := tx.QueryRow(ctx, `SELECT rev, `+limitColumns+` FROM routing.preset_limits WHERE singleton`).Scan(dest...)
	if errors.Is(err, pg.ErrNoRows) {
		return Limits{}, 0, nil
	}
	if err != nil {
		return Limits{}, 0, fmt.Errorf("routing: read the preset limits: %w", err)
	}
	for i, v := range l.fields() {
		if cols[i] != nil {
			*v = *cols[i]
		}
	}
	return l, rev, nil
}

// fields are l's values in limitColumns' order.
func (l *Limits) fields() []*int64 {
	return []*int64{
		&l.Platform.Active, &l.Platform.Total, &l.Site.Active, &l.Site.Total, &l.Account.Active, &l.Account.Total,
		&l.Workspace.Active, &l.Workspace.Total, &l.KeptVersions, &l.Presets, &l.Bytes,
	}
}

// SetLimits validates and replaces the record at rev, the revision the caller read (ErrPresetStale when it moved on),
// in the platform scope, records it with the actor's reason, and returns the new revision. The row stays when no value
// is left, every column NULL, so the defaults are still the only source of what is not deliberately different and a
// revision is never handed out twice. It takes the budget row first, as every preset write does, so a write is held
// either to the limits before or to the ones after, and two changes cannot interleave their before and after.
func SetLimits(ctx context.Context, tx pg.Tx, actor audit.Actor, l Limits, rev int64, reason string) (int64, error) {
	if err := l.Check(); err != nil {
		return 0, err
	}
	if err := lockBudget(ctx, tx); err != nil {
		return 0, err
	}
	before, current, err := ReadLimits(ctx, tx)
	if err != nil {
		return 0, err
	}
	if rev != current {
		return 0, ErrPresetStale
	}
	args := []any{current + 1}
	for _, v := range l.fields() {
		args = append(args, nullable(*v))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO routing.preset_limits (rev, `+limitColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (singleton) DO UPDATE SET (rev, `+limitColumns+`) = ROW($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		args...); err != nil {
		return 0, presetWriteErr("write the preset limits", err)
	}
	return current + 1, audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "routing.limits.update", Reason: reason,
		Target: audit.Target{Module: "routing", Kind: "preset_limits", ID: uuid.Nil().String()},
		Detail: map[string]any{"before": before.detail(), "after": l.detail(), "rev": current + 1},
	})
}

// detail is the record as the audit log keeps it: a value left to its default is null.
func (l *Limits) detail() map[string]any {
	out := map[string]any{
		"kept_versions": nullable(l.KeptVersions),
		"global":        map[string]any{"presets": nullable(l.Presets), "bytes": nullable(l.Bytes)},
	}
	for _, layer := range l.layers() {
		out[layer.name] = map[string]any{"active": nullable(layer.limits.Active), "total": nullable(layer.limits.Total)}
	}
	return out
}

func nullable(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
