package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ErrKeyNotFound is a key the transaction cannot see, or already deleted.
var ErrKeyNotFound = errors.New("accounts: no such API key")

// ErrKeyPreset is a routing preset chosen for a key that has none: a management key, which is never routed.
var ErrKeyPreset = errors.New("accounts: a management key has no routing preset")

// ErrTooManyKeys is a key an account may not make now, over one of its site's two limits: the keys it holds that
// are not deleted, which deleting a key lifts, and the keys it makes in an hour, which waiting lifts. A site that lowers
// its limit leaves the keys already made usable.
var ErrTooManyKeys = errors.New("accounts: too many API keys")

// TooManyKeys is ErrTooManyKeys with the limit that was met and the keys counted against it. RetryAfter is set for the
// hourly limit: how long until the oldest of the keys counted is an hour old and a place is free.
type TooManyKeys struct {
	Limit, Count int
	RetryAfter   time.Duration
}

func (e *TooManyKeys) Error() string {
	return fmt.Sprintf("%s: %d counted against a limit of %d", ErrTooManyKeys, e.Count, e.Limit)
}

// Is makes errors.Is(err, ErrTooManyKeys) true for it.
func (*TooManyKeys) Is(target error) bool { return target == ErrTooManyKeys }

// Key kinds separate model calls from programmatic key administration.
const (
	CallKey       = "call"
	ManagementKey = "management"
)

// The reset windows of a spend limit.
const (
	ResetDaily   = "daily"
	ResetWeekly  = "weekly"
	ResetMonthly = "monthly"
)

// The states a key list can select, as the console shows them. An expired key is past its expiry whether it is
// enabled or not; a disabled key is one disabled before its expiry.
const (
	KeyStatusActive   = "active"
	KeyStatusDisabled = "disabled"
	KeyStatusExpired  = "expired"
)

// KeySettings are the settings of a key a user may change (README.md「API key」). What is left out is unrestricted: no
// spend limit, any model, any address. They satisfy the table's checks (a reset needs a limit; money is not negative),
// which the console's interface checks before it asks.
type KeySettings struct {
	Name     string
	Disabled bool
	// SpendLimit is in billionths of a credit, nil for none. LimitReset is a Reset constant, or empty for a limit that
	// never resets; it needs a SpendLimit. For a new console call key, a nil SpendLimit means the site's limit for new keys, reset
	// daily.
	SpendLimit *int64
	LimitReset string
	Models     []string // nil: any model
	IPAllow    []netip.Prefix
	// Preset is a call key's routing preset. Read, it is the one the key is bound to, nil for a management key and a
	// call key made before keys were bound. Written, it is the one chosen, checked by Config.KeyPreset; nil chooses
	// none, so a new key takes its layers' default and a changed key keeps its preset.
	Preset *uuid.UUID
}

// Equal compares settings by value, treating empty model and address lists as unrestricted like nil lists.
func (k KeySettings) Equal(other KeySettings) bool {
	return k.Name == other.Name && k.Disabled == other.Disabled && sameOptional(k.SpendLimit, other.SpendLimit) &&
		k.LimitReset == other.LimitReset && slices.Equal(k.Models, other.Models) && slices.Equal(k.IPAllow, other.IPAllow) &&
		sameOptional(k.Preset, other.Preset)
}

// Key is a key as the console shows it. Its text is not here: only its hash is kept, and the last four characters
// to tell keys apart.
type Key struct {
	Kind      string
	CreatedBy UserID
	ID        KeyID
	Workspace WorkspaceID
	Prefix    string
	Last4     string
	// CreatedAt is when the database stamped the key, on the clock that also judges the hour's limit.
	CreatedAt time.Time
	UpdatedAt *time.Time
	ExpiresAt *time.Time
	KeySettings
}

// KeyRequest is a new key in a workspace, made by a user of its site. A zero Workspace is the account's default
// workspace.
type KeyRequest struct {
	Kind      string // empty is call; management requires an expiry
	Site      SiteID
	Account   AccountID
	Workspace WorkspaceID
	CreatedBy UserID
	ExpiresAt *time.Time // only set when the key is made
	KeySettings
}

// CreatedKey is a new key with its text: the one time the text is anywhere but with the client, since only its hash
// is stored.
type CreatedKey struct {
	ID   KeyID
	Text secret.Secret
}

