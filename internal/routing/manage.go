package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// The refusals of the preset and default writes (docs/domains/routing.md「接口、权限与审计」).
var (
	// ErrPresetNotFound is a preset the transaction cannot read, or a disabled one asked to change.
	ErrPresetNotFound = errors.New("routing: no such routing preset")
	// ErrPresetReadOnly is a preset the transaction can read but not change: another layer's, or the system preset.
	ErrPresetReadOnly = errors.New("routing: the routing preset belongs to another layer")
	// ErrPresetSlugTaken is a slug a live preset of the layer already has.
	ErrPresetSlugTaken = errors.New("routing: the layer has a routing preset with that slug")
	// ErrPresetStale is a write made against a revision of a preset, or a version of a default, that has moved on.
	ErrPresetStale = errors.New("routing: the routing preset or default changed since it was read")
	// ErrPresetInUse is a preset a layer's default still points at, which disabling would leave new keys without.
	ErrPresetInUse = errors.New("routing: a default still points at the routing preset")
	// ErrPresetQuota is a write over one of the limits on what presets a tenant, or all of them, may hold.
	ErrPresetQuota = errors.New("routing: over the limit of routing presets")
	// ErrPresetCurrency is a change of a platform preset that would take it away from sites it serves: an unpriced one
	// gaining a currency, or a priced one changing its currency.
	ErrPresetCurrency = errors.New("routing: a platform preset keeps the currency it was made with")
	// ErrPresetNotSelectable is a preset a key or a default may not be pointed at: the system preset, a disabled one,
	// or one the key's or the layer's owners cannot see.
	ErrPresetNotSelectable = errors.New("routing: the routing preset cannot be chosen here")
)

// Layer is the layer a preset or a default belongs to: the owner IDs its scope has, and none of the others.
type Layer struct {
	Scope     PresetScope
	Site      SiteID
	Account   AccountID
	Workspace WorkspaceID
}

func (o Layer) ids() (site, account, workspace *uuid.UUID) {
	opt := func(id uuid.UUID, set bool) *uuid.UUID {
		if !set {
			return nil
		}
		return &id
	}
	return opt(uuid.UUID(o.Site), o.Scope >= ScopeSite), opt(uuid.UUID(o.Account), o.Scope >= ScopeAccount),
		opt(uuid.UUID(o.Workspace), o.Scope == ScopeWorkspace)
}

// match is the condition that picks the layer's row of a table with the three owner columns, with its arguments from
// $first: the columns the scope has are equal and the others are null, which the owner indexes can read, unlike IS NOT
// DISTINCT FROM.
func (o Layer) match(first int) (string, []any) {
	site, account, workspace := o.ids()
	var (
		conds []string
		args  []any
	)
	for _, c := range [...]struct {
		column string
		id     *uuid.UUID
	}{{"site_id", site}, {"account_id", account}, {"workspace_id", workspace}} {
		if c.id == nil {
			conds = append(conds, c.column+" IS NULL")
			continue
		}
		conds = append(conds, fmt.Sprintf("%s = $%d", c.column, first+len(args)))
		args = append(args, *c.id)
	}
	return strings.Join(conds, " AND "), args
}

// id names the layer in the audit log: its deepest owner, or the platform.
func (o Layer) id() string {
	switch o.Scope {
	case ScopePlatform:
		return o.Scope.String()
	case ScopeSite:
		return uuid.UUID(o.Site).String()
	case ScopeAccount:
		return uuid.UUID(o.Account).String()
	case ScopeWorkspace:
		return uuid.UUID(o.Workspace).String()
	}
	return o.Scope.String()
}

