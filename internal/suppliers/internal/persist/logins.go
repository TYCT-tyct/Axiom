package persist

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

type loginStore struct{ s *Session }

// loginLocation is where a supplier's login is sealed to: the row it is stored in, so that it opens nowhere else.
func loginLocation(supplier uuid.UUID) secret.Location {
	return secret.Location{Table: "suppliers.supplier_logins", RowID: supplier.String(), Purpose: "login"}
}

// readLogin reads and opens a supplier's login. It is read-only and checks no lease: the resident usage collector runs
// outside the sync lease and never writes the login.
func readLogin(ctx context.Context, db *pg.DB, scope pg.Scope, keys *secret.Keyring, supplier uuid.UUID) (raw []byte, version int64, err error) {
	err = db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		row, err := store.New().GetSupplierLogin(ctx, tx, supplier)
		if err != nil {
			return err
		}
		opened, err := keys.Open(secret.Sealed{
			Ciphertext: row.Ciphertext, Nonce: row.Nonce, WrappedDEK: row.WrappedDek,
			KeyVersion: uint32(row.KeyVersion), //nolint:gosec // the column holds 1 to 2³²-1
		}, loginLocation(supplier))
		if err != nil {
			return fmt.Errorf("the login does not open: %w", err)
		}
		raw, version = []byte(opened.Reveal()), row.Version
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("persist: load the login of supplier %s: %w", supplier, err)
	}
	return raw, version, nil
}

// ReadLogin reads a supplier's sealed login for a reader outside the sync lease. The version lets the resident
// collector tell a replaced login from the one whose management key was rejected.
func ReadLogin(ctx context.Context, db *pg.DB, scope pg.Scope, keys *secret.Keyring, supplier uuid.UUID) ([]byte, int64, error) {
	return readLogin(ctx, db, scope, keys, supplier)
}

// Load implements adapter.LoginStore.
func (l loginStore) Load(ctx context.Context) (raw []byte, version int64, err error) {
	cfg := l.s.node.cfg
	return readLogin(ctx, cfg.DB, cfg.Scope, cfg.Keys, l.s.Supplier.ID)
}

// Swap implements adapter.LoginStore: it seals raw and stores it over the version read, if the lease is still the
// session's and the supplier's credentials are still the generation the lease was taken in, which it holds until the
// login is written. Anything else is adapter.ErrStaleLogin, and the stored login is left as it is.
func (l loginStore) Swap(ctx context.Context, version int64, raw []byte) error {
	cfg := l.s.node.cfg
	sealed := cfg.Keys.Seal(secret.New(string(raw)), loginLocation(l.s.Supplier.ID))
	err := cfg.DB.Tx(ctx, cfg.Scope, func(tx pg.Tx) error {
		if _, err := l.s.hold(ctx, tx); err != nil {
			return err
		}
		changed, err := store.New().SwapSupplierLogin(ctx, tx, store.SwapSupplierLoginParams{
			SupplierID: l.s.Supplier.ID, Version: version,
			Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: int64(sealed.KeyVersion),
		})
		if err != nil {
			return err
		}
		if changed == 0 {
			return adapter.ErrStaleLogin
		}
		return nil
	})
	if errors.Is(err, syncer.ErrFenced) {
		err = adapter.ErrStaleLogin
	}
	if err != nil {
		return fmt.Errorf("persist: store the login of supplier %s: %w", l.s.Supplier.ID, err)
	}
	return nil
}