// CreateKey makes a call or management key in tx, with the site's prefix and, unless r says otherwise, the site's daily spend limit
// for new keys, and records it in the audit log in the same transaction. It refuses a key over one of the site's two
// limits on how many an account holds and how many it makes in an hour (ErrTooManyKeys).
func (s *Service) CreateKey(ctx context.Context, tx pg.Tx, actor audit.Actor, r KeyRequest) (CreatedKey, error) {
	return s.createKey(ctx, tx, actor, r, true)
}

func (s *Service) createKey(ctx context.Context, tx pg.Tx, actor audit.Actor, r KeyRequest, defaults bool) (CreatedKey, error) {
	if r.Kind == "" {
		r.Kind = CallKey
	}
	site, ok := s.snapshot.Load().sites[r.Site]
	if !ok {
		return CreatedKey{}, fmt.Errorf("create an API key: site %s is not in the snapshot", r.Site.TypeID())
	}
	if err := s.admitKey(ctx, tx, site, r.Account); err != nil {
		return CreatedKey{}, err
	}
	if r.Workspace == (WorkspaceID{}) {
		var err error
		if r.Workspace, err = s.defaultWorkspace(ctx, tx, r.Account); err != nil {
			return CreatedKey{}, fmt.Errorf("create an API key: %w", errors.Join(err, ErrKeyNotFound))
		}
	}
	limit, reset := r.SpendLimit, r.LimitReset
	if limit == nil && defaults && r.Kind == CallKey {
		limit, reset = &site.NewKeyDailyLimit, ResetDaily
	}
	text := newKeyText(site.KeyPrefix)
	id := KeyID(uuid.NewV7())
	version := s.versions[0]
	hash := s.hashes.Sum(version, []byte(text))
	key := InsertKeyParams{
		Kind: r.Kind, ID: uuid.UUID(id), SiteID: uuid.UUID(r.Site), AccountID: uuid.UUID(r.Account),
		WorkspaceID: uuid.UUID(r.Workspace), Name: r.Name, Prefix: site.KeyPrefix, Last4: text[len(text)-4:],
		SecretHash: hash[:], SecretVersion: int64(version), CreatedBy: uuid.UUID(r.CreatedBy),
		ExpiresAt: r.ExpiresAt, SpendLimit: limit, LimitReset: nullable(reset), Models: nilIfEmpty(r.Models), IpAllow: nilIfEmpty(r.IPAllow),
	}
	if err := s.insertKey(ctx, tx, &key, site.Currency, r.Preset); err != nil {
		return CreatedKey{}, err
	}
	if err := recordKey(ctx, tx, actor, "api_key.create", id, uuid.UUID(r.Site), uuid.UUID(r.Account), presetDetail(key.PresetID)); err != nil {
		return CreatedKey{}, err
	}
	return CreatedKey{id, secret.New(text)}, nil
}

// insertKey binds a new call key to the chosen routing preset, or with none chosen to its layers' default
// (Config.KeyPreset), in the transaction that makes it, and inserts it. A management key is never routed, so it has no
// preset; nor has any key when no KeyPreset was given.
func (s *Service) insertKey(ctx context.Context, tx pg.Tx, key *InsertKeyParams, currency platform.Currency, chosen *uuid.UUID) error {
	switch {
	case key.Kind == CallKey && s.keyPreset != nil:
		preset, err := s.keyPreset(ctx, tx, key.SiteID, key.AccountID, key.WorkspaceID, currency, chosen)
		if err != nil {
			return fmt.Errorf("create an API key: its routing preset: %w", err)
		}
		key.PresetID = &preset
	case chosen != nil:
		return ErrKeyPreset
	}
	if err := s.queries.InsertKey(ctx, tx, *key); err != nil {
		return fmt.Errorf("create an API key: %w", err)
	}
	return nil
}

// presetDetail is what the audit of a new key says of its routing preset.
func presetDetail(preset *uuid.UUID) map[string]any {
	if preset == nil {
		return nil
	}
	return map[string]any{"preset_id": preset.String()}
}

// keyWindow is the span the limit of keys made counts over.
const keyWindow = time.Hour

