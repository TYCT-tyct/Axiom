package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/platform/pg"
)

// The pace of the expiry (docs/domains/billing.md「额度批次」「到期」). A run is cut short by its budget, below the
// queue's job timeout, and the next goes on after the last account it reached; an account that takes longer than its
// budget fails and the run goes on to the next, so one slow account cannot hold the rest back.
const (
	expireEvery         = time.Minute
	expireRunBudget     = 5 * time.Minute
	expireAccountBudget = 30 * time.Second
	dueAccountsPage     = 50
)

// expirer runs the expiry of credit batches.
type expirer struct {
	db       *pg.DB
	scope    pg.Scope
	logger   *slog.Logger
	failures metric.Int64Counter
	// deferred counts the batches a run could not write off in full, and deferredAmount what it left for a later run,
	// in billionths of a credit.
	deferred, deferredAmount metric.Int64Counter
	// What the tests turn down: the transactions one read of the ledger takes, the accounts one read of the due
	// accounts takes, and the budgets.
	movements     int32
	accounts      int32
	accountBudget time.Duration
	runBudget     time.Duration
}

func newExpirer(db *pg.DB, scope pg.Scope, logger *slog.Logger, meter metric.Meter) *expirer {
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("billing")
	}
	return &expirer{
		db: db, scope: scope, logger: logger,
		failures: counter(meter, "axiom.billing.expire.failures", "Accounts whose expired credit could not be written off in a run."),
		deferred: counter(meter, "axiom.billing.expire.deferred",
			"Expired credit batches a run wrote off only in part, the rest held back by reservations, freezes or the balance."),
		deferredAmount: counter(meter, "axiom.billing.expire.deferred.amount",
			"The credit those batches kept for a later run, in billionths of a credit."),
		movements: movementPage, accounts: dueAccountsPage, accountBudget: expireAccountBudget, runBudget: expireRunBudget,
	}
}

// run writes off, in every account with a batch past its expiry, what is left of the batch, going on after the
// account the last run reached, and returns how many accounts it changed and how many it could not write off. An
// account that fails is counted and logged and the run goes on; the error returned is for what stops the run. The scan
// wraps to the start when it has gone through every due account, or when the run's budget is spent it stops, and the
// next run continues from there.
func (e *expirer) run(ctx context.Context) (changed, failed int, err error) {
	start := time.Now()
	after, now, err := e.begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	for {
		due, err := e.due(ctx, now, after)
		if err != nil {
			return changed, failed, err
		}
		for _, account := range due {
			after = account
			c, f := e.visit(ctx, account, failed)
			changed, failed = changed+c, failed+f
			if ctx.Err() != nil || time.Since(start) > e.runBudget {
				return changed, failed, e.finish(ctx, after, failed)
			}
		}
		if len(due) < int(e.accounts) {
			return changed, failed, e.finish(ctx, uuid.UUID{}, failed)
		}
		if err := e.saveCursor(ctx, after); err != nil {
			return changed, failed, err
		}
	}
}

// visit writes off one account and counts what came of it: 1 changed when something was written off, 1 failed when it
// could not be. failedBefore is how many accounts failed in the run so far, to keep the log short.
func (e *expirer) visit(ctx context.Context, account uuid.UUID, failedBefore int) (changed, failed int) {
	written, err := e.expire(ctx, account)
	switch {
	case err != nil:
		e.failures.Add(ctx, 1)
		if failedBefore < maxProblemsLogged {
			e.logger.Error("billing: credit could not be written off", "account", account.String(), "err", err)
		}
		return 0, 1
	case written > 0:
		e.logger.Info("billing: credit expired", "account", account.String(), "amount", written)
		return 1, 0
	}
	return 0, 0
}

// finish stores where the next run goes on, and reports a run that failed some accounts beyond the ones logged.
func (e *expirer) finish(ctx context.Context, after uuid.UUID, failed int) error {
	if failed > maxProblemsLogged {
		e.logger.Error("billing: more accounts could not be written off than are listed", "accounts", failed)
	}
	select {
	case <-ctx.Done():
		return nil // the node is stopping; the next run starts from the stored place
	default:
		return e.saveCursor(ctx, after)
	}
}

// begin reads where the scan goes on and the database's time, which the whole run bounds expires_at by.
func (e *expirer) begin(ctx context.Context) (after uuid.UUID, now time.Time, err error) {
	err = e.db.Tx(ctx, e.scope, func(tx pg.Tx) error {
		after, err = queries.ExpiryCursor(ctx, tx)
		if errors.Is(err, pg.ErrNoRows) {
			after, err = uuid.UUID{}, nil
		}
		if err != nil {
			return err
		}
		now, err = queries.Now(ctx, tx)
		return err
	})
	if err != nil {
		return after, now, fmt.Errorf("billing: start the expiry: %w", err)
	}
	return after, now, nil
}

