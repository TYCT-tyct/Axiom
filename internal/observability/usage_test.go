package observability

import (
	"errors"
	"math"
	"math/big"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestUsageWindow(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, granularity string
		since, until      time.Time
		valid             bool
	}{
		{"90 days", "day", at, at.Add(90 * 24 * time.Hour), true},
		{"91 days", "day", at, at.Add(91 * 24 * time.Hour), false},
		{"7 days hourly", "hour", at, at.Add(7 * 24 * time.Hour), true},
		{"8 days hourly", "hour", at, at.Add(8 * 24 * time.Hour), false},
		{"empty", "day", at, at, false},
		{"backward", "week", at, at.Add(-time.Hour), false},
		{"partial hour", "day", at.Add(time.Second), at.Add(time.Hour), false},
		{"offset", "week", at.In(time.FixedZone("offset", 3600)), at.Add(time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateUsageWindow(tc.since, tc.until, tc.granularity); (err == nil) != tc.valid {
				t.Fatalf("validation=%v, valid=%v", err, tc.valid)
			}
		})
	}
}

func usageRead(t *testing.T, db *pg.DB, scope pg.Scope, q UsageQuery) UsageStatistics {
	t.Helper()
	var result UsageStatistics
	if err := db.ReadTx(t.Context(), scope, func(tx pg.Tx) (err error) { result, err = ReadUsageStatistics(t.Context(), tx, q); return err }); err != nil {
		t.Fatal(err)
	}
	return result
}

func usageBatch(t *testing.T, db *pg.DB, size int32) bool {
	t.Helper()
	var more bool
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) { more, err = rollupUsageBatch(t.Context(), tx, size); return err }); err != nil {
		t.Fatal(err)
	}
	return more
}

func TestUsageRollupRetryLateArrivalAndMoney(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	account := uuid.NewV7()
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	a, b := record(account, at.Add(time.Minute)), record(account, at.Add(time.Minute))
	b.Site, b.Workspace, b.Key, b.Model = a.Site, a.Workspace, a.Key, a.Model
	a.ErrorKind = ""
	a.PriceNano, b.PriceNano = math.MaxInt64, math.MaxInt64
	physical := int64(200)
	a.PhysicalInput = &physical
	a.Usage = map[catalog.Item]int64{catalog.ItemInput: 120, catalog.ItemOutput: 30, catalog.ItemReasoning: 900, catalog.ItemCacheRead: 80, catalog.ItemCacheWrite: 5, catalog.ItemCacheWrite1h: 6}
	b.Usage = map[catalog.Item]int64{}
	keep(t, db, a, b)
	q := UsageQuery{Account: account, Since: at, Until: at.Add(24 * time.Hour), Granularity: "day", GroupBy: "workspace"}
	// A failure after the aggregate statement must roll back both counters and journal consumption.
	aborted := errors.New("crash before commit")
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		if _, err := rollupUsageBatch(t.Context(), tx, 1); err != nil {
			return err
		}
		return aborted
	})
	if !errors.Is(err, aborted) {
		t.Fatal(err)
	}
	if got := usageRead(t, db, accountScope(account), q); len(got.Totals) != 0 {
		t.Fatalf("rollback leaked %+v", got)
	}
	for range 3 {
		usageBatch(t, db, 1)
	}
	first := usageRead(t, db, accountScope(account), q)
	usageBatch(t, db, 1)
	if diff := cmp.Diff(first, usageRead(t, db, accountScope(account), q)); diff != "" {
		t.Fatal(diff)
	}
	if len(first.Totals) != 1 {
		t.Fatalf("totals %+v", first)
	}

	hit, success := 0.4, 0.5
	want := UsageMetrics{
		Currency: "USD", Requests: "2", Successes: "1", Spend: "18446744073709551614",
		Input: "120", Output: "930", CacheRead: "80", CacheWrite: "11", CacheRateRequests: "1", CacheHitRate: &hit, SuccessRate: &success,
	}
	if diff := cmp.Diff(want, first.Totals[0]); diff != "" {
		t.Fatal(diff)
	}
	// Recorder replay uses ON CONFLICT DO NOTHING, which must not enqueue a second event.
	keep(t, db, a)
	usageBatch(t, db, 1000)
	if diff := cmp.Diff(first, usageRead(t, db, accountScope(account), q)); diff != "" {
		t.Fatal(diff)
	}
	late := record(account, at.Add(2*time.Minute))
	late.Site, late.Workspace, late.Key, late.Model = a.Site, a.Workspace, a.Key, a.Model
	keep(t, db, late)
	usageBatch(t, db, 1000)
	got := usageRead(t, db, accountScope(account), q)
	if got.Totals[0].Requests != "3" {
		t.Fatalf("late event missing: %+v", got)
	}
	sum := new(big.Int)
	for _, call := range list(t, db, accountScope(account), OwnQuery{Account: account, Since: q.Since, Until: q.Until}) {
		sum.Add(sum, big.NewInt(call.PriceNano))
	}
	if got.Totals[0].Spend != sum.String() {
		t.Fatalf("spend %s, listCalls %s", got.Totals[0].Spend, sum)
	}
}

