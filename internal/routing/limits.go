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
// presets each layer may hold, live and with the disabled ones, and how many rule versions a preset keeps. A zero value
// is the default in code, so the record holds only deliberate departures; Effective fills the rest.
type Limits struct {
	Platform, Site, Account, Workspace LayerLimits
	KeptVersions                       int64
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
		KeptVersions: 50,
	}
}

// The bounds a recorded limit must keep, which the table's checks repeat. The live presets of the four layers make the
// list a key chooses from, so they stay at a size a page can show whole. A preset keeps at least the 50 versions the
// table's delete policy keeps, and at most what 500 versions of 16 KiB rules take.
const (
	maxActiveLimit = 500
	maxTotalLimit  = 5000
	minKept        = 50
	maxKept        = 500
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
	if v := l.KeptVersions; v != 0 && (v < minKept || v > maxKept) {
		return &LimitError{"kept_versions", fmt.Sprintf("must be from %d to %d", minKept, maxKept)}
	}
	return nil
}

// LimitError is an administrator's limit outside its bounds. Field is its path in the admin API's limits object.
type LimitError struct{ Field, Reason string }

func (e *LimitError) Error() string { return "routing: limit " + e.Field + " " + e.Reason }

const limitColumns = `platform_active, platform_total, site_active, site_total, account_active, account_total,
	workspace_active, workspace_total, kept_versions`

// ReadLimits reads the administrator's record, which every scope may read; no row is all defaults.
func ReadLimits(ctx context.Context, tx pg.Tx) (Limits, error) {
	var (
		l    Limits
		cols [9]*int64
	)
	dest := make([]any, len(cols))
	for i := range cols {
		dest[i] = &cols[i]
	}
	err := tx.QueryRow(ctx, `SELECT `+limitColumns+` FROM routing.preset_limits WHERE singleton`).Scan(dest...)
	if errors.Is(err, pg.ErrNoRows) {
		return Limits{}, nil
	}
	if err != nil {
		return Limits{}, fmt.Errorf("routing: read the preset limits: %w", err)
	}
	for i, v := range l.fields() {
		if cols[i] != nil {
			*v = *cols[i]
		}
	}
	return l, nil
}

// fields are l's values in limitColumns' order.
func (l *Limits) fields() []*int64 {
	return []*int64{
		&l.Platform.Active, &l.Platform.Total, &l.Site.Active, &l.Site.Total, &l.Account.Active, &l.Account.Total,
		&l.Workspace.Active, &l.Workspace.Total, &l.KeptVersions,
	}
}

// SetLimits validates and replaces the record, in the platform scope, and records it with the actor's reason. A record
// with no value is deleted, so the defaults stay the only source of what is not deliberately different. It takes the
// budget row first, as every preset write does, so a write is held either to the limits before or to the ones after,
// and two changes cannot interleave their before and after in the audit log.
func SetLimits(ctx context.Context, tx pg.Tx, actor audit.Actor, l Limits, reason string) error {
	if err := l.Check(); err != nil {
		return err
	}
	if err := lockBudget(ctx, tx); err != nil {
		return err
	}
	before, err := ReadLimits(ctx, tx)
	if err != nil {
		return err
	}
	if l == (Limits{}) {
		_, err = tx.Exec(ctx, `DELETE FROM routing.preset_limits`)
	} else {
		args := make([]any, 0, 9)
		for _, v := range l.fields() {
			args = append(args, nullable(*v))
		}
		_, err = tx.Exec(ctx, `INSERT INTO routing.preset_limits (`+limitColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (singleton) DO UPDATE SET (`+limitColumns+`) = ROW($1, $2, $3, $4, $5, $6, $7, $8, $9)`, args...)
	}
	if err != nil {
		return presetWriteErr("write the preset limits", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "routing.limits.update", Reason: reason,
		Target: audit.Target{Module: "routing", Kind: "preset_limits", ID: uuid.Nil().String()},
		Detail: map[string]any{"before": before.detail(), "after": l.detail()},
	})
}

// detail is the record as the audit log keeps it: a value left to its default is null.
func (l *Limits) detail() map[string]any {
	out := map[string]any{"kept_versions": nullable(l.KeptVersions)}
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
