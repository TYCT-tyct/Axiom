package routing

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"sync"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Module is routing's migrations, for cmd/axiom to give pg.Migrate and pg.DB.Check.
func Module() pg.Module {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	return pg.Module{Name: "routing", Migrations: files}
}

// SystemPreset is the allow-all preset of the zero ID, which the migration writes: a key that names no preset is routed
// by it, and so is a new key when no layer sets a default.
var SystemPreset PresetID

// ErrDefaultInvalid is a layer's default that a new key cannot be bound to: the preset is disabled, not visible to
// the key, or limits prices in another currency. The key is refused rather than bound to a wider layer's default,
// which could drop the price limits or sources the layer meant (docs/domains/routing.md「默认链与 key 落章」).
var ErrDefaultInvalid = errors.New("routing: the default routing preset cannot be used for this key")

// DefaultPreset is the preset a new call key of the workspace is bound to: the default of the first layer, from the
// workspace up to the platform, that sets one, and the system preset when none does. tx is the transaction that makes
// the key, in the key's own scope, so a layer the key's account cannot read is not one of its layers; it reads the
// defaults as they are now, not the routing snapshot's.
func DefaultPreset(ctx context.Context, tx pg.Tx, site SiteID, account AccountID, workspace WorkspaceID, currency platform.Currency) (PresetID, error) {
	var (
		found                   *uuid.UUID
		scope                   *string
		siteID, accountID, work *uuid.UUID
		disabled                bool
		priced                  *string
	)
	// The unique index on the layer columns leaves at most four rows; the deepest layer is the one with most owners.
	err := tx.QueryRow(ctx, `SELECT p.id, p.scope, p.site_id, p.account_id, p.workspace_id, p.deleted_at IS NOT NULL, v.currency
		FROM routing.preset_defaults d
		LEFT JOIN routing.presets p ON p.id = d.preset_id
		LEFT JOIN routing.preset_versions v ON v.preset_id = p.id AND v.version = p.current_version
		WHERE d.site_id IS NULL
			OR d.site_id = $1 AND d.account_id IS NULL
			OR d.site_id = $1 AND d.account_id = $2 AND (d.workspace_id IS NULL OR d.workspace_id = $3)
		ORDER BY (d.site_id IS NOT NULL)::int + (d.account_id IS NOT NULL)::int + (d.workspace_id IS NOT NULL)::int DESC
		LIMIT 1`, uuid.UUID(site), uuid.UUID(account), uuid.UUID(workspace),
	).Scan(&found, &scope, &siteID, &accountID, &work, &disabled, &priced)
	if errors.Is(err, pg.ErrNoRows) {
		return SystemPreset, nil
	}
	if err != nil {
		return PresetID{}, fmt.Errorf("routing: read the default preset: %w", err)
	}
	if found == nil { // the default names a preset this scope cannot read; its columns are all NULL
		return PresetID{}, ErrDefaultInvalid
	}
	p := Preset{ID: PresetID(*found), Site: SiteID(orZero(siteID)), Account: AccountID(orZero(accountID)), Workspace: WorkspaceID(orZero(work))}
	if p.Scope, err = ParseScope(*scope); err != nil {
		return PresetID{}, err
	}
	if priced != nil {
		if p.Currency, err = platform.ParseCurrency(*priced); err != nil {
			return PresetID{}, fmt.Errorf("routing: preset %s: %w", uuid.UUID(p.ID), err)
		}
	}
	if disabled || !p.visibleTo(Admission{Site: site, Account: account, Workspace: workspace, Currency: currency}) {
		return PresetID{}, ErrDefaultInvalid
	}
	return p.ID, nil
}

func orZero(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.UUID{}
	}
	return *id
}

// Presets reads the routing presets a snapshot is built with. It compiles a version once and keeps it while the preset
// points at it, so a rebuild reads the rules of the versions that changed only. A preset whose rules this node cannot
// read is left out of the snapshot and logged once: the keys bound to it are refused as unavailable, and every other
// preset still routes. Disabled presets are read too, because the keys bound to them still route by them. While the
// platform has suspended presets (SetSuspension) the snapshot holds the system preset alone, and the others stay
// compiled for when they resume.
type Presets struct {
	Logger *slog.Logger

	mu        sync.Mutex
	compiled  map[presetVersion]compiledVersion
	suspended bool // as the last load read it, so a change is logged once
}

type presetVersion struct {
	id      PresetID
	version int64
}

// compiledVersion is a version as this node read it: the preset, or why it could not be.
type compiledVersion struct {
	preset Preset
	err    error
}

// presetHeader is a preset's row: who it belongs to and which version it is at.
type presetHeader struct {
	id                       uuid.UUID
	scope                    string
	site, account, workspace *uuid.UUID
	version                  int64
}

