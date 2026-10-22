package payments

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// ErrVerifyLimited is an order queried by its user less than a minute ago.
var ErrVerifyLimited = errors.New("payments: wait a minute before checking this order again")

// Verify queries an order visible in s once. The platform scope is used only after the account's read authorized it;
// the common receipt transaction supplies the same money and concurrency guarantees as Notify.
func (p *Payments) Verify(ctx context.Context, s pg.Scope, id OrderID) (Order, error) {
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	var o Order
	var allowed bool
	err := p.db.Tx(ctx, s, func(tx pg.Tx) (err error) {
		o, err = loadOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		allowed, err = queries.QueryAllowed(ctx, tx, uuid.UUID(id))
		if err != nil || !allowed {
			return err
		}
		n, err := queries.TakeVerify(ctx, tx, uuid.UUID(id))
		if err == nil && n == 0 {
			return ErrVerifyLimited
		}
		return err
	})
	if err != nil || !allowed {
		return o, err
	}
	if err = p.queryOrder(ctx, o); err != nil {
		return o, err
	}
	err = p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) { o, err = loadOrder(ctx, tx, id); return err })
	return o, err
}

// A gateway may publish its paid status before the settlement fields. Retry once within the same deadline.
func queryPayment(ctx context.Context, ch Channel, order, trade string) (Payment, error) {
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	paid, err := ch.Query(ctx, order, trade)
	if err == nil && incompletePayment(paid) {
		return ch.Query(ctx, order, trade)
	}
	return paid, err
}

func (p *Payments) queryOrder(ctx context.Context, o Order) error {
	ch, err := p.channelOf(ctx, o.MerchantID)
	if err != nil {
		return err
	}
	if !ch.Caps().Has(CapQuery) {
		return nil
	}
	paid, err := queryPayment(ctx, ch, o.ID.TypeID(), o.TradeNo)
	if err != nil {
		return fmt.Errorf("%w: query the order: %w", ErrUnconfirmed, err)
	}
	return p.receive(ctx, confirmed{merchant: o.MerchantID, order: o.ID.TypeID(), payment: paid, source: "query"})
}

func (p *Payments) queryPending(ctx context.Context) error {
	var due []uuid.UUID
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) {
		due, err = queries.QueryOrders(ctx, tx, expireBatch)
		return err
	})
	if err != nil {
		return fmt.Errorf("payments: read orders to query: %w", err)
	}
	for _, id := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.pollOrder(ctx, OrderID(id))
	}
	return nil
}

// pollOrder logs its own failure: one order's query must not stop the round.
func (p *Payments) pollOrder(ctx context.Context, id OrderID) {
	if err := p.queryScheduled(ctx, id); err != nil {
		p.logger.Warn("payments: order query failed", "order", id.TypeID(), "error", err)
	}
}

func (p *Payments) queryScheduled(ctx context.Context, id OrderID) error {
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	var o Order
	var claimed int64
	err := p.db.Tx(ctx, p.platform, func(tx pg.Tx) (err error) {
		claimed, err = queries.ScheduleQuery(ctx, tx, uuid.UUID(id))
		if err != nil || claimed == 0 {
			return err
		}
		o, err = loadOrder(ctx, tx, id)
		return err
	})
	if err != nil || claimed == 0 {
		return err
	}
	return p.queryOrder(ctx, o)
}
