package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ErrKeyNotFound is a key the transaction cannot see, already deleted, or, for DisableKey, already disabled.
var ErrKeyNotFound = errors.New("accounts: no such API key")

// KeyRequest is a new call key in a workspace, made by a user of its site.
type KeyRequest struct {
	Site      SiteID
	Account   AccountID
	Workspace WorkspaceID
	CreatedBy UserID
	Name      string
	ExpiresAt *time.Time // only set when the key is made
}

// CreatedKey is a new key with its text: the one time the text is anywhere but with the client, since only its hash
// is stored.
type CreatedKey struct {
	ID   KeyID
	Text secret.Secret
}

// CreateKey makes a call key in tx, with the site's prefix and, as its daily spend limit, the site's limit for new
// keys, and records it in the audit log in the same transaction.
func (s *Service) CreateKey(ctx context.Context, tx pg.Tx, actor audit.Actor, r KeyRequest) (CreatedKey, error) {
	site, ok := s.snapshot.Load().sites[r.Site]
	if !ok {
		return CreatedKey{}, fmt.Errorf("create an API key: site %s is not in the snapshot", r.Site.TypeID())
	}
	text := newKeyText(site.KeyPrefix)
	id := KeyID(uuid.NewV7())
	version := s.versions[0]
	hash := s.hashes.Sum(version, []byte(text))
	if err := s.queries.InsertKey(ctx, tx, InsertKeyParams{
		ID: uuid.UUID(id), SiteID: uuid.UUID(r.Site), AccountID: uuid.UUID(r.Account),
		WorkspaceID: uuid.UUID(r.Workspace), Name: r.Name, Prefix: site.KeyPrefix, Last4: text[len(text)-4:],
		SecretHash: hash[:], SecretVersion: int64(version), CreatedBy: uuid.UUID(r.CreatedBy), ExpiresAt: r.ExpiresAt,
		SpendLimit: &site.NewKeyDailyLimit,
	}); err != nil {
		return CreatedKey{}, fmt.Errorf("create an API key: %w", err)
	}
	if err := recordKey(ctx, tx, actor, "api_key.create", id); err != nil {
		return CreatedKey{}, err
	}
	return CreatedKey{id, secret.New(text)}, nil
}

// DisableKey stops the key in tx and records it; the next call with it is refused on every node.
func (s *Service) DisableKey(ctx context.Context, tx pg.Tx, actor audit.Actor, key KeyID) error {
	return changeKey(ctx, tx, actor, "api_key.disable", key, s.queries.DisableKey)
}

// DeleteKey deletes the key for good in tx and records it.
func (s *Service) DeleteKey(ctx context.Context, tx pg.Tx, actor audit.Actor, key KeyID) error {
	return changeKey(ctx, tx, actor, "api_key.delete", key, s.queries.DeleteKey)
}

func changeKey(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, key KeyID,
	update func(context.Context, DBTX, uuid.UUID) (int64, error),
) error {
	changed, err := update(ctx, tx, uuid.UUID(key))
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if changed == 0 {
		return ErrKeyNotFound
	}
	return recordKey(ctx, tx, actor, action, key)
}

func recordKey(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, key KeyID) error {
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action, Target: audit.Target{Module: "accounts", Kind: "api_key", ID: key.TypeID()},
	})
}
