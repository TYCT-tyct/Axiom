package payments

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/pg"
)

// payments.poll queries before expiry, then releases expired orders before making any network close calls.

// pollEvery is how often a run is inserted; the queue inserts it on the node that leads.
const pollEvery = time.Minute

// expireBatch is how many orders one run takes: an order that fails does not hold the next run's, and the run's budget
// bounds a gateway that answers slowly.
const (
	expireBatch  = 100
	pollDeadline = 30 * time.Second
	closeTimeout = 5 * time.Second
)

// pollArgs is the job of one run.
type pollArgs struct{ singleRun }

// Kind implements river.JobArgs.
func (pollArgs) Kind() string { return "payments.poll" }

// singleRun makes a run unique among the jobs of its kind that are queued or running, so an interval that passes while
// a run is still going does not start a second one.
type singleRun struct{}

// InsertOpts implements river.JobArgsWithInsertOpts.
func (singleRun) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueMoney, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}}}
}

// PollConfig is what the job needs: the module, the queue it runs on, and where its log goes.
type PollConfig struct {
	Payments *Payments
	Queue    *pg.Queue
	Logger   *slog.Logger
}

// RegisterPoll adds payments.poll to the queue before it starts: a run every minute, on whichever node leads.
func RegisterPoll(cfg PollConfig) error {
	// A run that fails is not a failed job: it counted and logged what it could not do and the next run takes it up, as
	// billing's expiry does. A retrying job would hold the next run back and with it every other order.
	work := func(ctx context.Context, _ *river.Job[pollArgs]) error {
		if err := cfg.Payments.poll(ctx); err != nil {
			cfg.Logger.Error("payments: the poll stopped", "err", err)
		}
		return nil
	}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(work)); err != nil {
		return fmt.Errorf("payments: %w", err)
	}
	cfg.Queue.Every(pollEvery, func() river.JobArgs { return pollArgs{} })
	return nil
}

// poll takes the orders past their deadline, oldest first, and expires each: one that fails is logged and the rest go
// on, so a gateway that stopped answering cannot hold every other order.
func (p *Payments) poll(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pollDeadline)
	defer cancel()
	// Leave the expiry and close steps time even when every queried gateway stalls.
	queryCtx, stopQuery := context.WithTimeout(ctx, 10*time.Second)
	err := p.queryPending(queryCtx)
	stopQuery()
	if err != nil {
		p.logger.Warn("payments: poll queries stopped", "error", err)
	}
	var due []ExpiringOrdersRow
	err = p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) {
		due, err = queries.ExpiringOrders(ctx, tx, expireBatch)
		return err
	})
	if err != nil {
		return fmt.Errorf("payments: read the orders to expire: %w", err)
	}
	var closing []Order
	for _, r := range due {
		o, err := p.expire(ctx, r)
		if err != nil {
			p.logger.Error("payments: an order did not expire", "order", OrderID(r.ID).TypeID(), "error", err)
		} else if o != nil {
			closing = append(closing, *o)
		}
	}
	refundCtx, stopRefunds := context.WithTimeout(ctx, 10*time.Second)
	err = p.pollRefunds(refundCtx)
	stopRefunds()
	if err != nil {
		p.logger.Warn("payments: poll refunds stopped", "error", err)
	}
	p.closeExpired(ctx, closing)
	return nil
}

// closeExpired runs only after the whole batch has released its places and reservations. A stalled merchant gets
// one bounded attempt per run, so its other orders cannot exhaust the closing budget of unrelated merchants.
func (p *Payments) closeExpired(ctx context.Context, orders []Order) {
	timedOut := make(map[MerchantID]bool)
	for _, o := range orders {
		if ctx.Err() != nil {
			return
		}
		if !timedOut[o.MerchantID] {
			timedOut[o.MerchantID] = p.closeOrder(ctx, o)
		}
	}
}

// expire commits the local state and reservation independently of the gateway. A nil order means another caller
// already changed its state, so this run must not issue another close.
func (p *Payments) expire(ctx context.Context, r ExpiringOrdersRow) (*Order, error) {
	id := OrderID(r.ID)
	var expired *Order
	err := p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		locked, err := lockForChange(ctx, tx, id)
		if err != nil {
			return err
		}
		if State(locked.State) != Pending { // paid or cancelled while the run read it
			return nil
		}
		expired = &Order{ID: id, MerchantID: MerchantID(locked.MerchantID), TradeNo: locked.TradeNo}
		return move(ctx, tx, locked, Expired, event{source: sourceJob, kind: kindExpire})
	})
	if err != nil {
		return nil, err
	}
	return expired, nil
}
