package payments

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

func (p *Payments) probeMerchant(ctx context.Context, scope pg.Scope, plugin Plugin, in MerchantInput, settings []byte, created bool) error {
	// Keep the caller's nil secrets intact: the write must distinguish reuse from rotation when checking identity.
	candidate := in
	if !created {
		err := p.db.ReadTx(ctx, scope, func(tx pg.Tx) error {
			rows, err := queries.ReadMerchantConfig(ctx, tx, uuid.UUID(in.ID))
			if err != nil {
				return fmt.Errorf("payments: read merchant for save: %w", err)
			}
			if len(rows) == 0 {
				return ErrNotFound
			}
			_, err = p.bindForSave(ctx, tx, &candidate, settings, LockMerchantRow(rows[0]))
			return err
		})
		if err != nil {
			return err
		}
	}
	ch, err := p.validate(plugin, candidate, settings)
	if err != nil {
		return err
	}
	if !in.Enabled {
		return nil
	}
	if prober, ok := ch.(interface{ probe(context.Context) error }); ok {
		return prober.probe(ctx)
	}
	return nil
}
