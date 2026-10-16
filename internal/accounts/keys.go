package accounts

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ErrKeyNotFound is a key the transaction cannot see, already deleted, or, for DisableKey, already disabled.
var ErrKeyNotFound = errors.New("accounts: no such API key")

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
// for new keys, and records it in the audit log in the same transaction.
func (s *Service) CreateKey(ctx context.Context, tx pg.Tx, actor audit.Actor, r KeyRequest) (CreatedKey, error) {
	site, ok := s.snapshot.Load().sites[r.Site]
	if !ok {
		return CreatedKey{}, fmt.Errorf("create an API key: site %s is not in the snapshot", r.Site.TypeID())
	}
	if r.Workspace == (WorkspaceID{}) {
		found, err := s.queries.DefaultWorkspace(ctx, tx, uuid.UUID(r.Account))
		if err != nil || len(found) == 0 {
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
		return CreatedKey{}, fmt.Errorf("create an API key: %w", err)
	}
	if err := recordKey(ctx, tx, actor, "api_key.create", id, uuid.UUID(r.Site), uuid.UUID(r.Account)); err != nil {
		return CreatedKey{}, err
	}
	return CreatedKey{id, secret.New(text)}, nil
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

// WorkspaceIDs are the account's workspaces, deleted ones included: what they were charged stays counted.
func (s *Service) WorkspaceIDs(ctx context.Context, tx pg.Tx, account AccountID) ([]WorkspaceID, error) {
	ids, err := s.queries.AccountWorkspaces(ctx, tx, uuid.UUID(account))
	if err != nil {
		return nil, fmt.Errorf("list the workspaces: %w", err)
	}
	workspaces := make([]WorkspaceID, len(ids))
	for i, id := range ids {
		workspaces[i] = WorkspaceID(id)
	}
	return workspaces, nil
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

// UpdateKey replaces id's settings in tx, records it, and returns the key as changed. Turning a disabled key on again
// is a change of settings like any other. The next call with the key meets the new settings on every node: Admit reads
// them from the database.
func (s *Service) UpdateKey(ctx context.Context, tx pg.Tx, actor audit.Actor, id KeyID, k KeySettings) (Key, error) {
	status := "active"
	if k.Disabled {
		status = "disabled"
	}
	rows, err := s.queries.UpdateKey(ctx, tx, UpdateKeyParams{
		ID: uuid.UUID(id), Name: k.Name, Status: status, SpendLimit: k.SpendLimit, LimitReset: nullable(k.LimitReset),
		Models: nilIfEmpty(k.Models), IpAllow: nilIfEmpty(k.IPAllow),
	})
	if err != nil {
		return Key{}, fmt.Errorf("update an API key: %w", err)
	}
	if len(rows) == 0 {
		return Key{}, ErrKeyNotFound
	}
	if err := recordKey(ctx, tx, actor, "api_key.update", id, rows[0].SiteID, rows[0].AccountID); err != nil {
		return Key{}, err
	}
	return s.GetKey(ctx, tx, id)
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

// DisableKey stops the key in tx and records it; the next call with it is refused on every node.
func (s *Service) DisableKey(ctx context.Context, tx pg.Tx, actor audit.Actor, key KeyID) error {
	rows, err := s.queries.DisableKey(ctx, tx, uuid.UUID(key))
	if err != nil {
		return fmt.Errorf("disable an API key: %w", err)
	}
	if len(rows) == 0 {
		return ErrKeyNotFound
	}
	return recordKey(ctx, tx, actor, "api_key.disable", key, rows[0].SiteID, rows[0].AccountID)
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
	return recordKey(ctx, tx, actor, "api_key.delete", key, rows[0].SiteID, rows[0].AccountID)
}

// recordKey records a change to key, which belongs to account of site.
func recordKey(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, key KeyID, site, account uuid.UUID) error {
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: action, Target: audit.Target{
		Module: "accounts", Kind: "api_key", ID: key.TypeID(), Site: site, Account: account,
	}})
}