// admitKey checks the limits on the keys account holds and makes, in the transaction that makes one: the
// account's own over its site's over the defaults. The account's row is locked first, so that two creates at once are
// made one after the other and the second counts the key the first made: counting before the insert without the lock
// would let both take the last place. The counts are the database's, so they hold across nodes, and the lock is held
// to the end of the transaction, past the insert. The hour is judged on the database's clock, the one that stamps a key
// when it is made (RecentKeys, InsertKey), so nodes whose own clocks differ cannot stretch or shorten it; deleted keys
// count, so deleting one does not give its turn back.
func (s *Service) admitKey(ctx context.Context, tx pg.Tx, site Site, account AccountID) error {
	own, err := s.queries.LockAccount(ctx, tx, uuid.UUID(account))
	if errors.Is(err, pg.ErrNoRows) {
		return fmt.Errorf("create an API key: %w", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("create an API key: lock the account: %w", err)
	}
	limits := KeyLimits{own.MaxKeys, own.KeyCreationsPerHour}.over(EffectiveKeyLimits{site.MaxKeysPerAccount, site.KeyCreationsPerHour})
	held, err := s.queries.CountKeys(ctx, tx, uuid.UUID(account))
	if err != nil {
		return fmt.Errorf("create an API key: count the account's keys: %w", err)
	}
	if int(held) >= limits.MaxKeys {
		return &TooManyKeys{Limit: limits.MaxKeys, Count: int(held)}
	}
	made, err := s.queries.RecentKeys(ctx, tx, RecentKeysParams{
		AccountID: uuid.UUID(account), WindowSeconds: int64(keyWindow / time.Second), MaxMade: int64(limits.CreationsPerHour),
	})
	if err != nil {
		return fmt.Errorf("create an API key: count the keys it made in the last hour: %w", err)
	}
	if int(made.Made) >= limits.CreationsPerHour {
		return &TooManyKeys{Limit: limits.CreationsPerHour, Count: int(made.Made), RetryAfter: time.Duration(made.WaitSeconds * float64(time.Second))}
	}
	return nil
}

// nullable is s as a nullable column: empty is null.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nilIfEmpty is v, or nil when v has nothing in it: an empty list of models or addresses restricts nothing, and the
// table keeps "any" as null.
func nilIfEmpty[T any](v []T) []T {
	if len(v) == 0 {
		return nil
	}
	return v
}

// KeyQuery is one page of an account's keys, newest first. The zero After starts at the newest key. Empty filters do
// not narrow; Status is one of the KeyStatus constants.
type KeyQuery struct {
	After         KeyID
	Limit         int
	Name          string
	Prefix        string
	Status        string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
}

// ListKeys is up to q.Limit of account's keys that are not deleted and match q's filters, disabled ones included,
// newest first, starting after q.After (the zero KeyID for the first page); more tells whether keys remain beyond
// them. The page is read in index order: with no filter, or a dense one, a later page costs the rows it reads and
// not the account's history (Issue #408); a sparse filter reads on until the page is full or its range is exhausted,
// but still never sorts.
func (s *Service) ListKeys(ctx context.Context, tx pg.Tx, account AccountID, q KeyQuery) (keys []Key, more bool, err error) {
	// The scope's row estimate is below LIMIT when RLS is on, so PostgreSQL would otherwise prefer a bitmap scan of
	// every matching key and a sort; force the custom plan and keep the ordered index the page reads through.
	if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = force_custom_plan"); err != nil {
		return nil, false, fmt.Errorf("plan the key page: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL enable_sort = off"); err != nil {
		return nil, false, fmt.Errorf("read the key page in index order: %w", err)
	}
	before := uuid.Max()
	if q.After != (KeyID{}) {
		before = uuid.UUID(q.After)
	}
	rows, err := s.queries.ListKeys(ctx, tx, ListKeysParams{
		AccountID: uuid.UUID(account), Before: before, PageSize: int32(q.Limit) + 1, // #nosec G115 -- limit is the contract's 1 to 200
		Name: nullable(q.Name), Prefix: nullable(q.Prefix), Status: nullable(q.Status),
		CreatedAfter: q.CreatedAfter, CreatedBefore: q.CreatedBefore,
	})
	if err != nil {
		return nil, false, fmt.Errorf("list API keys: %w", err)
	}
	// The page is planned; the caller's next query in this transaction (the spend left) plans normally again.
	if _, err := tx.Exec(ctx, "SET LOCAL enable_sort = on"); err != nil {
		return nil, false, fmt.Errorf("restore the transaction's sort setting: %w", err)
	}
	if more = len(rows) > q.Limit; more {
		rows = rows[:q.Limit]
	}
	keys = make([]Key, len(rows))
	for i, r := range rows {
		keys[i] = keyOf(r)
	}
	return keys, more, nil
}

// GetKey is the key in tx's scope with this ID, or ErrKeyNotFound.
func (s *Service) GetKey(ctx context.Context, tx pg.Tx, id KeyID) (Key, error) {
	rows, err := s.queries.GetKey(ctx, tx, uuid.UUID(id))
	if err != nil {
		return Key{}, fmt.Errorf("get an API key: %w", err)
	}
	if len(rows) == 0 {
		return Key{}, ErrKeyNotFound
	}
	return keyOf(rows[0]), nil
}

// PresetKeys counts the keys bound to each of presets that are not deleted, disabled and expired ones included: those
// in tx's scope, or only account's when account is not nil. A preset no key is bound to is not in the map, nor is the
// zero ID, which names every unbound key (routing's system preset). One indexed read whatever the number of presets.
func (s *Service) PresetKeys(ctx context.Context, tx pg.Tx, account *AccountID, presets []uuid.UUID) (map[uuid.UUID]int64, error) {
	var rows []KeysByPresetRow
	var err error
	if account == nil {
		rows, err = s.queries.KeysByPreset(ctx, tx, presets)
	} else {
		var own []AccountKeysByPresetRow
		own, err = s.queries.AccountKeysByPreset(ctx, tx, AccountKeysByPresetParams{Presets: presets, AccountID: uuid.UUID(*account)})
		for _, r := range own {
			rows = append(rows, KeysByPresetRow(r))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("count the keys by routing preset: %w", err)
	}
	out := make(map[uuid.UUID]int64, len(rows))
	for _, r := range rows {
		out[r.PresetID] = r.Keys
	}
	return out, nil
}

// UpdateKey replaces id's settings in tx, records what it changed, and returns the key as changed. Turning a disabled
// key on again is a change of settings like any other. The old settings are read in the key's row's turn, in this
// transaction, so the audit entry names what this change replaced and not what a change made at the same moment did.
// The next call with the key meets the new settings on every node: Admit reads them from the database.
func (s *Service) UpdateKey(ctx context.Context, tx pg.Tx, actor audit.Actor, id KeyID, k KeySettings) (Key, error) {
	before, err := s.queries.LockKey(ctx, tx, uuid.UUID(id))
	if errors.Is(err, pg.ErrNoRows) {
		return Key{}, ErrKeyNotFound
	}
	if err != nil {
		return Key{}, fmt.Errorf("update an API key: read it: %w", err)
	}
	status := "active"
	if k.Disabled {
		status = "disabled"
	}
	if k.Preset, err = s.rebind(ctx, tx, before, k.Preset); err != nil {
		return Key{}, err
	}
	if err := s.queries.UpdateKey(ctx, tx, UpdateKeyParams{
		ID: uuid.UUID(id), Name: k.Name, Status: status, SpendLimit: k.SpendLimit, LimitReset: nullable(k.LimitReset),
		Models: nilIfEmpty(k.Models), IpAllow: nilIfEmpty(k.IPAllow), PresetID: k.Preset,
	}); err != nil {
		return Key{}, fmt.Errorf("update an API key: %w", err)
	}
	if err := recordKey(ctx, tx, actor, "api_key.update", id, before.SiteID, before.AccountID, keyChange(before, k)); err != nil {
		return Key{}, err
	}
	return s.GetKey(ctx, tx, id)
}

// rebind is the routing preset a changed key is bound to: the one it has, when none is chosen or the chosen one is it,
// and otherwise the chosen one, once Config.KeyPreset found that the key may use it.
func (s *Service) rebind(ctx context.Context, tx pg.Tx, before LockKeyRow, chosen *uuid.UUID) (*uuid.UUID, error) {
	if chosen == nil || sameOptional(chosen, before.PresetID) {
		return before.PresetID, nil
	}
	if before.Kind != CallKey || s.keyPreset == nil {
		return nil, ErrKeyPreset
	}
	site, ok := s.snapshot.Load().sites[SiteID(before.SiteID)]
	if !ok {
		return nil, fmt.Errorf("update an API key: site %s is not in the snapshot", SiteID(before.SiteID).TypeID())
	}
	preset, err := s.keyPreset(ctx, tx, before.SiteID, before.AccountID, before.WorkspaceID, site.Currency, chosen)
	if err != nil {
		return nil, fmt.Errorf("update an API key: its routing preset: %w", err)
	}
	return &preset, nil
}

// keyChange is what a change to a key replaced, as the audit entry records it: the names of the settings that changed,
// and the values of those the log may hold, as they were and as they now are. A setting that did not change is in
// neither. The two lists are named and no more: a model list is up to 256 names of 200 characters, more than an entry's
// 8 KiB, and an address is personal data the log does not hold (platform/README.md「审计日志」).
func keyChange(before LockKeyRow, now KeySettings) map[string]any {
	changed := []string{}
	was, after := map[string]any{}, map[string]any{}
	disclose := func(name string, old, next any) {
		changed = append(changed, name)
		was[name], after[name] = old, next
	}
	if before.Name != now.Name {
		disclose("name", before.Name, now.Name)
	}
	if disabled := before.Status == "disabled"; disabled != now.Disabled {
		disclose("disabled", disabled, now.Disabled)
	}
	if !sameOptional(before.SpendLimit, now.SpendLimit) {
		disclose("spend_limit", before.SpendLimit, now.SpendLimit)
	}
	if reset := nullable(now.LimitReset); !sameOptional(before.LimitReset, reset) {
		disclose("limit_reset", before.LimitReset, reset)
	}
	if !slices.Equal(before.Models, nilIfEmpty(now.Models)) {
		changed = append(changed, "models")
	}
	if !slices.Equal(before.IpAllow, nilIfEmpty(now.IPAllow)) {
		changed = append(changed, "ip_allow")
	}
	if !sameOptional(before.PresetID, now.Preset) {
		disclose("preset_id", before.PresetID, now.Preset)
	}
	return map[string]any{"changed": changed, "before": was, "after": after}
}

// sameOptional is whether two optional values are both unset or both the same.
func sameOptional[T comparable](a, b *T) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// keyOf is a key row as a Key.
func keyOf[R ListKeysRow | GetKeyRow | ValidManagementKeyRow | ListManagedKeysRow](row R) Key {
	r := ListKeysRow(row)
	k := Key{
		Kind: r.Kind, CreatedBy: UserID(r.CreatedBy), ID: KeyID(r.ID), Workspace: WorkspaceID(r.WorkspaceID), Prefix: r.Prefix, Last4: r.Last4,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt, ExpiresAt: r.ExpiresAt,
		Name: r.Name, Disabled: r.Status == "disabled", SpendLimit: r.SpendLimit, Models: r.Models, IPAllow: r.IpAllow,
		Preset: r.PresetID,
	}
	if r.LimitReset != nil {
		k.LimitReset = *r.LimitReset
	}
	return k
}

// DeleteKey deletes the key for good in tx and records it.
func (s *Service) DeleteKey(ctx context.Context, tx pg.Tx, actor audit.Actor, key KeyID) error {
	rows, err := s.queries.DeleteKey(ctx, tx, uuid.UUID(key))
	if err != nil {
		return fmt.Errorf("delete an API key: %w", err)
	}
	if len(rows) == 0 {
		return ErrKeyNotFound
	}
	return recordKey(ctx, tx, actor, "api_key.delete", key, rows[0].SiteID, rows[0].AccountID, nil)
}

// recordKey records a change to key, which belongs to account of site, with what it replaced.
func recordKey(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, key KeyID, site, account uuid.UUID, detail map[string]any) error {
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: action, Detail: detail, Target: audit.Target{
		Module: "accounts", Kind: "api_key", ID: key.TypeID(), Site: site, Account: account,
	}})
}

