package payments

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// MerchantChange is a merchant account checked and probed outside a transaction, ready to be written in the caller's
// transaction. Only PrepareMerchant makes one: it carries the settings the plugin accepted and, when the caller gave
// none, the sealed secrets read for reuse, so a change and its idempotency claim commit together.
type MerchantChange struct {
	in       MerchantInput
	settings []byte
	created  bool
}

// PrepareMerchant checks a candidate, reuses the saved secrets when none are given and probes the channel outside every
// database transaction. The write must still recheck the revision and the identity fields under the merchant's lock.
func (p *Payments) PrepareMerchant(ctx context.Context, scope pg.Scope, in MerchantInput) (MerchantChange, error) {
	if err := in.check(); err != nil {
		return MerchantChange{}, err
	}
	plugin, ok := p.plugins[in.Kind]
	if !ok {
		return MerchantChange{}, &FieldError{"/kind", "is not a channel this program has"}
	}
	settings, err := merchantSettings(plugin, in.Settings)
	if err != nil {
		return MerchantChange{}, &FieldError{"/settings", err.Error()}
	}
	created := in.ID == MerchantID{}
	if created {
		in.ID = MerchantID(uuid.NewV7())
		if in.Secrets == nil {
			return MerchantChange{}, &FieldError{"/secrets", "are required"}
		}
	}
	if err := p.probeMerchant(ctx, scope, plugin, in, settings, created); err != nil {
		return MerchantChange{}, err
	}
	return MerchantChange{in: in, settings: settings, created: created}, nil
}

// WriteMerchant saves a prepared change in tx, which may be the transaction that claims its Idempotency-Key.
func (p *Payments) WriteMerchant(ctx context.Context, tx pg.Tx, actor audit.Actor, change MerchantChange) (Merchant, error) {
	return p.saveMerchant(ctx, tx, actor, change.in, change.settings, change.created)
}

// ListMerchants reads a site's merchant accounts and their payment methods without their secrets.
func ListMerchants(ctx context.Context, tx pg.Tx, site uuid.UUID) ([]Merchant, error) {
	rows, err := queries.ListMerchants(ctx, tx, site)
	if err != nil {
		return nil, fmt.Errorf("payments: list merchants: %w", err)
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	configs, err := queries.MerchantConfigs(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("payments: read merchant configs: %w", err)
	}
	settings := make(map[uuid.UUID]string, len(configs))
	for _, config := range configs {
		settings[config.ID] = config.Settings
	}
	methods, err := queries.ListMethods(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("payments: read merchant methods: %w", err)
	}
	byID := make(map[uuid.UUID][]PaymentsMerchantMethod, len(rows))
	for _, method := range methods {
		byID[method.MerchantID] = append(byID[method.MerchantID], method)
	}
	merchants := make([]Merchant, len(rows))
	for i, row := range rows {
		merchants[i] = Merchant{PaymentsMerchant: row, Settings: settings[row.ID], Methods: byID[row.ID]}
	}
	return merchants, nil
}

// DisableMerchant stops a merchant account from being chosen for new orders without touching its configuration: the
// secrets stay sealed and callbacks, queries and refunds of its orders keep working (docs/domains/payments.md「后台配置」).
// Disabling one that is disabled already changes nothing and writes no audit entry.
func DisableMerchant(ctx context.Context, tx pg.Tx, actor audit.Actor, id MerchantID, reason string) (Merchant, error) {
	rows, err := queries.LockMerchant(ctx, tx, uuid.UUID(id))
	if err != nil {
		return Merchant{}, fmt.Errorf("payments: lock merchant: %w", err)
	}
	if len(rows) == 0 {
		return Merchant{}, ErrNotFound
	}
	if !rows[0].Enabled {
		return ReadMerchant(ctx, tx, id)
	}
	if _, err := queries.DisableMerchant(ctx, tx, uuid.UUID(id)); err != nil {
		return Merchant{}, fmt.Errorf("payments: disable merchant: %w", err)
	}
	err = audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "merchant.disable", Reason: reason,
		Target: audit.Target{Module: "payments", Kind: "merchant", ID: id.TypeID(), Site: rows[0].SiteID},
		Detail: map[string]any{"from": "enabled", "to": "disabled"},
	})
	if err != nil {
		return Merchant{}, err
	}
	return ReadMerchant(ctx, tx, id)
}
