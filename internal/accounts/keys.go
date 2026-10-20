package accounts

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ErrKeyNotFound is a key the transaction cannot see, or already deleted.
var ErrKeyNotFound = errors.New("accounts: no such API key")

// ErrTooManyKeys is an account that already holds as many call keys as its site allows. It is lifted by deleting a
// key, never taken away: the keys made are the account's, and a site that lowers its limit keeps them usable.
var ErrTooManyKeys = errors.New("accounts: too many API keys")

// TooManyKeys is ErrTooManyKeys with the limit of the site and the keys the account holds.
type TooManyKeys struct{ Limit, Count int }

func (e *TooManyKeys) Error() string {
	return fmt.Sprintf("%s: %d of %d", ErrTooManyKeys, e.Count, e.Limit)
}

// Is makes errors.Is(err, ErrTooManyKeys) true for it.
func (*TooManyKeys) Is(target error) bool { return target == ErrTooManyKeys }

// The reset windows of a spend limit.
const (
	ResetDaily   = "daily"
	ResetWeekly  = "weekly"
	ResetMonthly = "monthly"
)

// KeySettings are the settings of a key a user may change (README.md「API key」). What is left out is unrestricted: no
// spend limit, any model, any address. They satisfy the table's checks (a reset needs a limit; money is not negative),
// which the console's interface checks before it asks.
type KeySettings struct {
	Name     string
	Disabled bool
	// SpendLimit is in billionths of a credit, nil for none. LimitReset is a Reset constant, or empty for a limit that
	// never resets; it needs a SpendLimit. For a new key, a nil SpendLimit means the site's limit for new keys, reset
	// daily.
	SpendLimit *int64
	LimitReset string
	Models     []string // nil: any model
	IPAllow    []netip.Prefix
}

// Key is a call key as the console shows it. Its text is not here: only its hash is kept, and the last four characters
// to tell keys apart.
type Key struct {
	ID        KeyID
	Workspace WorkspaceID
	Prefix    string
	Last4     string
	// CreatedAt is when the ID was made: an ID is a UUIDv7, which carries its time in milliseconds.
	CreatedAt time.Time
	ExpiresAt *time.Time
	KeySettings
}

