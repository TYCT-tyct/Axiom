package accounts

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// Caller is a key recognised on a site: who it belongs to, nothing about whether it may be used, which Admit reads
// from the database on every call. Only AuthenticateKey makes one; the zero value is refused by Admit.
type Caller struct {
	key       KeyID
	account   AccountID
	workspace WorkspaceID
	site      SiteID
}

// KeyID is the key's ID.
func (c Caller) KeyID() KeyID { return c.key }

// AccountID is the account the key belongs to.
func (c Caller) AccountID() AccountID { return c.account }

// AuthenticateKey recognises raw as a key of site. A malformed key is refused before anything is hashed or read; a
// known one is answered from memory with no database query; concurrent misses of one key share a single lookup. It
// says nothing about whether the key may be used: Admit decides that.
func (s *Service) AuthenticateKey(ctx context.Context, site SiteID, raw string) (Caller, error) {
	if !wellFormedKey(raw) {
		return Caller{}, ErrUnknownKey
	}
	at := keyHash{uuid.UUID(site), s.hashes.Sum(s.versions[0], []byte(raw))}
	now := s.now()
	if c, ok := s.known.get(at, now); ok {
		return c, nil
	}
	if _, ok := s.unknown.get(at, now); ok {
		return Caller{}, ErrUnknownKey
	}
	found, err, _ := s.lookups.Do(string(at.site[:])+string(at.hash[:]), func() (any, error) {
		return s.lookUp(ctx, at, raw)
	})
	if err != nil {
		return Caller{}, err
	}
	c, _ := found.(Caller)
	if c == (Caller{}) {
		return Caller{}, ErrUnknownKey
	}
	return c, nil
}

// lookUp finds the key in the database, with no scope, under every hash version, and remembers the answer. The key
// is a secret, so only its hashes leave the process.
func (s *Service) lookUp(ctx context.Context, at keyHash, raw string) (Caller, error) {
	hashes := [][]byte{at.hash[:]}
	for _, v := range s.versions[1:] {
		h := s.hashes.Sum(v, []byte(raw))
		hashes = append(hashes, h[:])
	}
	var rows []KeyTenantRow
	if err := s.db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		var err error
		rows, err = s.queries.KeyTenant(ctx, tx, KeyTenantParams{SiteID: at.site, Hashes: hashes})
		return err
	}); err != nil {
		return Caller{}, fmt.Errorf("look up an API key: %w", err)
	}
	if len(rows) == 0 {
		s.unknown.put(at, struct{}{}, s.now().Add(unknownFor))
		return Caller{}, nil
	}
	c := Caller{KeyID(rows[0].KeyID), AccountID(rows[0].AccountID), WorkspaceID(rows[0].WorkspaceID), SiteID(at.site)}
	s.known.put(at, c, time.Time{})
	return c, nil
}
