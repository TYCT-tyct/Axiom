package billing

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgtype" //nolint:depguard // the baseline writes InsertReservationAtParams, whose UUID arrays are pgtype.FlatArray

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// reserveOne admits one request through the batch path, in the caller's transaction: a batch of one is what production
// does for a single admission. The per-request error is the batch's refusal for it; a database error takes the whole
// batch down and comes back as err.
func (b *Billing) reserveOne(ctx context.Context, tx pg.Tx, a Admitted, id uuid.UUID, d Demand, q []Quotable) (*Hold, error) {
	holds, _, errs, err := b.ReserveMany(ctx, tx, []ReserveRequest{{Admitted: a, ID: id, Demand: d, Quotables: q, Segments: b.config.Segments.Load()}})
	if err != nil {
		return nil, err
	}
	return holds[0], errs[0]
}

// reserveMany reserves amounts for new requests in one transaction, in the order given.
func (b *bank) reserveMany(a Admitted, amounts []int64) ([]*Hold, []error, error) {
	b.t.Helper()
	reqs := make([]ReserveRequest, len(amounts))
	for i, n := range amounts {
		reqs[i] = ReserveRequest{Admitted: a, ID: uuid.NewV7(), Demand: Demand{TextBytes: n}, Quotables: []Quotable{b.exact}}
	}
	var holds []*Hold
	var errs []error
	err := b.app.Tx(b.t.Context(), a.Scope, func(tx pg.Tx) (err error) {
		holds, _, errs, err = b.billing.ReserveMany(b.t.Context(), tx, reqs)
		return err
	})
	return holds, errs, err
}

// randomAmounts is n reservation amounts from 1 to 100.
func randomAmounts(r *rand.Rand, n int) []int64 {
	amounts := make([]int64, n)
	for i := range amounts {
		amounts[i] = r.Int64N(100) + 1
	}
	return amounts
}

// TestReserveManyIsTheSerialOrder: a batch admits exactly what the same requests would one at a time in the same
// order, refusal for refusal, because each quote sees the amounts the earlier ones would reserve.
func TestReserveManyIsTheSerialOrder(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // G404: test data
	for trial := range 3 {
		amounts := randomAmounts(r, 20)
		batch := newBank(t, 1000, -200, Config{})
		holds, errs, err := batch.reserveMany(batch.admitted, amounts)
		if err != nil {
			t.Fatal(err)
		}
		serial := newBank(t, 1000, -200, Config{})
		for i, amount := range amounts {
			h, err := serial.reserve(serial.admitted, amount)
			if msg := sameOutcome(holds[i], errs[i], h, err); msg != "" {
				t.Fatalf("trial %d request %d (%d): %s", trial, i, amount, msg)
			}
		}
		batch.consistent()
	}
}

// sameOutcome says how a batch's outcome for one request differs from the serial order's, or "" if it does not.
func sameOutcome(batched *Hold, batchErr error, serial *Hold, serialErr error) string {
	switch {
	case serialErr != nil && batchErr == nil:
		return "the batch admitted it, the serial order refused it: " + serialErr.Error()
	case serialErr == nil && batchErr != nil:
		return "the batch refused it (" + batchErr.Error() + "), the serial order admitted it"
	case serialErr != nil:
		var b, s *Insufficient
		if !errors.As(batchErr, &b) || !errors.As(serialErr, &s) || b.Source != s.Source {
			return "batch refused " + batchErr.Error() + ", serial refused " + serialErr.Error()
		}
	case batched == nil || batched.Amount.CeilAmount() != serial.Amount.CeilAmount() || !slices.Equal(batched.Kept, serial.Kept):
		return fmt.Sprintf("batch %+v, serial %+v", batched, serial)
	}
	return ""
}