// KeyRequest is a new call key in a workspace, made by a user of its site. A zero Workspace is the account's default
// workspace.
type KeyRequest struct {
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

// CreateKey makes a call key in tx, with the site's prefix and, unless r says otherwise, the site's daily spend limit
// for new keys, and records it in the audit log in the same transaction. It refuses a key over one of the site's two
// limits on how many an account holds and how many it makes in an hour (admitKey).
func (s *Service) CreateKey(ctx context.Context, tx pg.Tx, actor audit.Actor, r KeyRequest) (CreatedKey, error) {
	site, ok := s.snapshot.Load().sites[r.Site]
	if !ok {
		return CreatedKey{}, fmt.Errorf("create an API key: site %s is not in the snapshot", r.Site.TypeID())
	}
	give, err := s.admitKey(ctx, tx, site, r.Account)
	if err != nil {
		return CreatedKey{}, err
	}
	if r.Workspace == (WorkspaceID{}) {
		found, err := s.queries.DefaultWorkspace(ctx, tx, uuid.UUID(r.Account))
		if err != nil || len(found) == 0 {
			give()
			return CreatedKey{}, fmt.Errorf("create an API key: the account's default workspace: %w", errors.Join(err, ErrKeyNotFound))
		}
		r.Workspace = WorkspaceID(found[0])
	}
	limit, reset := r.SpendLimit, r.LimitReset
	if limit == nil {
		limit, reset = &site.NewKeyDailyLimit, ResetDaily
	}
	text := newKeyText(site.KeyPrefix)
	id := KeyID(uuid.NewV7())
	version := s.versions[0]
	hash := s.hashes.Sum(version, []byte(text))
	if err := s.queries.InsertKey(ctx, tx, InsertKeyParams{
		ID: uuid.UUID(id), SiteID: uuid.UUID(r.Site), AccountID: uuid.UUID(r.Account),
		WorkspaceID: uuid.UUID(r.Workspace), Name: r.Name, Prefix: site.KeyPrefix, Last4: text[len(text)-4:],
		SecretHash: hash[:], SecretVersion: int64(version), CreatedBy: uuid.UUID(r.CreatedBy), ExpiresAt: r.ExpiresAt,
		SpendLimit: limit, LimitReset: nullable(reset), Models: nilIfEmpty(r.Models), IpAllow: nilIfEmpty(r.IPAllow),
	}); err != nil {
		give()
		return CreatedKey{}, fmt.Errorf("create an API key: %w", err)
	}
	if err := recordKey(ctx, tx, actor, "api_key.create", id, uuid.UUID(r.Site), uuid.UUID(r.Account), nil); err != nil {
		give()
		return CreatedKey{}, err
	}
	return CreatedKey{id, secret.New(text)}, nil
}

// admitKey is the site's limits on the call keys account holds, met in the transaction that makes one: the hour's
// creation turns (KeyCreationsPerHour), and the keys the account already holds (MaxKeysPerAccount). The count is read
// in the account's row's turn, so that two creates at once cannot both take the last place. It returns the turn, to be
// given back when the key is not made after all. The turns are counted by the node, as the attempts before sign-in are
// (README.md「登录前的资源」), so the nodes multiply the hourly limit by their number.
func (s *Service) admitKey(ctx context.Context, tx pg.Tx, site Site, account AccountID) (func(), error) {
	per := time.Hour / time.Duration(site.KeyCreationsPerHour)
	turns := s.guards.keys
	if wait := turns.take(uuid.UUID(account), site.KeyCreationsPerHour, per, s.now()); wait > 0 {
		return nil, &TooManyAttempts{RetryAfter: wait}
	}
	// The turn is given back on every way out that makes no key: here, and by the caller whose later steps fail.
	give := sync.OnceFunc(func() { turns.give(uuid.UUID(account), per, s.now()) })
	locked, err := s.queries.LockAccount(ctx, tx, uuid.UUID(account))
	if err != nil {
		give()
		return nil, fmt.Errorf("create an API key: lock the account: %w", err)
	}
	if len(locked) == 0 {
		give()
		return nil, fmt.Errorf("create an API key: %w", ErrNotFound)
	}
	count, err := s.queries.CountKeys(ctx, tx, uuid.UUID(account))
	if err != nil {
		give()
		return nil, fmt.Errorf("create an API key: count the account's keys: %w", err)
	}
	if int(count) >= site.MaxKeysPerAccount {
		give()
		return nil, &TooManyKeys{Limit: site.MaxKeysPerAccount, Count: int(count)}
	}
	return give, nil
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

// ListKeys is up to limit of account's call keys that are not deleted, disabled ones included, newest first, starting
// after the key `after` (the zero KeyID for the first page); more tells whether keys remain beyond them.
func (s *Service) ListKeys(ctx context.Context, tx pg.Tx, account AccountID, after KeyID, limit int) (keys []Key, more bool, err error) {
	before := uuid.Max()
	if after != (KeyID{}) {
		before = uuid.UUID(after)
	}
	rows, err := s.queries.ListKeys(ctx, tx, ListKeysParams{AccountID: uuid.UUID(account), Before: before, PageSize: int32(limit) + 1}) // #nosec G115 -- limit is the contract's 1 to 200
	if err != nil {
		return nil, false, fmt.Errorf("list API keys: %w", err)
	}
	if more = len(rows) > limit; more {
		rows = rows[:limit]
	}
	keys = make([]Key, len(rows))
	for i, r := range rows {
		keys[i] = keyOf(r)
	}
	return keys, more, nil
}

// GetKey is the call key in tx's scope with this ID, or ErrKeyNotFound.
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
	if err := s.queries.UpdateKey(ctx, tx, UpdateKeyParams{
		ID: uuid.UUID(id), Name: k.Name, Status: status, SpendLimit: k.SpendLimit, LimitReset: nullable(k.LimitReset),
		Models: nilIfEmpty(k.Models), IpAllow: nilIfEmpty(k.IPAllow),
	}); err != nil {
		return Key{}, fmt.Errorf("update an API key: %w", err)
	}
	if err := recordKey(ctx, tx, actor, "api_key.update", id, before.SiteID, before.AccountID, keyChange(before, k)); err != nil {
		return Key{}, err
	}
	return s.GetKey(ctx, tx, id)
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
	return map[string]any{"changed": changed, "before": was, "after": after}
}

// sameOptional is whether two optional values are both unset or both the same.
func sameOptional[T comparable](a, b *T) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// keyOf is a key row as a Key.
func keyOf[R ListKeysRow | GetKeyRow](row R) Key {
	r := ListKeysRow(row)
	k := Key{
		ID: KeyID(r.ID), Workspace: WorkspaceID(r.WorkspaceID), Prefix: r.Prefix, Last4: r.Last4,
		CreatedAt: createdAt(r.ID), ExpiresAt: r.ExpiresAt,
		Name: r.Name, Disabled: r.Status == "disabled", SpendLimit: r.SpendLimit, Models: r.Models, IPAllow: r.IpAllow,
	}
	if r.LimitReset != nil {
		k.LimitReset = *r.LimitReset
	}
	return k
}

// createdAt is the time a UUIDv7 carries: its first 48 bits, in milliseconds since the Unix epoch.
func createdAt(id uuid.UUID) time.Time {
	var ms [8]byte
	copy(ms[2:], id[:6])
	return time.UnixMilli(int64(binary.BigEndian.Uint64(ms[:]))).UTC() // #nosec G115 -- 48 bits fit
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
