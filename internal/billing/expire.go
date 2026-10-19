package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform/pg"
)

// expireEvery is how often credit past its expiry is written off (docs/domains/billing.md「额度批次」「到期」). A node
// that starts, or a new leader of the queue, runs it again at once; a run finds only what is due, so a repeat costs
// nothing.
const expireEvery = time.Minute

// expireBatch is how many accounts one query of the run takes.
const expireBatch = 200

// ExpireDue writes off, in every account, what is left of each credit batch whose expiry has passed, and returns how
// many accounts it changed. Each account is a transaction of its own under its balance row lock; one that fails does
// not stop the rest, and its error is joined into the one returned. It reads and writes every tenant, so it runs in
// the platform scope.
func ExpireDue(ctx context.Context, db *pg.DB, platform pg.Scope, logger *slog.Logger) (changed int, err error) {
	var failed []error
	for after := (uuid.UUID{}); ; {
		var due []uuid.UUID
		err := db.Tx(ctx, platform, func(tx pg.Tx) (err error) {
			due, err = queries.DueAccounts(ctx, tx, DueAccountsParams{After: after, Max: expireBatch})
			return err
		})
		if err != nil {
			return changed, errors.Join(append(failed, fmt.Errorf("billing: find the credit that is due: %w", err))...)
		}
		for _, account := range due {
			var written int64
			err := db.Tx(ctx, platform, func(tx pg.Tx) (err error) {
				written, err = ExpireAccount(ctx, tx, account)
				return err
			})
			if err != nil {
				failed = append(failed, fmt.Errorf("billing: expire the credit of %s: %w", account, err))
				if ctx.Err() != nil {
					return changed, errors.Join(failed...) // the rest would fail for the same reason
				}
				continue
			}
			if written > 0 {
				changed++
				logger.Info("billing: credit expired", "account", account.String(), "amount", written)
			}
		}
		if len(due) < expireBatch {
			return changed, errors.Join(failed...)
		}
		after = due[len(due)-1]
	}
}

// ExpireAccount writes off the expired part of the account's batches in tx, which must be in the platform scope, and
// returns the amount. It holds the account's balance row lock to the end of the transaction, replays the allocation to
// the latest movement, writes off, and stores the allocation.
//
// What counts as unspent is what the replay leaves at the moment the lock is taken, so credit spent after its expiry
// and before the write-off is used, not lapsed. The write-off is capped at the balance less the reservations and the
// floor (room), so that every reservation admitted before it can still be settled in full: its charge is capped at
// what the balance leaves above the floor once the other reservations are covered (cap), and the write-off leaves
// that no lower than the reservation's own amount. What the cap holds back is written off by a later run, when the
// reservation has settled. A batch of which nothing is left is skipped, so a run that finds nothing to do writes
// nothing, and a second run after one writes nothing either.
func ExpireAccount(ctx context.Context, tx pg.Tx, account uuid.UUID) (written int64, err error) {
	balance, err := lockAccount(ctx, tx, account)
	if err != nil {
		return 0, err
	}
	a, err := loadAllocation(ctx, tx, account)
	if err != nil {
		return 0, err
	}
	if err := a.catchUp(ctx, tx, account); err != nil {
		return 0, err
	}
	if got := a.funded(); got != balance.Balance {
		return 0, fmt.Errorf("billing: the allocation of %s funds %d of a balance of %d; nothing is written off", account, got, balance.Balance)
	}
	now, err := queries.Now(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("billing: read the clock: %w", err)
	}
	left := room(balance, 0)
	for _, b := range a.open { // in expiry order, so a cap spares the batches that expire last
		if b.expiresAt == nil || b.expiresAt.After(now) || left == 0 {
			break
		}
		w := min(b.remaining(), left)
		e := event{site: b.site, account: account, kind: "expire", request: b.id, revision: b.expired}
		if posted, err := post(ctx, tx, e, line{bookUserBalance, w}, line{bookCreditExpired, -w}); err != nil {
			return 0, err
		} else if !posted {
			return 0, fmt.Errorf("billing: the expiry of batch %s at %d is posted already, but the allocation did not see it", b.id, b.expired)
		}
		left -= w
		written += w
	}
	if written > 0 {
		if err := a.catchUp(ctx, tx, account); err != nil {
			return 0, err
		}
	}
	return written, a.save(ctx, tx, account)
}

// expireArgs is the job of one run.
type expireArgs struct{ singleRun }

// Kind implements river.JobArgs.
func (expireArgs) Kind() string { return "billing.expire_credit" }

// ExpireConfig is what the expiry job needs: the database, the platform scope that only cmd/ builds, the queue the job
// runs on and where its log goes.
type ExpireConfig struct {
	DB     *pg.DB
	Scope  pg.Scope
	Queue  *pg.Queue
	Logger *slog.Logger
}

// RegisterExpire adds the expiry of credit to the queue, before it starts: a run every minute, on whichever node leads
// the queue. It needs only the database, like the reconciliation.
func RegisterExpire(cfg ExpireConfig) error {
	// An account that cannot be written off is not a failed job: the job did the rest, and River's retries back off for
	// longer each time while a retrying job keeps the next run from being inserted, which would hold every other
	// account's expiry back. The next run tries the account again.
	work := func(ctx context.Context, _ *river.Job[expireArgs]) error {
		if _, err := ExpireDue(ctx, cfg.DB, cfg.Scope, cfg.Logger); err != nil {
			cfg.Logger.Error("billing: credit could not be written off", "err", err)
		}
		return nil
	}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(work)); err != nil {
		return fmt.Errorf("billing: %w", err)
	}
	cfg.Queue.Every(expireEvery, func() river.JobArgs { return expireArgs{} })
	return nil
}