// TestReserveManyStorm: several batches reserve at once; the account never passes its floor, and every refusal was for
// more than what was left after all the reservations that succeeded.
func TestReserveManyStorm(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, -200, Config{})
	const capacity = 1200
	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // G404: test data
	var mu sync.Mutex
	var wg sync.WaitGroup
	var total int64
	var refused []int64
	for range 4 {
		amounts := randomAmounts(r, 10)
		wg.Go(func() {
			holds, errs, err := b.reserveMany(b.admitted, amounts)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for i, n := range amounts {
				if errs[i] != nil {
					refused = append(refused, n)
					continue
				}
				total += holds[i].Amount.CeilAmount()
			}
		})
	}
	wg.Wait()
	if total > capacity {
		t.Errorf("reserved %d, past the capacity %d", total, capacity)
	}
	for _, n := range refused {
		if n <= capacity-total {
			t.Errorf("refused %d with %d left", n, capacity-total)
		}
	}
	b.consistent()
}

// TestReserveManyKeepsRefusalsApart: one unaffordable request in the middle of a batch is refused on its own; the
// requests before and after it reserve.
func TestReserveManyKeepsRefusalsApart(t *testing.T) {
	t.Parallel()
	b := newBank(t, 100, 0, Config{})
	holds, errs, err := b.reserveMany(b.admitted, []int64{10, 10_000, 10})
	if err != nil {
		t.Fatal(err)
	}
	if holds[0] == nil || holds[2] == nil || errs[0] != nil || errs[2] != nil {
		t.Fatalf("the affordable requests: holds %v %v, errors %v %v", holds[0], holds[2], errs[0], errs[2])
	}
	var short *Insufficient
	if !errors.As(errs[1], &short) || short.Source != SourceBalance {
		t.Fatalf("the unaffordable request: %v, want an insufficient balance", errs[1])
	}
	if n := b.count("SELECT count(*) FROM billing.reservations WHERE state = 'pending'"); n != 2 {
		t.Errorf("%d pending reservations, want 2", n)
	}
	b.consistent()
}

// historicInsertReservation is the single reservation's insert, removed with Reserve (#530). It stays as the baseline
// the batch's pipelined arguments are checked against, placeholders and order unchanged.
const historicInsertReservation = `WITH reservation AS (
    INSERT INTO billing.reservations (request_id, site_id, account_id, workspace_id, key_id, holder_id,
        workspace_window, key_window, segment, amount, state, deadline, prices, fallbacks, models, endpoints, exchange_rates,
        price_versions)
    VALUES ($8, $3, $4, $1, $6, $9,
        $2, $7, 1, $5, 'pending', clock_timestamp() + make_interval(0, 0, 0, 0, 0, 0, $10::float8),
        $11::text[], $12::text[], $13, $14, $15::jsonb, $16)
), balance AS (
    UPDATE billing.balances b SET reserved = b.reserved + $5 WHERE b.account_id = $4
)
INSERT INTO billing.spend AS s (subject, window_start, site_id, account_id, reserved)
VALUES ($1, $2, $3, $4, $5),
    ($6, $7, $3, $4, $5)
ON CONFLICT (subject, window_start) DO UPDATE SET reserved = s.reserved + excluded.reserved`

// writeHistoricReservation writes a reservation through the removed single insert, in its generated argument order.
func writeHistoricReservation(ctx context.Context, tx pg.Tx, p InsertReservationAtParams) error {
	_, err := tx.Exec(ctx, historicInsertReservation,
		p.WorkspaceID, p.WorkspaceWindow, p.SiteID, p.AccountID, p.Amount, p.KeyID, p.KeyWindow, p.RequestID,
		p.HolderID, p.LifetimeSeconds, p.Prices, p.Fallbacks, p.Models, p.Endpoints, p.ExchangeRates, p.PriceVersions)
	return err
}

