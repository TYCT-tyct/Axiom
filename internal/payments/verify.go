package payments

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// ErrVerifyLimited is an order queried by its user less than a minute ago.
var ErrVerifyLimited = errors.New("payments: wait a minute before checking this order again")

// Only this core deadline, rather than a caller abort, is evidence of a slow gateway.
var errQueryDeadline = errors.New("payments: gateway query deadline")

// Verify queries an order visible in s once. The platform scope is used only after the account's read authorized it;
// the common receipt transaction supplies the same money and concurrency guarantees as Notify.
func (p *Payments) Verify(ctx context.Context, s pg.Scope, id OrderID) (Order, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, closeTimeout, errQueryDeadline)
	defer cancel()
	var o Order
	var allowed bool
	err := p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) {
		o, err = loadOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		allowed, err = queries.QueryAllowed(ctx, tx, uuid.UUID(id))
		if err != nil || !allowed {
			return err
		}
		return err
	})
	if err != nil || !allowed {
		return o, err
	}
	token, err := p.acquireQuery(ctx, o.MerchantID, false)
	if err != nil {
		return o, err
	}
	allowed, err = p.takeVerification(ctx, s, id)
	if err != nil || !allowed {
		return o, errors.Join(err, p.finishQuery(ctx, o.MerchantID, token, queryUnused))
	}
	if err = p.queryWithToken(ctx, o, token); err != nil {
		return o, err
	}
	return p.GetOrder(ctx, s, id)
}

// Recheck eligibility and claim the user's allowance atomically, only after admission to the merchant.
func (p *Payments) takeVerification(ctx context.Context, s pg.Scope, id OrderID) (allowed bool, err error) {
	err = p.db.Tx(ctx, s, func(tx pg.Tx) (err error) {
		allowed, err = queries.QueryAllowed(ctx, tx, uuid.UUID(id))
		if err != nil || !allowed {
			return err
		}
		n, err := queries.TakeVerify(ctx, tx, uuid.UUID(id))
		if err == nil && n == 0 {
			return &Wait{ErrVerifyLimited, time.Minute}
		}
		return err
	})
	return allowed, err
}

// A gateway may publish its paid status before the settlement fields. Retry once within the same deadline.
func queryPayment(ctx context.Context, ch Channel, order, trade string) (Payment, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, closeTimeout, errQueryDeadline)
	defer cancel()
	paid, err := ch.Query(ctx, order, trade)
	if err == nil && incompletePayment(paid) {
		return ch.Query(ctx, order, trade)
	}
	return paid, err
}

func (p *Payments) queryOrder(ctx context.Context, o Order, background bool) error {
	ctx, cancel := context.WithTimeoutCause(ctx, closeTimeout, errQueryDeadline)
	defer cancel()
	token, err := p.acquireQuery(ctx, o.MerchantID, background)
	if err != nil {
		return err
	}
	return p.queryWithToken(ctx, o, token)
}

func (p *Payments) queryWithToken(ctx context.Context, o Order, token uuid.UUID) error {
	ch, err := p.channelOf(ctx, o.MerchantID)
	var paid Payment
	if err != nil {
		return errors.Join(err, p.finishQuery(ctx, o.MerchantID, token, queryUnused))
	}
	if !ch.Caps().Has(CapQuery) {
		return p.finishQuery(ctx, o.MerchantID, token, queryUnused)
	}
	paid, err = p.completeQuery(ctx, ch, o.MerchantID, token, o.ID.TypeID(), o.TradeNo)
	if err != nil {
		return err
	}
	err = p.receive(ctx, confirmed{merchant: o.MerchantID, order: o.ID.TypeID(), payment: paid, source: "query"})
	if incompletePayment(paid) {
		return errors.Join(ErrUnconfirmed, err)
	}
	return err
}

func (p *Payments) queryPending(ctx context.Context) error {
	var due []QueryOrdersRow
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) {
		due, err = queries.QueryOrders(ctx, tx, expireBatch)
		return err
	})
	if err != nil {
		return fmt.Errorf("payments: read orders to query: %w", err)
	}
	groups := make(map[MerchantID][]OrderID)
	for _, r := range due {
		groups[MerchantID(r.MerchantID)] = append(groups[MerchantID(r.MerchantID)], OrderID(r.ID))
	}
	var workers sync.WaitGroup
	for _, ids := range groups {
		for worker := range min(2, len(ids)) {
			workers.Go(func() {
				for i := worker; i < len(ids) && ctx.Err() == nil; i += 2 {
					p.pollOrder(ctx, ids[i])
				}
			})
		}
	}
	workers.Wait()
	return ctx.Err()
}

// pollOrder logs its own failure: one order's query must not stop the round.
func (p *Payments) pollOrder(ctx context.Context, id OrderID) {
	if err := p.queryScheduled(ctx, id); err != nil {
		p.logger.Warn("payments: order query failed", "order", id.TypeID(), "error", err)
	}
}

func (p *Payments) queryScheduled(ctx context.Context, id OrderID) error {
	ctx, cancel := context.WithTimeoutCause(ctx, closeTimeout, errQueryDeadline)
	defer cancel()
	var o Order
	if err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) { o, err = loadOrder(ctx, tx, id); return err }); err != nil {
		return err
	}
	token, err := p.acquireQuery(ctx, o.MerchantID, false)
	if err != nil {
		return err
	}
	var claimed int64
	err = p.db.Tx(ctx, p.platform, func(tx pg.Tx) (err error) {
		claimed, err = queries.ScheduleQuery(ctx, tx, uuid.UUID(id))
		if err != nil || claimed == 0 {
			return err
		}
		o, err = loadOrder(ctx, tx, id)
		return err
	})
	if err != nil || claimed == 0 {
		return errors.Join(err, p.finishQuery(ctx, o.MerchantID, token, queryUnused))
	}
	return p.queryWithToken(ctx, o, token)
}

func (p *Payments) completeQuery(ctx context.Context, ch Channel, merchant MerchantID, token uuid.UUID, order, trade string) (Payment, error) {
	paid, err := queryPayment(ctx, ch, order, trade)
	finishErr := p.finishQuery(ctx, merchant, token, queryHealthOutcome(ctx, err, incompletePayment(paid)))
	if err != nil {
		return paid, errors.Join(fmt.Errorf("%w: query the order: %w", ErrUnconfirmed, err), finishErr)
	}
	return paid, finishErr
}