// KeyInfo is what a key shows of itself.
type KeyInfo struct {
	Name string
	// Label is the key with its middle left out, such as sk-ax-v1-...Ab12: enough to tell keys apart, not to use one.
	Label     string
	ExpiresAt *time.Time
}

// GetKeyInfo reads a key of the transaction's scope; another account's key, a deleted one and an unknown one are all
// ErrKeyNotFound.
func (s *Service) GetKeyInfo(ctx context.Context, tx pg.Tx, key KeyID) (KeyInfo, error) {
	row, err := s.queries.GetKeyInfo(ctx, tx, uuid.UUID(key))
	if errors.Is(err, pg.ErrNoRows) {
		return KeyInfo{}, ErrKeyNotFound
	}
	if err != nil {
		return KeyInfo{}, fmt.Errorf("get an API key: %w", err)
	}
	return KeyInfo{Name: row.Name, Label: row.Prefix + "..." + row.Last4, ExpiresAt: row.ExpiresAt}, nil
}

// AccountWorkspaces are the IDs of all the workspaces of the account, deleted ones included, which billing sums an
// account's spend over. The transaction's scope must be that account's; another account's workspaces are not visible.
func (s *Service) AccountWorkspaces(ctx context.Context, tx pg.Tx, account AccountID) ([]uuid.UUID, error) {
	ids, err := s.queries.ListAccountWorkspaces(ctx, tx, uuid.UUID(account))
	if err != nil {
		return nil, fmt.Errorf("list the workspaces of an account: %w", err)
	}
	return ids, nil
}
