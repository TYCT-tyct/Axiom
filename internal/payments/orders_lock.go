package payments

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// lockOrder locks one order's row for a change of its state.
func lockForChange(ctx context.Context, tx pg.Tx, id OrderID) (LockOrderRow, error) {
	rows, err := queries.LockOrder(ctx, tx, uuid.UUID(id))
	if err != nil {
		return LockOrderRow{}, fmt.Errorf("payments: lock order %s: %w", id.TypeID(), err)
	}
	if len(rows) == 0 {
		return LockOrderRow{}, ErrNotFound
	}
	return rows[0], nil
}

// boundOrderTx bounds the waits of an order's transaction, so a lock another request holds answers instead of holding
// the connection (README.md「事务与任务」).
func boundOrderTx(ctx context.Context, tx pg.Tx) error {
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '2s'; SET LOCAL statement_timeout = '5s'"); err != nil {
		return fmt.Errorf("payments: bound the order's waits: %w", err)
	}
	return nil
}

// accountKey is the advisory lock key of an account's orders: its limits are read and its order written under it, so
// two orders at once cannot both take the last place. The leading namespace byte separates these keys from the
// exchange rate's pairs.
func accountKey(account uuid.UUID) int64 {
	return advisoryKey(0x4f, account)
}

// merchantKey keeps merchant identity locks separate from order (0x4f) and exchange-rate (0x46) locks.
func merchantKey(id MerchantID) int64 { return advisoryKey(0x4d, uuid.UUID(id)) }

func advisoryKey(namespace byte, id uuid.UUID) int64 {
	hash := sha256.Sum256(id[:])
	return int64(binary.BigEndian.Uint64(hash[:8])>>8) | int64(namespace)<<56
}

// channelOf opens a merchant account's channel in the platform scope, which alone reads merchant_configs.
func (p *Payments) channelOf(ctx context.Context, id MerchantID) (Channel, error) {
	var ch Channel
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) {
		ch, err = p.channel(ctx, tx, id)
		return err
	})
	return ch, err
}