// TestReserveManyRowsMatchTheHistoricInsert: the batch's pipelined insert writes the same row, column for column, as
// the single reservation's insert did before #530 removed it. The column list comes from the table itself and every
// column is compared, so a migration that adds one cannot slip past. All columns but three must be equal; request_id
// names each row, and created_at and deadline are clock values the two writes cannot share (created_at is the batch's
// call start, the historic insert's default).
func TestReserveManyRowsMatchTheHistoricInsert(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1e6, 0, Config{})
	at := time.Now().UTC().Truncate(time.Microsecond)
	lifetime := 1733 * time.Second
	day := time.Now().UTC().Truncate(24 * time.Hour)
	params := func(id uuid.UUID) InsertReservationAtParams {
		return InsertReservationAtParams{
			WorkspaceID: b.admitted.Workspace, WorkspaceWindow: day,
			SiteID: b.admitted.Site, AccountID: b.admitted.Account, Amount: 100,
			KeyID: b.admitted.Key, KeyWindow: day.Add(-24 * time.Hour),
			RequestID: id, HolderID: b.billing.holder, LifetimeSeconds: lifetime.Seconds(),
			Prices: []string{`{"v":1,"currency":"USD"}`}, Fallbacks: []string{`{"per_use":{}}`},
			Models:        pgtype.FlatArray[uuid.UUID]{uuid.NewV7()},
			Endpoints:     pgtype.FlatArray[uuid.UUID]{uuid.NewV7()},
			ExchangeRates: []byte(`[]`),
			PriceVersions: pgtype.FlatArray[uuid.UUID]{uuid.NewV7()},
			CreatedAtUs:   at.UnixMicro(),
		}
	}
	base := params(uuid.Nil())
	batched, baseline := base, base
	batched.RequestID, baseline.RequestID = uuid.NewV7(), uuid.NewV7()
	var before, after time.Time
	if err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		if err := tx.QueryRow(t.Context(), "SELECT clock_timestamp()").Scan(&before); err != nil {
			return err
		}
		if err := insertBatch(t.Context(), tx, []quotedReservation{{params: batched}}); err != nil {
			return err
		}
		if err := writeHistoricReservation(t.Context(), tx, baseline); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), "SELECT clock_timestamp()").Scan(&after)
	}); err != nil {
		t.Fatal(err)
	}
	facts := reservationFacts{
		batchedID: batched.RequestID, historicID: baseline.RequestID, at: at,
		before: before, after: after, lifetime: lifetime,
	}
	batchedRow, historicRow := readReservationRow(t, b, batched.RequestID), readReservationRow(t, b, baseline.RequestID)
	compareReservationRows(t, b, batchedRow, historicRow, facts)
	b.consistent()
}

// reservationFacts is what the column checks need beyond the two values: the IDs of the inserted rows, the call start
// the batch wrote, and the database clock on both sides of the two inserts.
type reservationFacts struct {
	batchedID, historicID uuid.UUID
	at                    time.Time
	before, after         time.Time
	lifetime              time.Duration
}

// reservationCheck compares one column of the two rows, returning "" when they agree or what differs.
type reservationCheck func(batched, historic jsonx.Raw, facts reservationFacts) string

// reservationChecks handles the columns the two inserts cannot fill identically. compareReservationRows compares every
// other column exactly, so a column a migration adds is compared even before it gets a check here.
var reservationChecks = map[string]reservationCheck{
	"request_id": reservationRequestID, "created_at": reservationCreatedAt, "deadline": reservationDeadline,
}

// compareReservationRows checks every column the table has, not a list the test keeps, against the two rows.
func compareReservationRows(t *testing.T, b *bank, batched, historic map[string]jsonx.Raw, facts reservationFacts) {
	t.Helper()
	for _, column := range reservationColumns(t, b) {
		check, ok := reservationChecks[column]
		if !ok {
			check = reservationValuesEqual
		}
		if diff := check(batched[column], historic[column], facts); diff != "" {
			t.Errorf("%s: %s", column, diff)
		}
	}
}

// reservationValuesEqual is the check for a column both writes must fill identically.
func reservationValuesEqual(batched, historic jsonx.Raw, _ reservationFacts) string {
	if string(batched) == string(historic) {
		return ""
	}
	return "the batch wrote " + string(batched) + ", the historic insert wrote " + string(historic)
}

