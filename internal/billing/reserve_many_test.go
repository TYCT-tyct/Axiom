package billing

import (
	"errors"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

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

// TestReserveManyIsTheSerialOrder: a batch admits exactly what the same requests would one at a time in the same
// order, refusal for refusal, because each quote sees the amounts the earlier ones would reserve.
func TestReserveManyIsTheSerialOrder(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2))
	for trial := range 3 {
		amounts := make([]int64, 20)
		for i := range amounts {
			amounts[i] = r.Int64N(100) + 1
		}
		batch := newBank(t, 1000, -200, Config{})
		holds, errs, err := batch.reserveMany(batch.admitted, amounts)
		if err != nil {
			t.Fatal(err)
		}
		serial := newBank(t, 1000, -200, Config{})
		for i, amount := range amounts {
			h, err := serial.reserve(serial.admitted, amount)
			switch {
			case err != nil && errs[i] == nil:
				t.Fatalf("trial %d request %d: the batch admitted %d, the serial order refused it: %v", trial, i, amount, err)
			case err == nil && errs[i] != nil:
				t.Fatalf("trial %d request %d: the batch refused %d (%v), the serial order admitted it", trial, i, amount, errs[i])
			case err != nil:
				var batched, single *Insufficient
				if !errors.As(errs[i], &batched) || !errors.As(err, &single) || batched.Source != single.Source {
					t.Fatalf("trial %d request %d: batch refused %v, serial refused %v", trial, i, errs[i], err)
				}
			default:
				if holds[i] == nil || holds[i].Reservation.Amount.CeilAmount() != h.Reservation.Amount.CeilAmount() ||
					!slices.Equal(holds[i].Reservation.Kept, h.Reservation.Kept) {
					t.Fatalf("trial %d request %d: batch %+v, serial %+v", trial, i, holds[i], h)
				}
			}
		}
		batch.consistent()
	}
}

// TestReserveManyStorm: several batches reserve at once; the account never passes its floor, and every refusal was for
// more than what was left after all the reservations that succeeded.
func TestReserveManyStorm(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1000, -200, Config{})
	const capacity = 1200
	var mu sync.Mutex
	var wg sync.WaitGroup
	var total int64
	var refused []int64
	for range 4 {
		amounts := make([]int64, 10)
		for i := range amounts {
			amounts[i] = rand.Int64N(100) + 1 //nolint:gosec // test data
		}
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
				total += holds[i].Reservation.Amount.CeilAmount()
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

// TestReserveManyRowsMatchReserve: a batch's insert writes the same row as Reserve, field for field; the insert is
// pipelined and does not go through the generated function.
func TestReserveManyRowsMatchReserve(t *testing.T) {
	t.Parallel()
	b := newBank(t, 1e6, 0, Config{})
	single, err := b.reserve(b.admitted, 100)
	if err != nil {
		t.Fatal(err)
	}
	holds, errs, err := b.reserveMany(b.admitted, []int64{100})
	if err != nil || errs[0] != nil {
		t.Fatalf("batch: %v, %v", errs, err)
	}
	const columns = `amount, prices, fallbacks, models, endpoints, exchange_rates, price_versions, workspace_window, key_window, state`
	read := func(id uuid.UUID) string {
		var row string
		if err := b.app.Tx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT row(`+columns+`)::text FROM billing.reservations WHERE request_id = $1`, id).Scan(&row)
		}); err != nil {
			t.Fatal(err)
		}
		return row
	}
	if got, want := read(holds[0].id), read(single.id); got != want {
		t.Errorf("the batch wrote\n%s\nthe single reservation wrote\n%s", got, want)
	}
	b.consistent()
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
