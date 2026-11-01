package billing

import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/platform/pg"
)

// The subscription job (docs/domains/billing.md「订阅套餐」): a run passes the windows whose time came for accounts that
// stay idle, expires terms that ended, and writes off what is left of an expired subscription's liability once its
// holds have all ended. Admission does the same for a window it finds due, so a busy account never waits for the run.
const (
	subscriptionEvery = time.Minute
	subscriptionPage  = 50
)

// subscriptionArgs is the job of one run.
type subscriptionArgs struct{ singleRun }

// Kind implements river.JobArgs.
func (subscriptionArgs) Kind() string { return "billing.subscription_windows" }

// SubscriptionConfig is what the subscription job needs: the database, the platform scope that only cmd/ builds, the
// queue the job runs on, where its log goes and the meter of its counters (nil for none).
type SubscriptionConfig struct {
	DB     *pg.DB
	Scope  pg.Scope
	Queue  *pg.Queue
	Logger *slog.Logger
	Meter  metric.Meter
}

// RegisterSubscriptions adds the subscription job to the queue, before it starts: a run every minute, on whichever node
// leads the queue. A subscription that cannot be closed is counted and logged and the run goes on, as the expiry does:
// a failed job would back off and hold the next run back.
func RegisterSubscriptions(cfg SubscriptionConfig) error {
	if cfg.Meter == nil {
		cfg.Meter = noop.NewMeterProvider().Meter("billing")
	}
	failures := counter(cfg.Meter, "axiom.billing.subscription.failures", "Subscriptions a run could not pass, expire or close.")
	work := func(ctx context.Context, _ *river.Job[subscriptionArgs]) error {
		if err := runSubscriptions(ctx, cfg, failures); err != nil {
			cfg.Logger.Error("billing: the subscription run stopped", "err", err)
		}
		return nil
	}
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), river.WorkFunc(work)); err != nil {
		return fmt.Errorf("billing: %w", err)
	}
	cfg.Queue.Every(subscriptionEvery, func() river.JobArgs { return subscriptionArgs{} })
	return nil
}

func runSubscriptions(ctx context.Context, cfg SubscriptionConfig, failures metric.Int64Counter) error {
	var due []DueSubscriptionsRow
	if err := cfg.DB.Tx(ctx, cfg.Scope, func(tx pg.Tx) error {
		var err error
		due, err = queries.DueSubscriptions(ctx, tx, subscriptionPage)
		return err
	}); err != nil {
		return fmt.Errorf("billing: find the subscriptions that are due: %w", err)
	}
	for _, d := range due {
		if err := ctx.Err(); err != nil {
			return err // the node is stopping; the next run finds it again
		}
		if err := visitSubscription(ctx, cfg, d.ID, d.AccountID); err != nil {
			failures.Add(ctx, 1)
			cfg.Logger.Error("billing: a subscription could not be passed or closed", "subscription", d.ID.String(),
				"account", d.AccountID.String(), "err", err)
		}
	}
	return nil
}

// visitSubscription brings one due subscription up to date in its own transaction under its account's balance lock.
func visitSubscription(ctx context.Context, cfg SubscriptionConfig, id, account uuid.UUID) error {
	return cfg.DB.Tx(ctx, cfg.Scope, func(tx pg.Tx) error {
		if _, err := lockAccount(ctx, tx, account); err != nil {
			return err
		}
		sub, err := subscriptionOf(ctx, tx, id)
		if err != nil {
			return err
		}
		if sub.State == "active" {
			if _, err := planWindowNow(ctx, tx, account); err != nil {
				return err
			}
			if sub, err = subscriptionOf(ctx, tx, id); err != nil {
				return err
			}
		}
		return writeOffSubscription(ctx, tx, sub)
	})
}

// subscriptionOf reads one subscription by ID.
func subscriptionOf(ctx context.Context, tx pg.Tx, id uuid.UUID) (Subscription, error) {
	r, err := queries.Subscription(ctx, tx, id)
	if err != nil {
		return Subscription{}, fmt.Errorf("billing: read the subscription: %w", err)
	}
	return Subscription{
		ID: r.ID, Site: r.SiteID, Account: r.AccountID, PlanID: r.PlanID, PlanName: r.PlanName,
		StartsAt: r.StartsAt, EndsAt: r.EndsAt, State: r.State, CurrentSeq: r.CurrentSeq, ClosedAt: r.ClosedAt,
	}, nil
}
