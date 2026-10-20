package catalog

import (
	"context"
	"time"

	"axiom/internal/platform/egress"
)

// Publish and SetClock let a test replace a snapshot out of order and move the clock.
func (c *Catalog) Publish(s *Snapshot, confirmed time.Time) { c.publish(s, confirmed) }

// SetClock replaces the clock the staleness limit reads.
func (c *Catalog) SetClock(now func() time.Time) { c.now = now }

// NewTestFetcher reads the datasets from the given addresses instead of the registered ones.
func NewTestFetcher(c *egress.Client, urls map[PriceSource]string) *Fetcher {
	f := NewFetcher(c)
	f.urls = urls
	return f
}

// RunSyncRound runs one round of the scheduled sync, as the queue would, and returns its error.
func RunSyncRound(ctx context.Context, cfg SyncJobConfig) error {
	j, err := newSyncJob(cfg)
	if err != nil {
		return err
	}
	return j.work(ctx, nil)
}
