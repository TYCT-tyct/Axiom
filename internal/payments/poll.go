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

// payments.poll is the module's periodic job (README.md「事务与任务」). This PR carries its first step, the expiry of
// orders past their deadline; querying orders that lost their callback, and the late payment that follows a query, come
// with the callback (README.md「实现拆分」E1-2c), which is what a query's answer would be credited by.

// pollEvery is how often a run is inserted; the queue inserts it on the node that leads.
const pollEvery = time.Minute

// expireBatch is how many orders one run takes: an order that fails does not hold the next run's, and the run's budget
// bounds a gateway that answers slowly.
const (
	expireBatch  = 100
	pollDeadline = 30 * time.Second
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
	var due []ExpiringOrdersRow
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) (err error) {
		due, err = queries.ExpiringOrders(ctx, tx, expireBatch)
		return err
	})
	if err != nil {
		return fmt.Errorf("payments: read the orders to expire: %w", err)
	}
	for _, r := range due {
		if err := p.expire(ctx, r); err != nil {
			p.logger.Error("payments: an order did not expire", "order", OrderID(r.ID).TypeID(), "error", err)
		}
	}
	return nil
}

// expire moves one order past its deadline to expired, which gives its reservation of the merchant's daily limit back,
// and then asks the channel to close it. The channel is asked afterwards and its refusal is only logged: whether an
// order is payable here is our decision, and one our database no longer offers cannot hold the account's place for
// ever because a gateway is misconfigured.
func (p *Payments) expire(ctx context.Context, r ExpiringOrdersRow) error {
	id := OrderID(r.ID)
	moved := false
	err := p.db.Tx(ctx, p.platform, func(tx pg.Tx) error {
		locked, err := lockForChange(ctx, tx, id)
		if err != nil {
			return err
		}
		if State(locked.State) != Pending { // paid or cancelled while the run read it
			return nil
		}
		moved = true
		return move(ctx, tx, locked, Expired, event{source: sourceJob, kind: kindExpire})
	})
	if err != nil || !moved {
		return err
	}
	p.closeOrder(ctx, Order{ID: id, MerchantID: MerchantID(r.MerchantID), TradeNo: r.TradeNo})
	return nil
}