func (e *expirer) due(ctx context.Context, now time.Time, after uuid.UUID) (accounts []uuid.UUID, err error) {
	err = e.db.Tx(ctx, e.scope, func(tx pg.Tx) (err error) {
		accounts, err = queries.DueAccounts(ctx, tx, DueAccountsParams{Now: now, After: after, Max: e.accounts})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("billing: find the credit that is due: %w", err)
	}
	return accounts, nil
}

func (e *expirer) saveCursor(ctx context.Context, after uuid.UUID) error {
	err := e.db.Tx(ctx, e.scope, func(tx pg.Tx) error { return queries.SaveExpiryCursor(ctx, tx, after) })
	if err != nil {
		return fmt.Errorf("billing: save where the expiry goes on: %w", err)
	}
	return nil
}

// expire writes off the expired credit of one account within the account's budget, and returns the amount. It
// replays the history in short transactions that hold no lock, a page each, so that the account's calls are not held
// up by a long history, and then, in one transaction under the account's balance row lock, replays what is left and
// writes off.
func (e *expirer) expire(ctx context.Context, account uuid.UUID) (written int64, err error) {
	ctx, cancel := context.WithTimeout(ctx, e.accountBudget)
	defer cancel()
	for more := true; more; {
		if more, err = advanceAllocation(ctx, e.db, e.scope, account, e.movements); err != nil {
			return 0, err
		}
	}
	var held deferred
	err = e.db.Tx(ctx, e.scope, func(tx pg.Tx) (err error) {
		written, held, err = writeOffAccount(ctx, tx, account, e.movements)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("billing: write off the credit of %s: %w", account, err)
	}
	if held.batches > 0 {
		e.deferred.Add(ctx, int64(held.batches))
		e.deferredAmount.Add(ctx, held.amount)
	}
	return written, nil
}

// deferred is the expired credit a run left in the batches: still frozen, held by in-flight reservations, or beyond what
// the balance covers. It is written off in a later run, once the freeze or the reservation has gone
// (docs/domains/billing.md「额度批次」). amount is in billionths of a credit; batches counts the batches, one write-off
// each.
type deferred struct {
	amount  int64
	batches int
}

// writeOffAccount writes off the expired part of the account's batches in tx, which must be in the platform scope, and
// returns the amount and what the cap held back. It holds the account's balance row lock to the end of the transaction,
// replays the allocation to the latest movement, writes off, and stores the allocation.
//
// What counts as unspent is what the replay leaves at the moment the lock is taken, so credit spent after its expiry
// and before the write-off is used, not lapsed. The write-off is capped at the balance less reservations and freezes, and not
// by the floor: the floor is an allowance for charges, and an expiry that used it would put the user in debt for credit
// that the calls in flight could have used. With that cap, the balance stays at least what is reserved, so every
// reservation admitted before it can still be settled in full (its charge is capped by cap, which is at least its own
// amount). What the cap holds back is written off by a later run, when the reservations have settled. A second run
// finds nothing left to write off.
func writeOffAccount(ctx context.Context, tx pg.Tx, account uuid.UUID, size int32) (written int64, held deferred, err error) {
	balance, a, err := replayAccount(ctx, tx, account, size)
	if err != nil {
		return 0, deferred{}, err
	}
	if written, held, err = a.writeOffDue(ctx, tx, account, room(LockBalanceRow{Balance: balance.Balance, Reserved: balance.Reserved, Frozen: balance.Frozen}, 0)); err != nil {
		return 0, deferred{}, err
	}
	if written > 0 {
		if err := a.catchUp(ctx, tx, account, size); err != nil {
			return 0, deferred{}, err
		}
	}
	return written, held, a.save(ctx, tx, account)
}

// writeOffDue posts an expiry for each open batch whose time has come, in expiry order so that a cap of limit spares the
// batches that expire last, and returns the total and what it left in the batches. It does not replay what it posts.
func (a *allocation) writeOffDue(ctx context.Context, tx pg.Tx, account uuid.UUID, limit int64) (written int64, held deferred, err error) {
	now, err := queries.Now(ctx, tx)
	if err != nil {
		return 0, deferred{}, fmt.Errorf("billing: read the clock: %w", err)
	}
	for _, b := range a.open {
		if b.expiresAt == nil || b.expiresAt.After(now) {
			break
		}
		remaining := b.remaining()
		if remaining == 0 {
			continue
		}
		w := min(b.available(), limit)
		if w > 0 {
			e := event{site: b.site, account: account, kind: "expire", request: b.id, revision: b.expired}
			if posted, err := post(ctx, tx, e, line{bookUserBalance, w, siteCredits}, line{bookCreditExpired, -w, siteCredits}); err != nil {
				return 0, deferred{}, err
			} else if !posted {
				return 0, deferred{}, fmt.Errorf("billing: the expiry of batch %s at %d is posted already, but the allocation did not see it", b.id, b.expired)
			}
			limit -= w
			written += w
		}
		if w < remaining {
			held.amount += remaining - w
			held.batches++
		}
	}
	return written, held, nil
}

// expireArgs is the job of one run.
type expireArgs struct{ singleRun }

// Kind implements river.JobArgs.
func (expireArgs) Kind() string { return "billing.expire_credit" }

// ExpireConfig is what the expiry job needs: the database, the platform scope that only cmd/ builds, the queue the job
// runs on, where its log goes and the meter of its counter (nil for none).
type ExpireConfig struct {
	DB     *pg.DB
	Scope  pg.Scope
	Queue  *pg.Queue
	Logger *slog.Logger
	Meter  metric.Meter
}

// RegisterExpire adds the expiry of credit to the queue, before it starts: a run every minute, on whichever node leads
// the queue. It needs only the database, like the reconciliation.
func RegisterExpire(cfg ExpireConfig) error {
	e := newExpirer(cfg.DB, cfg.Scope, cfg.Logger, cfg.Meter)
	// An account that cannot be written off is not a failed job: the run counts and logs it and goes on, and a job that
	// failed would be retried with a growing delay while a retrying job keeps the next run from being inserted, which
	// would hold every other account's expiry back.
	work := func(ctx context.Context, _ *river.Job[expireArgs]) error {
		if _, _, err := e.run(ctx); err != nil {
			cfg.Logger.Error("billing: the expiry of credit stopped", "err", err)
		}
		return nil
	}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(work)); err != nil {
		return fmt.Errorf("billing: %w", err)
	}
	cfg.Queue.Every(expireEvery, func() river.JobArgs { return expireArgs{} })
	return nil
}
