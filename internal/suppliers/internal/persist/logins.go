package persist

import (
	"context"
	"errors"
	"fmt"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

type loginStore struct{ s *Session }

// at is where a supplier's login is sealed to: the row it is stored in, so that it opens nowhere else.
func (l loginStore) at() secret.Location {
	return secret.Location{Table: "suppliers.supplier_logins", RowID: l.s.Supplier.ID.String(), Purpose: "login"}
}

// Load implements adapter.LoginStore.
func (l loginStore) Load(ctx context.Context) (raw []byte, version int64, err error) {
	cfg := l.s.node.cfg
	err = cfg.DB.ReadTx(ctx, cfg.Scope, func(tx pg.Tx) error {
		row, err := store.New().GetSupplierLogin(ctx, tx, l.s.Supplier.ID)
		if err != nil {
			return err
		}
		opened, err := cfg.Keys.Open(secret.Sealed{
			Ciphertext: row.Ciphertext, Nonce: row.Nonce, WrappedDEK: row.WrappedDek,
			KeyVersion: uint32(row.KeyVersion), //nolint:gosec // the column holds 1 to 2³²-1
		}, l.at())
		if err != nil {
			return fmt.Errorf("the login does not open: %w", err)
		}
		raw, version = []byte(opened.Reveal()), row.Version
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("persist: load the login of supplier %s: %w", l.s.Supplier.ID, err)
	}
	return raw, version, nil
}

// Swap implements adapter.LoginStore: it seals raw and stores it over the version read, if the lease is still the
// session's and the supplier's credentials are still the generation the lease was taken in, which it holds until the
// login is written. Anything else is adapter.ErrStaleLogin, and the stored login is left as it is.
func (l loginStore) Swap(ctx context.Context, version int64, raw []byte) error {
	cfg := l.s.node.cfg
	sealed := cfg.Keys.Seal(secret.New(string(raw)), l.at())
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