// StoredPreset is a preset as the layer that owns it manages it: its current version's rules as stored.
type StoredPreset struct {
	ID PresetID
	Layer
	Slug      string
	Name      string
	Revision  int64 // moves on every change
	Version   int64 // moves when the rules or their currency change
	Currency  platform.Currency
	Rules     jsonx.Raw
	System    bool
	Disabled  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PresetContent is what a write sets of a preset: its name and its rules, with the currency of a platform preset's
// price limits.
type PresetContent struct {
	Name     string
	Currency platform.Currency
	Rules    []byte
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

const presetColumns = `p.id, p.scope, p.site_id, p.account_id, p.workspace_id, p.slug, p.name, p.revision, p.current_version,
	v.currency, v.rules::text, p.is_system, p.deleted_at IS NOT NULL, p.created_at, p.updated_at
	FROM routing.presets p JOIN routing.preset_versions v ON v.preset_id = p.id AND v.version = p.current_version`

func scanPreset(row interface{ Scan(dest ...any) error }) (StoredPreset, error) {
	var (
		p                        StoredPreset
		id                       uuid.UUID
		scope                    string
		site, account, workspace *uuid.UUID
		currency                 *string
		rules                    string
	)
	err := row.Scan(&id, &scope, &site, &account, &workspace, &p.Slug, &p.Name, &p.Revision, &p.Version, &currency, &rules,
		&p.System, &p.Disabled, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return StoredPreset{}, err
	}
	p.ID, p.Rules = PresetID(id), jsonx.Raw(rules)
	p.Site, p.Account, p.Workspace = SiteID(orZero(site)), AccountID(orZero(account)), WorkspaceID(orZero(workspace))
	if p.Scope, err = ParseScope(scope); err != nil {
		return StoredPreset{}, err
	}
	if currency != nil {
		if p.Currency, err = platform.ParseCurrency(*currency); err != nil {
			return StoredPreset{}, fmt.Errorf("routing: preset %s: %w", id, err)
		}
	}
	return p, nil
}

func (p StoredPreset) visibleTo(a Admission) bool {
	return (&Preset{Scope: p.Scope, Site: p.Site, Account: p.Account, Workspace: p.Workspace, Currency: p.Currency}).visibleTo(a)
}

// ListPresets is the live presets a key of a may be bound to, the widest layer first: the system preset, which no one
// chooses, is not among them.
func ListPresets(ctx context.Context, tx pg.Tx, a Admission) ([]StoredPreset, error) {
	return queryPresets(ctx, tx, func(p StoredPreset) bool { return p.visibleTo(a) }, `SELECT `+presetColumns+` WHERE p.deleted_at IS NULL AND NOT p.is_system
		ORDER BY array_position(ARRAY['platform', 'site', 'account', 'workspace'], p.scope), p.slug`)
}

// LayerPresets is the layer's own presets as the layer's managers see them, the disabled ones after the live ones:
// the platform's include the system preset.
func LayerPresets(ctx context.Context, tx pg.Tx, layer Layer) ([]StoredPreset, error) {
	where, args := layer.match(1)
	return queryPresets(ctx, tx, func(StoredPreset) bool { return true },
		`SELECT `+presetColumns+` WHERE `+where+` ORDER BY p.deleted_at IS NOT NULL, p.slug`, args...)
}

// queryPresets is the presets query reads that keep keeps.
func queryPresets(ctx context.Context, tx pg.Tx, keep func(StoredPreset) bool, query string, args ...any) ([]StoredPreset, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("routing: list the presets: %w", err)
	}
	defer rows.Close()
	var out []StoredPreset
	for rows.Next() {
		p, err := scanPreset(rows)
		if err != nil {
			return nil, fmt.Errorf("routing: list the presets: %w", err)
		}
		if keep(p) {
			out = append(out, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing: list the presets: %w", err)
	}
	return out, nil
}

// GetPreset is the preset with this ID that tx can read, disabled or not.
func GetPreset(ctx context.Context, tx pg.Tx, id PresetID) (StoredPreset, error) {
	p, err := scanPreset(tx.QueryRow(ctx, `SELECT `+presetColumns+` WHERE p.id = $1`, uuid.UUID(id)))
	if errors.Is(err, pg.ErrNoRows) {
		return StoredPreset{}, ErrPresetNotFound
	}
	if err != nil {
		return StoredPreset{}, fmt.Errorf("routing: read preset %s: %w", uuid.UUID(id), err)
	}
	return p, nil
}

// CreatePreset makes a preset of layer with its first version, in tx, which must be in a scope that may write the
// layer, and records it with the actor's reason, which the platform's and sites' changes give.
func CreatePreset(ctx context.Context, tx pg.Tx, actor audit.Actor, layer Layer, slug string, c PresetContent, reason string) (StoredPreset, error) {
	rules, err := checkContent(ctx, tx, layer, c)
	if err != nil {
		return StoredPreset{}, err
	}
	if err := takeBudget(ctx, tx, layer, 1, int64(len(rules))); err != nil {
		return StoredPreset{}, err
	}
	id := uuid.NewV7()
	site, account, workspace := layer.ids()
	if _, err := tx.Exec(ctx, `INSERT INTO routing.presets (id, scope, site_id, account_id, workspace_id, slug, name, current_version, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8)`,
		id, layer.Scope.String(), site, account, workspace, slug, c.Name, actorID(actor)); err != nil {
		if pg.SQLState(err) == "23505" {
			return StoredPreset{}, ErrPresetSlugTaken
		}
		return StoredPreset{}, presetWriteErr("create a preset", err)
	}
	if err := insertVersion(ctx, tx, actor, PresetID(id), 1, c.Currency, rules); err != nil {
		return StoredPreset{}, err
	}
	p, err := GetPreset(ctx, tx, PresetID(id))
	if err != nil {
		return StoredPreset{}, err
	}
	return p, recordPreset(ctx, tx, actor, reason, "routing.preset.create", p, map[string]any{"slug": slug, "name": p.Name})
}

// UpdatePreset replaces the name and the rules of the preset at revision. Rules that differ from the current ones are
// a new version; the same rules keep it, and a write that changes nothing changes nothing, its revision included.
func UpdatePreset(ctx context.Context, tx pg.Tx, actor audit.Actor, id PresetID, revision int64, c PresetContent, reason string) (StoredPreset, error) {
	if err := lockBudget(ctx, tx); err != nil { // before the preset's row, in the order DisablePreset takes them
		return StoredPreset{}, err
	}
	before, err := lockPreset(ctx, tx, id, revision)
	if err != nil {
		return StoredPreset{}, err
	}
	rules, err := checkChange(ctx, tx, before, c)
	if err != nil {
		return StoredPreset{}, err
	}
	newRules := rules != string(before.Rules) || c.Currency != before.Currency
	if !newRules && c.Name == before.Name {
		return before, nil
	}
	if err := writeHead(ctx, tx, actor, before, c, rules, newRules); err != nil {
		return StoredPreset{}, err
	}
	p, err := GetPreset(ctx, tx, id)
	if err != nil {
		return StoredPreset{}, err
	}
	return p, recordPreset(ctx, tx, actor, reason, "routing.preset.update", p, map[string]any{
		"before": map[string]any{"name": before.Name, "version": before.Version, "rules_sha256": digest(string(before.Rules))},
		"after":  map[string]any{"name": p.Name, "version": p.Version},
	})
}

// writeHead writes the preset's new name and, when the rules changed, its next version, which it then prunes behind:
// a rename alone deletes no version, even after the kept versions were lowered (the prune follows the head's update,
// which the table's delete policy reads).
func writeHead(ctx context.Context, tx pg.Tx, actor audit.Actor, before StoredPreset, c PresetContent, rules string, newRules bool) error {
	version := before.Version
	if newRules {
		var err error
		if version, err = addVersion(ctx, tx, actor, before, c.Currency, rules); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE routing.presets SET name = $2, current_version = $3, revision = revision + 1,
		updated_at = statement_timestamp() WHERE id = $1`, uuid.UUID(before.ID), c.Name, version); err != nil {
		return presetWriteErr("update a preset", err)
	}
	if !newRules {
		return nil
	}
	return pruneVersions(ctx, tx, before.ID, version)
}

// pruneVersions keeps the preset's last versions, as many as the limits say, so rules that change without growing
// cannot pile versions up for good; the audit log keeps every version's hash. The table's policy deletes none of the
// last minKept, whatever the record says.
func pruneVersions(ctx context.Context, tx pg.Tx, id PresetID, current int64) error {
	lim, _, err := ReadLimits(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM routing.preset_versions WHERE preset_id = $1 AND version <= $2`,
		uuid.UUID(id), current-lim.Effective().KeptVersions); err != nil {
		return presetWriteErr("prune the preset's versions", err)
	}
	return nil
}

// checkChange is checkContent for a change of before, which may not make a platform preset serve fewer sites. It serves
// the sites of its currency, or every site when it limits no price: the keys and defaults of a site it stopped serving
// would turn unavailable, and routing cannot list the keys, which are accounts'. Prices in another currency are a new
// preset.
func checkChange(ctx context.Context, tx pg.Tx, before StoredPreset, c PresetContent) (string, error) {
	if before.Scope == ScopePlatform && c.Currency != before.Currency && c.Currency != (platform.Currency{}) {
		return "", ErrPresetCurrency
	}
	return checkContent(ctx, tx, before.Layer, c)
}

// addVersion adds the next version of the preset's rules, within the global budget, and returns its number.
func addVersion(ctx context.Context, tx pg.Tx, actor audit.Actor, before StoredPreset, currency platform.Currency, rules string) (int64, error) {
	if err := takeBudget(ctx, tx, before.Layer, 0, int64(len(rules)-len(before.Rules))); err != nil {
		return 0, err
	}
	return before.Version + 1, insertVersion(ctx, tx, actor, before.ID, before.Version+1, currency, rules)
}

// DisablePreset disables the preset at revision: the keys bound to it keep routing by it, and no new key or default
// can choose it. A preset a default still points at is refused (ErrPresetInUse); the default is changed first, so new
// keys never fall silently to a wider layer's default. A disabled preset keeps counting against the tenant's total, so
// making and disabling presets in turn cannot grow the table without bound.
func DisablePreset(ctx context.Context, tx pg.Tx, actor audit.Actor, id PresetID, revision int64, reason string) error {
	// The budget row orders this with the default writes, which check that the preset they point at is live.
	if err := lockBudget(ctx, tx); err != nil {
		return err
	}
	before, err := lockPreset(ctx, tx, id, revision)
	if err != nil {
		return err
	}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM routing.preset_defaults WHERE preset_id = $1)`, uuid.UUID(id)).Scan(&used); err != nil {
		return fmt.Errorf("routing: find the defaults of preset %s: %w", uuid.UUID(id), err)
	}
	if used {
		return ErrPresetInUse
	}
	if _, err := tx.Exec(ctx, `UPDATE routing.presets SET deleted_at = statement_timestamp(), revision = revision + 1,
		updated_at = statement_timestamp() WHERE id = $1`, uuid.UUID(id)); err != nil {
		return presetWriteErr("disable a preset", err)
	}
	return recordPreset(ctx, tx, actor, reason, "routing.preset.disable", before, nil)
}

// lockPreset is the live preset tx may change, at revision, held for the rest of tx. A row the scope may read but not
// change, which FOR UPDATE leaves out under the table's policies, is ErrPresetReadOnly.
func lockPreset(ctx context.Context, tx pg.Tx, id PresetID, revision int64) (StoredPreset, error) {
	p, err := scanPreset(tx.QueryRow(ctx, `SELECT `+presetColumns+` WHERE p.id = $1 AND p.deleted_at IS NULL FOR UPDATE OF p`, uuid.UUID(id)))
	if errors.Is(err, pg.ErrNoRows) {
		read, readErr := GetPreset(ctx, tx, id)
		switch {
		case readErr != nil:
			return StoredPreset{}, readErr
		case read.Disabled:
			return StoredPreset{}, ErrPresetNotFound
		}
		return StoredPreset{}, ErrPresetReadOnly
	}
	if err != nil {
		return StoredPreset{}, fmt.Errorf("routing: lock preset %s: %w", uuid.UUID(id), err)
	}
	if p.Revision != revision {
		return StoredPreset{}, ErrPresetStale
	}
	return p, nil
}

// checkContent is c's rules as the table stores them, after the one check every version passes (Preset.ReadRules)
// against the layer. The rules are checked as PostgreSQL prints them back, which is the text a snapshot reads
// and the size the table bounds.
func checkContent(ctx context.Context, tx pg.Tx, layer Layer, c PresetContent) (string, error) {
	var rules string
	if err := tx.QueryRow(ctx, `SELECT $1::jsonb::text`, string(c.Rules)).Scan(&rules); err != nil {
		if strings.HasPrefix(pg.SQLState(err), "22") {
			return "", fmt.Errorf("%w: not JSON", ErrRules)
		}
		return "", fmt.Errorf("routing: read the rules: %w", err)
	}
	p := Preset{Scope: layer.Scope, Site: layer.Site, Account: layer.Account, Workspace: layer.Workspace, Currency: c.Currency}
	return rules, p.ReadRules([]byte(rules))
}

func insertVersion(ctx context.Context, tx pg.Tx, actor audit.Actor, id PresetID, version int64, currency platform.Currency, rules string) error {
	var priced *string
	if currency != (platform.Currency{}) {
		code := currency.String()
		priced = &code
	}
	if _, err := tx.Exec(ctx, `INSERT INTO routing.preset_versions (preset_id, version, rules, currency, created_by)
		VALUES ($1, $2, $3::jsonb, $4, $5)`, uuid.UUID(id), version, rules, priced, actorID(actor)); err != nil {
		return presetWriteErr("add a preset version", err)
	}
	return nil
}

// presetWriteErr is a refused write: a scope that may not write the layer is refused by the tables' policies.
func presetWriteErr(what string, err error) error {
	if pg.SQLState(err) == "42501" {
		return fmt.Errorf("routing: %s: %w", what, ErrPresetReadOnly)
	}
	return fmt.Errorf("routing: %s: %w", what, err)
}

func lockBudget(ctx context.Context, tx pg.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM routing.preset_budget FOR UPDATE`); err != nil {
		return fmt.Errorf("routing: lock the preset budget: %w", err)
	}
	return nil
}

// takeBudget adds presets and bytes of current rules to the global budget, and checks a new preset against its layer's
// tenant limits, after the budget row is taken: every write that adds either takes it first, so two writes cannot
// both take the last place. bytes is negative for rules that shrink, which pass even over a budget lowered since.
func takeBudget(ctx context.Context, tx pg.Tx, layer Layer, presets, bytes int64) error {
	if err := lockBudget(ctx, tx); err != nil { // the limits are read under the lock SetLimits takes
		return err
	}
	saved, _, err := ReadLimits(ctx, tx)
	if err != nil {
		return err
	}
	lim := saved.Effective()
	tag, err := tx.Exec(ctx, `UPDATE routing.preset_budget SET presets = presets + $1, bytes = greatest(bytes + $2, 0)
		WHERE ($1 = 0 OR presets + $1 <= $3) AND ($2 <= 0 OR bytes + $2 <= $4)`, presets, bytes, lim.Presets, lim.Bytes)
	if err != nil {
		return fmt.Errorf("routing: take the preset budget: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPresetQuota
	}
	if presets == 0 {
		return nil
	}
	for _, limit := range limitsOf(layer, lim) {
		var active, total int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE deleted_at IS NULL), count(*) FROM routing.presets
			WHERE `+limit.where, limit.args...).Scan(&active, &total); err != nil {
			return fmt.Errorf("routing: count the %s's presets: %w", limit.owner, err)
		}
		if active+presets > limit.Active || total+presets > limit.Total {
			return ErrPresetQuota
		}
	}
	return nil
}

func recordPreset(ctx context.Context, tx pg.Tx, actor audit.Actor, reason, action string, p StoredPreset, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	detail["scope"], detail["version"], detail["rules_sha256"] = p.Scope.String(), p.Version, digest(string(p.Rules))
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: action, Reason: reason, Detail: detail, Target: audit.Target{
		Module: "routing", Kind: "preset", ID: uuid.UUID(p.ID).String(), Site: uuid.UUID(p.Site), Account: uuid.UUID(p.Account),
	}})
}

func digest(rules string) string {
	sum := sha256.Sum256([]byte(rules))
	return hex.EncodeToString(sum[:])
}

// actorID is the user behind a change, for the created_by and updated_by columns; another kind of actor is none.
func actorID(a audit.Actor) *uuid.UUID {
	if id, ok := a.UserID(); ok {
		return &id
	}
	return nil
}