func TestUsageFiltersComparisonAndBuckets(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	account := uuid.NewV7()
	a, b, c, foreign := record(account, at), record(account, at.Add(25*time.Hour)), record(account, at.Add(-48*time.Hour)), record(uuid.NewV7(), at)
	b.Site, b.Key, b.Workspace, b.Model = a.Site, a.Key, a.Workspace, a.Model
	foreign.Site = a.Site
	previousLast := record(account, at.Add(-time.Hour))
	beforePrevious, afterCurrent := record(account, at.Add(-49*time.Hour)), record(account, at.Add(48*time.Hour))
	keep(t, db, a, b, c, previousLast, beforePrevious, afterCurrent, foreign)
	usageBatch(t, db, 1000)
	q := UsageQuery{Account: account, Since: at, Until: at.Add(48 * time.Hour), Granularity: "day", Previous: true}
	result := usageRead(t, db, accountScope(account), q)
	previous := q
	previous.Since = q.Since.Add(-q.Until.Sub(q.Since))
	previous.Until = q.Since
	previous.Previous = false
	if diff := cmp.Diff(result.Previous, usageRead(t, db, accountScope(account), previous).Totals); diff != "" {
		t.Fatal(diff)
	}
	for _, tc := range []struct {
		name, group, granularity string
		key, workspace           *uuid.UUID
		model                    string
		account                  uuid.UUID
		want                     int
	}{
		{"workspace", "workspace", "day", nil, nil, "", account, 2},
		{"key", "key", "day", nil, nil, "", account, 2},
		{"model", "model", "day", nil, nil, "", account, 2},
		{"hour", "model", "hour", nil, nil, "", account, 2},
		{"week", "workspace", "week", nil, nil, "", account, 1},
		{"combined", "workspace", "day", &a.Key, &a.Workspace, a.Model, account, 2},
		{"foreign workspace", "workspace", "day", nil, &foreign.Workspace, "", account, 0},
		{"foreign key", "key", "day", &foreign.Key, nil, "", account, 0},
		{"foreign account", "workspace", "day", nil, nil, "", foreign.Account, 0},
		{"absent model", "model", "day", nil, nil, "absent", account, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := q
			query.GroupBy, query.Granularity, query.Key, query.Workspace, query.Model, query.Account = tc.group, tc.granularity, tc.key, tc.workspace, tc.model, tc.account
			got := usageRead(t, db, accountScope(account), query)
			if len(got.Buckets) != tc.want {
				t.Fatalf("buckets %+v", got)
			}
			if tc.want > 0 && !got.Buckets[0].At.Equal(at) {
				t.Fatalf("UTC bucket %+v", got.Buckets[0])
			}
		})
	}
	q.Since, q.Until = at.Add(3*24*time.Hour), at.Add(4*24*time.Hour)
	empty := usageRead(t, db, accountScope(account), q)
	if len(empty.Buckets) != 0 || len(empty.Totals) != 0 {
		t.Fatalf("empty %+v", empty)
	}
}

