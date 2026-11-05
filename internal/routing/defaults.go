package routing

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// The defaults of the layers and the checks of a chosen preset (docs/domains/routing.md「默认链与 key 落章」); the
// preset writes they are ordered with are in manage.go.

// Default is a layer's default: the preset it points at, nil when the layer sets none, and its version, 0 when it has
// never set one or cleared it.
type Default struct {
	Preset  *PresetID
	Version int64
}

// ReadDefault is the default the layer sets itself, not the one its keys inherit.
func ReadDefault(ctx context.Context, tx pg.Tx, layer Layer) (Default, error) {
	var (
		d  Default
		id uuid.UUID
	)
	where, args := layer.match(1)
	err := tx.QueryRow(ctx, `SELECT preset_id, version FROM routing.preset_defaults WHERE `+where, args...).Scan(&id, &d.Version)
	if errors.Is(err, pg.ErrNoRows) {
		return Default{}, nil
	}
	if err != nil {
		return Default{}, fmt.Errorf("routing: read the %s's default preset: %w", layer.Scope, err)
	}
	d.Preset = (*PresetID)(&id)
	return d, nil
}

// SetDefault points the layer's default at preset, or clears it when preset is nil, if the default is still at
// version. currency is the layer's site's, which a platform preset's price limits must be in; the platform's own
// default may point at no preset that limits a price, since it serves sites of every currency. New keys of the layer
// are bound to the new default; the keys already made keep theirs. The change is recorded with the actor's reason.
func SetDefault(ctx context.Context, tx pg.Tx, actor audit.Actor, layer Layer, currency platform.Currency, preset *PresetID, version int64, reason string) (Default, error) {
	if err := lockWrites(ctx, tx); err != nil { // ordered with DisablePreset: a default never points at a disabled preset
		return Default{}, err
	}
	before, err := ReadDefault(ctx, tx, layer)
	if err != nil {
		return Default{}, err
	}
	if before.Version != version {
		return Default{}, ErrPresetStale
	}
	if preset != nil {
		if err := selectable(ctx, tx, Admission{Site: layer.Site, Account: layer.Account, Workspace: layer.Workspace, Currency: currency}, *preset, layer.Scope == ScopePlatform); err != nil {
			return Default{}, err
		}
	}
	if sameDefault(before.Preset, preset) {
		return before, nil
	}
	if err := writeDefault(ctx, tx, actor, layer, before.Preset == nil, preset); err != nil {
		return Default{}, presetWriteErr("set the default preset", err)
	}
	after, err := ReadDefault(ctx, tx, layer)
	if err != nil {
		return Default{}, err
	}
	return after, audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "routing.default.update", Reason: reason,
		Target: audit.Target{Module: "routing", Kind: "preset_default", ID: layer.id(), Site: uuid.UUID(layer.Site), Account: uuid.UUID(layer.Account)},
		Detail: map[string]any{"scope": layer.Scope.String(), "before": optionalID(before.Preset), "after": optionalID(after.Preset)},
	})
}

func sameDefault(a, b *PresetID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// writeDefault clears the layer's default when preset is nil, or points it at preset: a new row when the layer had
// none, or a change of its row. Every write takes a new version from one sequence, so a version is never given twice,
// not even to a layer that cleared its default and set it again.
func writeDefault(ctx context.Context, tx pg.Tx, actor audit.Actor, layer Layer, unset bool, preset *PresetID) error {
	var err error
	switch {
	case preset == nil:
		where, args := layer.match(1)
		_, err = tx.Exec(ctx, `DELETE FROM routing.preset_defaults WHERE `+where, args...)
	case unset:
		site, account, workspace := layer.ids()
		_, err = tx.Exec(ctx, `INSERT INTO routing.preset_defaults (scope, site_id, account_id, workspace_id, preset_id, updated_by, version)
			VALUES ($1, $2, $3, $4, $5, $6, nextval('routing.preset_default_versions'))`, layer.Scope.String(), site, account, workspace, uuid.UUID(*preset), actorID(actor))
	default:
		where, args := layer.match(3)
		_, err = tx.Exec(ctx, `UPDATE routing.preset_defaults SET preset_id = $1, version = nextval('routing.preset_default_versions'), updated_by = $2,
			updated_at = statement_timestamp() WHERE `+where, append([]any{uuid.UUID(*preset), actorID(actor)}, args...)...)
	}
	return err
}

// SelectablePreset checks that a key of a may be bound to the preset, when it is made or changed: a live preset of a
// layer the key belongs to, priced in the key's site's currency, and not the system preset, which only a key with no
// default anywhere ends up with. While presets are suspended no key chooses one; a key made then is still bound to
// its layers' default (DefaultPreset), which it routes by once they resume.
func SelectablePreset(ctx context.Context, tx pg.Tx, a Admission, id PresetID) error {
	if err := refuseSuspended(ctx, tx); err != nil {
		return err
	}
	return selectable(ctx, tx, a, id, false)
}

func selectable(ctx context.Context, tx pg.Tx, a Admission, id PresetID, system bool) error {
	if id == SystemPreset && !system {
		return ErrPresetNotSelectable
	}
	p, err := GetPreset(ctx, tx, id)
	switch {
	case errors.Is(err, ErrPresetNotFound):
		return ErrPresetNotSelectable
	case err != nil:
		return err
	case p.Disabled || !p.visibleTo(a):
		return ErrPresetNotSelectable
	}
	return nil
}

func optionalID(id *PresetID) any {
	if id == nil {
		return nil
	}
	return uuid.UUID(*id).String()
}
