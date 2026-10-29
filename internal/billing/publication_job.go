package billing

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"axiom/internal/platform/pg"
)

const priceRefreshMaxAge = 30 * time.Second

type publishPricesArgs struct{ singleRun }

func (publishPricesArgs) Kind() string { return "billing.publish_prices" }

// RegisterPrices schedules a single canonical publication round, independent of route builds. Composition supplies
// the database readers because billing cannot import the suppliers or accounts modules.
func RegisterPrices(queue *pg.Queue, refresh func(context.Context) error) error {
	work := river.WorkFunc(func(ctx context.Context, _ *river.Job[publishPricesArgs]) error { return refresh(ctx) })
	if err := river.AddWorkerSafely(queue.Workers(), work); err != nil {
		return err
	}
	queue.Every(5*time.Second, func() river.JobArgs { return publishPricesArgs{} })
	return nil
}
