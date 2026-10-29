package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
)

// PageJobConfig is the scheduled watcher's platform-scoped storage and node-wide egress client.
type PageJobConfig struct {
	DB     *pg.DB
	Scope  pg.Scope
	Queue  *pg.Queue
	Client *egress.Client
	Logger *slog.Logger
}

type pageArgs struct{ syncArgs }

func (pageArgs) Kind() string { return "catalog.pages" }

// RegisterPages schedules due-page discovery every minute. Registered pages default to an hour between observations.
func RegisterPages(ctx context.Context, cfg PageJobConfig) error {
	if cfg.DB == nil || cfg.Queue == nil || cfg.Client == nil || cfg.Logger == nil {
		return errors.New("catalog: page watching needs database, queue, egress and logger")
	}
	cat, err := Open(cfg.DB, cfg.Scope, Config{Logger: cfg.Logger})
	if err != nil {
		return err
	}
	worker := river.WorkFunc(func(jobCtx context.Context, _ *river.Job[pageArgs]) error {
		round, cancel := context.WithTimeout(jobCtx, 9*time.Minute)
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		if ctx.Err() != nil {
			cancel()
		}
		return cat.watchPages(round, cfg.Client)
	})
	if err := river.AddWorkerSafely(cfg.Queue.Workers(), worker); err != nil {
		return err
	}
	cfg.Queue.Every(time.Minute, func() river.JobArgs { return pageArgs{} })
	return nil
}

func (c *Catalog) watchPages(ctx context.Context, client *egress.Client) error {
	if err := c.reportOverduePages(ctx); err != nil {
		return err
	}
	for ctx.Err() == nil {
		var page CatalogWatchedPage
		err := c.db.Tx(ctx, c.scope, func(tx pg.Tx) (err error) {
			page, err = New().ClaimWatchedPage(ctx, tx)
			return err
		})
		if errors.Is(err, pg.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim watched page: %w", err)
		}
		// The result's recording deadline is shorter than the five-minute lease, including a slow site rate gate.
		attempt, cancel := context.WithTimeout(ctx, 4*time.Minute)
		target := PageTarget{
			Author: page.Author, URL: page.Url, Currency: page.Currency,
			SectionStart: page.SectionStart, SectionEnd: page.SectionEnd, IntervalSeconds: page.IntervalSeconds,
			// The site row owns the configured rate; the inspector's default must not raise it.
			RateSeconds: 1,
		}
		content, fetchErr := fetchPage(attempt, client, target, c.reservePageRequest, c.backoffPageSite)
		cancel()
		// Node/round cancellation leaves the page due; only an attempt timeout is a page failure.
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		record, done := context.WithTimeout(context.WithoutCancel(ctx), syncRecordTimeout)
		err = c.recordPage(record, page, content, fetchErr)
		done()
		if err != nil {
			return fmt.Errorf("record watched page: %w", err)
		}
		if fetchErr != nil {
			c.cfg.Logger.WarnContext(ctx, "catalog page fetch failed", "page", page.ID.String(), "err", fetchErr)
		}
	}
	return nil
}