// reservationRequestID checks that each row carries the ID it was inserted with, which the comparison cannot share
// with the other row.
func reservationRequestID(batched, historic jsonx.Raw, facts reservationFacts) string {
	wantBatched, wantHistoric := fmt.Sprintf("%q", facts.batchedID), fmt.Sprintf("%q", facts.historicID)
	if string(batched) == wantBatched && string(historic) == wantHistoric {
		return ""
	}
	return fmt.Sprintf("the batch wrote %s, the historic insert wrote %s; want %s and %s",
		batched, historic, wantBatched, wantHistoric)
}

// reservationCreatedAt checks the documented difference: the batch writes the call's start, while the historic insert
// let created_at default to the statement's clock, which must lie between the readings around both inserts.
func reservationCreatedAt(batched, historic jsonx.Raw, facts reservationFacts) string {
	batchAt, historicAt, diff := reservationTimes(batched, historic)
	if diff != "" {
		return diff
	}
	if !batchAt.Equal(facts.at) {
		return fmt.Sprintf("the batch wrote %s, want the call start %s", batchAt, facts.at)
	}
	if historicAt.Before(facts.before) || historicAt.After(facts.after) {
		return fmt.Sprintf("the historic insert wrote %s, outside the inserts' clock %s to %s",
			historicAt, facts.before, facts.after)
	}
	return ""
}

// reservationDeadline checks that both writes added the same lifetime to their statement's clock: each deadline must
// lie in the insert window plus the lifetime, and the two deadlines can differ by no more than that window.
func reservationDeadline(batched, historic jsonx.Raw, facts reservationFacts) string {
	batchDeadline, historicDeadline, diff := reservationTimes(batched, historic)
	if diff != "" {
		return diff
	}
	earliest, latest := facts.before.Add(facts.lifetime), facts.after.Add(facts.lifetime)
	if batchDeadline.Before(earliest) || batchDeadline.After(latest) {
		return fmt.Sprintf("the batch wrote %s, outside %s to %s, the insert clock plus %s",
			batchDeadline, earliest, latest, facts.lifetime)
	}
	if historicDeadline.Before(earliest) || historicDeadline.After(latest) {
		return fmt.Sprintf("the historic insert wrote %s, outside %s to %s, the insert clock plus %s",
			historicDeadline, earliest, latest, facts.lifetime)
	}
	if diff, window := batchDeadline.Sub(historicDeadline), facts.after.Sub(facts.before); diff < -window || diff > window {
		return fmt.Sprintf("the deadlines differ by %s, more than the inserts' clock window %s", diff, window)
	}
	return ""
}

// reservationTimes reads both rows' timestamp JSON values, naming the row that failed to parse.
func reservationTimes(batched, historic jsonx.Raw) (time.Time, time.Time, string) {
	var batchAt, historicAt time.Time
	if err := jsonx.Unmarshal(batched, &batchAt); err != nil {
		return time.Time{}, time.Time{}, "the batch's timestamp: " + err.Error()
	}
	if err := jsonx.Unmarshal(historic, &historicAt); err != nil {
		return time.Time{}, time.Time{}, "the historic timestamp: " + err.Error()
	}
	return batchAt, historicAt, ""
}

// reservationColumns reads billing.reservations' column names in table order.
func reservationColumns(t *testing.T, b *bank) []string {
	t.Helper()
	var columns []string
	if err := b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT column_name::text FROM information_schema.columns
			WHERE table_schema = 'billing' AND table_name = 'reservations' ORDER BY ordinal_position`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				return err
			}
			columns = append(columns, column)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return columns
}

// readReservationRow reads one reservation as the JSON of its whole row, keeping every column's value exact.
func readReservationRow(t *testing.T, b *bank, id uuid.UUID) map[string]jsonx.Raw {
	t.Helper()
	var raw []byte
	if err := b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT to_jsonb(r) FROM billing.reservations r WHERE request_id = $1`, id).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	var row map[string]jsonx.Raw
	if err := jsonx.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	return row
}

