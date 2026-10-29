package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
)

// A Thursday source date can remain current through Easter Tuesday; daily polling adds up to another day.
const exchangeMaxAge = 7 * 24 * time.Hour

type exchangeArgs struct{}

func (exchangeArgs) Kind() string { return "billing.exchange_sync" }
func (exchangeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueSyncs, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
		rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

// ExchangeSyncConfig is the opt-in reference-rate download, outside the money queue and the request path.
type ExchangeSyncConfig struct {
	DB     *pg.DB
	Scope  pg.Scope
	Queue  *pg.Queue
	Client *egress.Client
	Logger *slog.Logger
}

// RegisterExchangeSync registers before Queue.Start. The hourly check downloads at most once per persisted 24 hours.
func RegisterExchangeSync(ctx context.Context, cfg ExchangeSyncConfig) error {
	if cfg.DB == nil || cfg.Queue == nil || cfg.Client == nil || cfg.Logger == nil {
		return errors.New("billing: exchange sync needs database, queue, egress and logger")
	}
	j := exchangeJob{db: cfg.DB, scope: cfg.Scope, client: cfg.Client, logger: cfg.Logger, url: exchangeURL}
	work := river.WorkFunc(func(jobCtx context.Context, _ *river.Job[exchangeArgs]) error {
		run, cancel := context.WithCancel(jobCtx)
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		if ctx.Err() != nil {
			cancel()
		}
		return j.run(run)
	})
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), work); err != nil {
		return err
	}
	cfg.Queue.Every(time.Hour, func() river.JobArgs { return exchangeArgs{} })
	return nil
}

type exchangeJob struct {
	db     *pg.DB
	scope  pg.Scope
	client *egress.Client
	logger *slog.Logger
	url    string
	now    func() time.Time
}

func (j *exchangeJob) timestamp(ctx context.Context, tx pg.Tx) (time.Time, error) {
	if j.now != nil {
		return j.now(), nil
	}
	var at time.Time
	err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&at)
	return at, err
}

func (j *exchangeJob) claim(ctx context.Context) (time.Time, bool, error) {
	var at time.Time
	var due bool
	err := j.db.Tx(ctx, j.scope, func(tx pg.Tx) error {
		state, err := queries.LockExchangeSync(ctx, tx)
		if err != nil {
			return err
		}
		at, err = j.timestamp(ctx, tx)
		if err != nil {
			return err
		}
		if err := exchangeAlert(ctx, tx, state, at); err != nil {
			return err
		}
		due = state.LastAttempt == nil || at.Sub(*state.LastAttempt) >= 24*time.Hour
		if !due {
			return nil
		}
		return queries.ClaimExchangeSync(ctx, tx, &at)
	})
	return at, due, err
}

func (j *exchangeJob) run(ctx context.Context) error {
	at, due, err := j.claim(ctx)
	if err != nil || !due {
		return err
	}
	inputs, date, fetchErr := fetchExchange(ctx, j.client, j.url, at)
	// Cancellation must not prevent the stale/recovery signal from reaching its durable boundary.
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if fetchErr != nil {
		j.logger.ErrorContext(record, "billing.exchange.fetch_failed", "err", fetchErr)
	}
	err = j.complete(record, inputs, date, at, fetchErr)
	if err != nil {
		return fmt.Errorf("billing: record exchange sync: %w", err)
	}
	return nil
}

func (j *exchangeJob) complete(ctx context.Context, inputs []ExchangeInput, date, at time.Time, fetchErr error) error {
	return j.db.Tx(ctx, j.scope, func(tx pg.Tx) error {
		state, err := queries.LockExchangeSync(ctx, tx)
		if err != nil {
			return err
		}
		if fetchErr == nil {
			if err := applyExchange(ctx, tx, state, inputs, date, at); err != nil {
				return err
			}
			state.SourceAt = &date
		}
		now, err := j.timestamp(ctx, tx)
		if err != nil {
			return err
		}
		return exchangeAlert(ctx, tx, state, now)
	})
}

func applyExchange(ctx context.Context, tx pg.Tx, state BillingExchangeSync, inputs []ExchangeInput, date, at time.Time) error {
	if state.SourceAt != nil && date.Before(*state.SourceAt) {
		return errors.New("billing: exchange source date regressed")
	}
	if state.LastAttempt == nil || !state.LastAttempt.Equal(at) {
		return errors.New("billing: exchange sync claim superseded")
	}
	if state.SourceAt == nil || date.After(*state.SourceAt) {
		for _, in := range inputs {
			if err := applyExchangePair(ctx, tx, in, date); err != nil {
				return err
			}
		}
	}
	return queries.CompleteExchangeSync(ctx, tx, CompleteExchangeSyncParams{At: &at, SourceAt: &date})
}

func applyExchangePair(ctx context.Context, tx pg.Tx, in ExchangeInput, date time.Time) error {
	previous, err := queries.LatestExchangeRate(ctx, tx, LatestExchangeRateParams{Base: in.Base, Quote: in.Quote})
	if err != nil {
		return err
	}
	if len(previous) != 0 {
		if previous[0].Source == "manual" {
			return nil
		}
		in.Kind, in.ExpectedVersion = "adjustment", previous[0].Version
	}
	_, err = setExchangeRate(ctx, tx, audit.System("billing.exchange_sync"), in, "ecb", &date)
	if err != nil {
		return fmt.Errorf("billing: exchange %s/%s: %w", in.Base, in.Quote, err)
	}
	return nil
}

func exchangeAlert(ctx context.Context, tx pg.Tx, state BillingExchangeSync, at time.Time) error {
	since := state.SourceAt
	if since == nil {
		since = state.StartedAt
	}
	stale := since != nil && at.Sub(*since) > exchangeMaxAge
	if stale == state.Alerted {
		return nil
	}
	if err := alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
		Kind: "billing.exchange.stale", Key: "ecb", Severity: "warning", Resolved: !stale,
		Fields: map[string]string{"source": "ecb"},
	}); err != nil {
		return err
	}
	return queries.SetExchangeAlert(ctx, tx, stale)
}