func TestUsageBackfillResumesAcrossPartitions(t *testing.T) {
	// Build the historical rows without journaling, then process the backfill and live rows together.
	db, database := usageDBBeforeRollup(t)
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	account := uuid.NewV7()
	a, b := record(account, at.Add(-time.Minute)), record(account, at.Add(time.Minute))
	keep(t, db, a, pendingOf(record(account, at)), b)
	beginUsageBackfill(t, database)
	q := UsageQuery{Account: account, Since: at.Add(-time.Hour), Until: at.Add(time.Hour), Granularity: "hour"}
	if !usageBatch(t, db, 1) {
		t.Fatal("backfill ended before second row")
	}
	if got := usageRead(t, db, accountScope(account), q); got.BackfillComplete {
		t.Fatal("prematurely complete")
	}
	late := record(account, at.Add(-2*time.Minute))
	keep(t, db, late)
	for range 4 {
		usageBatch(t, db, 1)
	}
	got := usageRead(t, db, accountScope(account), q)
	if !got.BackfillComplete || got.Totals[0].Requests != "3" {
		t.Fatalf("backfill %+v", got)
	}
	usageBatch(t, db, 1)
	if diff := cmp.Diff(got, usageRead(t, db, accountScope(account), q)); diff != "" {
		t.Fatal(diff)
	}
}

func TestUsageMissingTokensAndCurrencies(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	account := uuid.NewV7()
	a, b := record(account, at), record(account, at)
	a.Usage = map[catalog.Item]int64{}
	b.Currency = mustCurrency("EUR")
	b.Usage = map[catalog.Item]int64{catalog.ItemCacheRead: 100}
	keep(t, db, a, b, pendingOf(record(account, at)))
	usageBatch(t, db, 1000)
	got := usageRead(t, db, accountScope(account), UsageQuery{Account: account, Since: at, Until: at.Add(time.Hour), Granularity: "hour"})
	if len(got.Totals) != 2 {
		t.Fatalf("currencies %+v", got)
	}
	for _, m := range got.Totals {
		if m.Requests != "1" || m.CacheHitRate != nil || m.CacheRateRequests != "0" || m.Input != "" {
			t.Fatalf("missing tokens %+v", m)
		}
	}
}

func TestUsageWorkerRetriesDurably(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	queue := db.NewQueue(quiet, platformScope, nil)
	if err := RegisterUsage(db, platformScope, queue); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	account := uuid.NewV7()
	keep(t, db, record(account, at))
	if err := pgtest.Run(t.Context(), database.Superuser, `ALTER TABLE observability.usage_hourly ADD CONSTRAINT fail_rollup CHECK(requests < 1)`); err != nil {
		t.Fatal(err)
	}
	worker := &usageWorker{db: db, scope: platformScope}
	clock := &webhookClock{at: at}
	first, err := pgtest.WorkJob(t, database, river.WorkFunc(worker.work), usageArgs{}, nil, clock)
	if err == nil {
		t.Fatal("injected database failure was swallowed")
	}
	if err := pgtest.Run(t.Context(), database.Superuser, `ALTER TABLE observability.usage_hourly DROP CONSTRAINT fail_rollup`); err != nil {
		t.Fatal(err)
	}
	clock.at = clock.at.Add(time.Hour)
	second, err := pgtest.WorkJob(t, database, river.WorkFunc(worker.work), usageArgs{}, first.Job, clock)
	if err != nil || second.Job.State != rivertype.JobStateCompleted || second.Job.Attempt != 2 {
		t.Fatalf("retry: %+v, %v", second, err)
	}
	q := UsageQuery{Account: account, Since: at, Until: at.Add(time.Hour), Granularity: "hour"}
	got := usageRead(t, db, accountScope(account), q)
	if len(got.Totals) != 1 || got.Totals[0].Requests != "1" {
		t.Fatalf("retry counted %+v", got)
	}
	if err := worker.work(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(got, usageRead(t, db, accountScope(account), q)); diff != "" {
		t.Fatal(diff)
	}
}

func TestUsageConcurrentWorkerSkipsHeldPosition(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT 1 FROM observability.usage_backfill WHERE singleton FOR UPDATE`); err != nil {
			return err
		}
		if more := usageBatch(t, db, 1); more {
			t.Fatal("another worker's position was advanced")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