// TestReserveManyWritesTheCallStart: the caller's start, before admission and before the call log's write, is the
// reservation's created_at, so a window cannot count the call and miss its money by the admission's latency.
func TestReserveManyWritesTheCallStart(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1e6, 0, Config{})
	at := time.Date(2026, 10, 31, 11, 59, 59, 990_000_000, time.UTC)
	id := uuid.NewV7()
	reqs := []ReserveRequest{{Admitted: b.admitted, ID: id, At: at, Demand: Demand{TextBytes: 100}, Quotables: []Quotable{b.exact}}}
	if err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		_, _, _, err = b.billing.ReserveMany(t.Context(), tx, reqs)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var got time.Time
	if err := b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT created_at FROM billing.reservations WHERE request_id = $1", id).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	if !got.Equal(at) {
		t.Errorf("created_at = %s, want the call's start %s", got, at)
	}
}

// TestReserveManyRollsBackOnAWriteFailure: a database error in the insert pipeline takes the whole batch down; no
// request's reservation is left behind, so the caller may retry all of them.
func TestReserveManyRollsBackOnAWriteFailure(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1e6, 0, Config{})
	id := uuid.NewV7()
	reqs := []ReserveRequest{
		{Admitted: b.admitted, ID: uuid.NewV7(), Demand: Demand{TextBytes: 100}, Quotables: []Quotable{b.exact}},
		{Admitted: b.admitted, ID: id, Demand: Demand{TextBytes: 100}, Quotables: []Quotable{b.exact}},
		{Admitted: b.admitted, ID: id, Demand: Demand{TextBytes: 100}, Quotables: []Quotable{b.exact}},
	}
	err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		_, _, _, err := b.billing.ReserveMany(t.Context(), tx, reqs)
		return err
	})
	if err == nil {
		t.Fatal("the duplicate request ID was written twice")
	}
	if n := b.count("SELECT count(*) FROM billing.reservations"); n != 0 {
		t.Errorf("%d reservations left after the rollback", n)
	}
	if n := b.count("SELECT reserved FROM billing.balances"); n != 0 {
		t.Errorf("%d reserved after the rollback", n)
	}
	b.consistent()
}