// Load reads every preset in tx, which must see them all (the platform scope). It fails, and the snapshot is not
// replaced, only when the tables cannot be read or the system preset is missing or unreadable: without it, every key
// with no preset of its own would be refused.
func (s *Presets) Load(ctx context.Context, tx pg.Tx) ([]Preset, error) {
	headers, err := readHeaders(ctx, tx)
	if err != nil {
		return nil, err
	}
	since, err := ReadSuspension(ctx, tx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[presetVersion]compiledVersion, len(headers))
	var missing []presetHeader
	for _, h := range headers {
		key := presetVersion{PresetID(h.id), h.version}
		if c, ok := s.compiled[key]; ok {
			next[key] = c
		} else {
			missing = append(missing, h)
		}
	}
	if err := s.compile(ctx, tx, missing, next); err != nil {
		return nil, err
	}
	s.compiled = next
	out := make([]Preset, 0, len(headers))
	system := false
	for _, h := range headers {
		c := next[presetVersion{PresetID(h.id), h.version}]
		if c.err != nil {
			if PresetID(h.id) == SystemPreset {
				return nil, fmt.Errorf("routing: the system preset: %w", c.err)
			}
			continue
		}
		system = system || c.preset.ID == SystemPreset
		out = append(out, c.preset)
	}
	if !system {
		return nil, errors.New("routing: the system preset is missing")
	}
	return s.suspendedTo(since, out), nil
}

// suspendedTo is the presets a snapshot holds while the platform has suspended them since since, a zero time when it
// has not: the system preset alone, or all of them. A change is logged once per node: while it lasts, every key bound
// to a preset other than the system's is refused.
func (s *Presets) suspendedTo(since time.Time, presets []Preset) []Preset {
	suspended := !since.IsZero()
	if suspended != s.suspended && s.Logger != nil {
		if suspended {
			s.Logger.Warn("routing presets are suspended: keys bound to a preset are refused", "event", "routing.presets_suspended", "since", since)
		} else {
			s.Logger.Info("routing presets are resumed", "event", "routing.presets_resumed")
		}
	}
	s.suspended = suspended
	if !suspended {
		return presets
	}
	return slices.DeleteFunc(presets, func(p Preset) bool { return p.ID != SystemPreset })
}

func readHeaders(ctx context.Context, tx pg.Tx) ([]presetHeader, error) {
	rows, err := tx.Query(ctx, `SELECT id, scope, site_id, account_id, workspace_id, current_version FROM routing.presets`)
	if err != nil {
		return nil, fmt.Errorf("routing: read the presets: %w", err)
	}
	defer rows.Close()
	var out []presetHeader
	for rows.Next() {
		var h presetHeader
		if err := rows.Scan(&h.id, &h.scope, &h.site, &h.account, &h.workspace, &h.version); err != nil {
			return nil, fmt.Errorf("routing: read the presets: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing: read the presets: %w", err)
	}
	return out, nil
}

// compile reads the rules of the versions not compiled yet and adds each, compiled or with its error, to into. A
// version that cannot be compiled is logged here, the one time it is read.
func (s *Presets) compile(ctx context.Context, tx pg.Tx, headers []presetHeader, into map[presetVersion]compiledVersion) error {
	if len(headers) == 0 {
		return nil
	}
	ids, versions := make([]uuid.UUID, len(headers)), make([]int64, len(headers))
	byKey := make(map[presetVersion]presetHeader, len(headers))
	for i, h := range headers {
		ids[i], versions[i] = h.id, h.version
		byKey[presetVersion{PresetID(h.id), h.version}] = h
	}
	// rules::text is the form the table bounds, so the size ReadRules checks is the one the table did.
	rows, err := tx.Query(ctx, `SELECT preset_id, version, currency, rules::text FROM routing.preset_versions
		WHERE (preset_id, version) IN (SELECT * FROM unnest($1::uuid[], $2::bigint[]))`, ids, versions)
	if err != nil {
		return fmt.Errorf("routing: read the preset rules: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id       uuid.UUID
			version  int64
			currency *string
			rules    string
		)
		if err := rows.Scan(&id, &version, &currency, &rules); err != nil {
			return fmt.Errorf("routing: read the preset rules: %w", err)
		}
		key := presetVersion{PresetID(id), version}
		c := compiledVersion{}
		c.preset, c.err = compiledPreset(byKey[key], currency, []byte(rules))
		if c.err != nil && s.Logger != nil {
			s.Logger.Warn("a routing preset is left out of the snapshot", "event", "routing.preset_isolated",
				"preset", id, "version", version, "err", c.err)
		}
		into[key] = c
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("routing: read the preset rules: %w", err)
	}
	for key := range byKey {
		if _, ok := into[key]; !ok { // the foreign key rules it out; a preset is never read without its version
			into[key] = compiledVersion{err: fmt.Errorf("%w: version %d is missing", ErrRules, key.version)}
		}
	}
	return nil
}

func compiledPreset(h presetHeader, currency *string, rules []byte) (Preset, error) {
	p := Preset{
		ID: PresetID(h.id), Version: h.version, Site: SiteID(orZero(h.site)), Account: AccountID(orZero(h.account)),
		Workspace: WorkspaceID(orZero(h.workspace)),
	}
	var err error
	if p.Scope, err = ParseScope(h.scope); err != nil {
		return Preset{}, err
	}
	if currency != nil {
		if p.Currency, err = platform.ParseCurrency(*currency); err != nil {
			return Preset{}, fmt.Errorf("%w: %w", ErrRules, err)
		}
	}
	if err := p.ReadRules(rules); err != nil {
		return Preset{}, err
	}
	if _, err := newPreset(p); err != nil { // what the snapshot would refuse, refused for this preset alone
		return Preset{}, err
	}
	return p, nil
}