// TestRunBatchSettlesInOneTransaction: the completion batch writes several settlements in one transaction and runs
// their reports after it commits; each request charges what it would alone.
func TestRunBatchSettlesInOneTransaction(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1e6, 0, Config{})
	holds := []*Hold{b.holdOn(metered(t), 100), b.holdOn(metered(t), 100), b.holdOn(metered(t), 100)}
	o := outcome(Committed, FinishStop, used(t, 50, 20, 0))
	// The first settles as a request does; it is the amount the batch must match.
	alone, err := b.billing.Settle(t.Context(), holds[0], o)
	if err != nil {
		t.Fatal(err)
	}
	finals := make([]Final, 3)
	afters := make([]func(), 3)
	err = b.billing.RunBatch(t.Context(), b.admitted.Scope, []func(pg.Tx) error{
		func(tx pg.Tx) error {
			var err error
			finals[1], afters[1], err = b.billing.SettleIn(t.Context(), tx, holds[1], o)
			return err
		},
		func(tx pg.Tx) error {
			var err error
			finals[2], afters[2], err = b.billing.SettleIn(t.Context(), tx, holds[2], o)
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 3; i++ {
		afters[i]()
		if finals[i].Charged != alone.Charged {
			t.Errorf("request %d charged %v, alone %v", i, finals[i].Charged, alone.Charged)
		}
	}
	if n := b.count("SELECT count(*) FROM billing.transactions WHERE kind = 'settle'"); n != 3 {
		t.Errorf("%d settlements booked, want 3", n)
	}
	b.balanced()
	b.consistent()
}

// TestRunBatchRollsBackAndARetryChargesOnce: a work's error rolls the batch back, no report runs, and retrying every
// request charges each exactly once.
func TestRunBatchRollsBackAndARetryChargesOnce(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1e6, 0, Config{})
	holds := []*Hold{b.holdOn(metered(t), 100), b.holdOn(metered(t), 100)}
	o := outcome(Committed, FinishStop, used(t, 50, 20, 0))
	boom := errors.New("boom")
	err := b.billing.RunBatch(t.Context(), b.admitted.Scope, []func(pg.Tx) error{
		func(tx pg.Tx) error {
			_, after, err := b.billing.SettleIn(t.Context(), tx, holds[0], o)
			if err != nil {
				return err
			}
			_ = after // a batch that does not commit runs no report
			return nil
		},
		func(tx pg.Tx) error {
			if _, _, err := b.billing.SettleIn(t.Context(), tx, holds[1], o); err != nil {
				return err
			}
			return boom
		},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("RunBatch = %v, want the failing work's error", err)
	}
	var failed *BatchWorkError
	if !errors.As(err, &failed) || failed.Index != 1 {
		t.Fatalf("RunBatch = %v (%T), want work 1 named", err, err)
	}
	if n := b.count("SELECT count(*) FROM billing.reservations WHERE state <> 'pending'"); n != 0 {
		t.Errorf("%d reservations ended by the rolled-back batch", n)
	}
	for _, h := range holds {
		if _, err := b.billing.Settle(t.Context(), h, o); err != nil {
			t.Fatalf("retry: %v", err)
		}
	}
	if n := b.count("SELECT sum(charged) FROM billing.reservations"); n != 140 {
		t.Errorf("charged %d in total, want 140", n)
	}
	b.balanced()
	b.consistent()
}

// reserveOnePlan reserves 100 for b and returns the hold, failing the test if the batch refused it.
func reserveOnePlan(t *testing.T, b *bank) *Hold {
	t.Helper()
	holds, errs, err := b.reserveMany(b.admitted, []int64{100})
	if err != nil || errs[0] != nil {
		t.Fatalf("ReserveMany = %v, %v", errs[0], err)
	}
	return holds[0]
}

// TestReserveManyPlanBoundaries: admission passes a window or a term at the database's whole clock, not at the UTC day
// the counters are bucketed by. A window that ended at 10:00 must not keep funding requests after 10:00 merely because
// both times fall on the same date.
func TestReserveManyPlanBoundaries(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	if now.Sub(now.Truncate(24*time.Hour)) < 2*time.Second {
		t.Skip("the test needs a moment past UTC midnight to tell the day from the whole timestamp")
	}
	t.Run("a window ended after midnight is passed before quoting", reservePlanWindowBoundary)
	t.Run("an ended term is expired before quoting", reservePlanTermBoundary)
}

func reservePlanWindowBoundary(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, Config{})
	sub := b.subscribe(b.makePlan(300, 7200), uuid.NewV7())
	b.sql("UPDATE billing.subscriptions SET starts_at = date_trunc('day', clock_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' WHERE id = '"+sub.ID.String()+"'",
		"UPDATE billing.subscription_windows SET starts_at = date_trunc('day', clock_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', "+
			"ends_at = date_trunc('day', clock_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '1 second', used = 300 "+
			"WHERE subscription_id = '"+sub.ID.String()+"' AND seq = 1")
	h := reserveOnePlan(t, b)
	if h.funding != "plan" || h.windowSeq != 2 {
		t.Fatalf("hold funds from %q window %d, want the plan's second window", h.funding, h.windowSeq)
	}
	if w := b.window(2); w.Reserved != 100 {
		t.Errorf("window 2 reserves %d, want the whole 100", w.Reserved)
	}
	b.consistent()
}

func reservePlanTermBoundary(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, 0, Config{})
	sub := b.subscribe(b.makePlan(300, 3600), uuid.NewV7())
	b.sql("UPDATE billing.subscriptions SET starts_at = date_trunc('day', clock_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', " +
		"ends_at = date_trunc('day', clock_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '1 second' WHERE id = '" + sub.ID.String() + "'")
	h := reserveOnePlan(t, b)
	if h.funding != "wallet" {
		t.Fatalf("hold funds from %q, want the wallet after the term ended", h.funding)
	}
	if state := b.subscribed().State; state != "expired" {
		t.Errorf("subscription state %q, want expired", state)
	}
	if w := b.window(1); w.Reserved != 0 || w.Used != 0 {
		t.Errorf("window 1 is %+v, want nothing reserved or used", w)
	}
	b.consistent()
}
