package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// ReserveRequest is one call's reservation in a batch: what admission read, the request ID its rows are keyed by, and
// the route entries its quote may keep.
type ReserveRequest struct {
	Admitted  Admitted
	ID        uuid.UUID
	Demand    Demand
	Quotables []Quotable
}

// ReserveMany reserves for every request in one transaction, under one lock of the account's balance row
// (README.md#同一账户的合并预留). Business refusals are per request: an unaffordable or unpriced one gets its error
// and the rest still reserve, because none of the refused request's rows were written. A database error returns an
// error for the whole batch and the transaction rolls back; the caller may retry every request, since each row is
// keyed by its request ID.
//
// The lock is taken once. The counters are read once per request, in statements pipelined behind the lock, and each
// quote sees the reservation amounts the earlier requests in the batch would add, so the batch cannot reserve more
// than the account may hold. The inserts are pipelined too: one round trip writes every reservation.
func (b *Billing) ReserveMany(ctx context.Context, tx pg.Tx, reqs []ReserveRequest) ([]*Hold, [][]int, []error, error) {
	holds := make([]*Hold, len(reqs))
	kept := make([][]int, len(reqs))
	errs := make([]error, len(reqs))
	if len(reqs) == 0 {
		return holds, kept, errs, nil
	}
	account := reqs[0].Admitted.Account
	for _, r := range reqs[1:] {
		if r.Admitted.Account != account {
			return nil, nil, nil, fmt.Errorf("billing: a reservation batch mixes accounts %s and %s", account, r.Admitted.Account)
		}
	}
	src, err := b.lockBatch(ctx, tx, account, reqs)
	var refused error
	switch {
	case errors.Is(err, errNoBalance):
		refused = &Insufficient{Source: SourceBalance}
	case errors.Is(err, ErrTerminated):
		refused = ErrTerminated
	case err != nil:
		return nil, nil, nil, err
	}
	if refused != nil {
		for i := range errs {
			errs[i] = refused
		}
		return holds, kept, errs, nil
	}
	usage, err := readBatchUsage(ctx, tx, reqs, src.day)
	if err != nil {
		return nil, nil, nil, err
	}
	quoted := b.quoteBatch(reqs, src, usage, errs)
	if err := insertBatch(ctx, tx, quoted); err != nil {
		return nil, nil, nil, err
	}
	for _, p := range quoted {
		holds[p.at] = &Hold{Reservation: p.quote, id: reqs[p.at].ID, admitted: reqs[p.at].Admitted, workspaceWindow: src.day, keyWindow: src.day}
		kept[p.at] = p.quote.Kept
	}
	return holds, kept, errs, nil
}

// batchSources is what a batch reads under its account's lock, once for every request in it.
type batchSources struct {
	balance LockBalanceRow
	day     time.Time
	found   map[string]fallbacks
	bodies  map[string]string
}

// lockBatch reads the fallbacks every request needs, then locks the balance, checks the fence and reads the clock.
// The fallbacks are platform data. The lock, the fence and the clock follow them in the same pipeline: the usage
// statements after it start after the lock, so their snapshot sees every change committed before it.
func (b *Billing) lockBatch(ctx context.Context, tx pg.Tx, account uuid.UUID, reqs []ReserveRequest) (batchSources, error) {
	src := batchSources{found: map[string]fallbacks{}, bodies: map[string]string{}}
	err := tx.Batch(ctx, []pg.Statement{
		{SQL: readFallbacks, Args: []any{batchCurrencies(reqs)}},
		{SQL: lockBalance, Args: []any{account}},
		{SQL: fenceHolder, Args: []any{b.holder}},
		{SQL: now, Args: nil},
	}, func(batch pg.BatchResults) error {
		read, err := scanFallbacks(batch)
		if err != nil {
			return err
		}
		if err := parseFallbackRows(read, src.found, src.bodies); err != nil {
			return err
		}
		bal := &src.balance
		if err := batch.QueryRow().Scan(&bal.Balance, &bal.Floor, &bal.Reserved, &bal.Frozen); err != nil {
			return balanceErr(err)
		}
		var holder uuid.UUID
		if err := batch.QueryRow().Scan(&holder); err != nil {
			return fenceErr(err)
		}
		if err := batch.QueryRow().Scan(&src.day); err != nil {
			return fmt.Errorf("billing: read the clock: %w", err)
		}
		src.day = src.day.UTC().Truncate(24 * time.Hour) // the windows are UTC days, as ReadUsage makes them
		return nil
	})
	return src, err
}

// batchCurrencies is the currencies whose fallback prices any request of the batch needs, each once.
func batchCurrencies(reqs []ReserveRequest) []string {
	currencies := []string{}
	for _, r := range reqs {
		for _, c := range fallbackCurrencies(r.Quotables) {
			if !slices.Contains(currencies, c) {
				currencies = append(currencies, c)
			}
		}
	}
	return currencies
}

func scanFallbacks(batch pg.BatchResults) ([]ReadFallbacksRow, error) {
	rows, err := batch.Query()
	if err != nil {
		return nil, fmt.Errorf("billing: read the fallback prices: %w", err)
	}
	defer rows.Close()
	var read []ReadFallbacksRow
	for rows.Next() {
		var r ReadFallbacksRow
		if err := rows.Scan(&r.Currency, &r.Body); err != nil {
			return nil, fmt.Errorf("billing: read the fallback prices: %w", err)
		}
		read = append(read, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("billing: read the fallback prices: %w", err)
	}
	return read, nil
}

// readBatchUsage reads every request's counters in separate statements after the lock, in one pipeline. They all use
// day, so a batch that crosses midnight still checks and books every request in one window.
func readBatchUsage(ctx context.Context, tx pg.Tx, reqs []ReserveRequest, day time.Time) ([]ReadUsageRow, error) {
	usages := make([]pg.Statement, len(reqs))
	for i, r := range reqs {
		p := r.Admitted.usageParams(&day)
		usages[i] = pg.Statement{SQL: readUsage, Args: []any{p.WorkspaceID, p.WorkspaceReset, p.KeyID, p.KeyReset, p.Day}}
	}
	usage := make([]ReadUsageRow, len(reqs))
	err := tx.Batch(ctx, usages, func(batch pg.BatchResults) error {
		for i := range usage {
			if err := batch.QueryRow().Scan(&usage[i].Day, &usage[i].WorkspaceUsed, &usage[i].KeyUsed); err != nil {
				return fmt.Errorf("billing: read the counters: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return usage, nil
}

// quotedReservation is a request of the batch whose quote passed, with the row its reservation writes.
type quotedReservation struct {
	at     int
	params InsertReservationParams
	quote  Reservation
}

// quoteBatch quotes the requests in order. Each quote sees the amounts the earlier quoted requests add to the balance's
// reserved total and to their workspace's and key's counters, so the batch cannot reserve more than the account may
// hold. A refused request gets its error in errs and adds nothing.
func (b *Billing) quoteBatch(reqs []ReserveRequest, src batchSources, usage []ReadUsageRow, errs []error) []quotedReservation {
	reserved := src.balance.Reserved
	workspaceUsed, keyUsed := map[uuid.UUID]int64{}, map[uuid.UUID]int64{}
	quoted := make([]quotedReservation, 0, len(reqs))
	for i, r := range reqs {
		a := r.Admitted
		bal := src.balance
		bal.Reserved = reserved
		avail := a.available(bal,
			addClamp(usage[i].WorkspaceUsed, workspaceUsed[a.Workspace]),
			addClamp(usage[i].KeyUsed, keyUsed[a.Key]))
		q, err := b.quoteOne(r, src, avail)
		if err != nil {
			errs[i] = err
			continue
		}
		q.at = i
		amount := q.quote.Amount.CeilAmount()
		reserved = addClamp(reserved, amount)
		workspaceUsed[a.Workspace] = addClamp(workspaceUsed[a.Workspace], amount)
		keyUsed[a.Key] = addClamp(keyUsed[a.Key], amount)
		quoted = append(quoted, q)
	}
	return quoted
}

func (b *Billing) quoteOne(r ReserveRequest, src batchSources, avail []Available) (quotedReservation, error) {
	q, locked, err := fillFallbacks(r.Quotables, src.found, src.bodies)
	if err != nil {
		return quotedReservation{}, err
	}
	quote, err := Quote(r.Demand, q, b.config.Segments, avail)
	if insufficient, ok := errors.AsType[*Insufficient](err); ok {
		return quotedReservation{}, r.Admitted.transient(insufficient)
	} else if err != nil {
		return quotedReservation{}, err
	}
	p, err := b.reservationParams(r.Admitted, r.ID, q, quote, locked, src.day)
	if err != nil {
		return quotedReservation{}, err
	}
	return quotedReservation{params: p, quote: quote}, nil
}

// insertBatch writes every quoted reservation in one pipelined round trip.
func insertBatch(ctx context.Context, tx pg.Tx, quoted []quotedReservation) error {
	if len(quoted) == 0 {
		return nil
	}
	statements := make([]pg.Statement, len(quoted))
	for i, p := range quoted {
		statements[i] = pg.Statement{SQL: insertReservation, Args: insertReservationArgs(p.params)}
	}
	return tx.Batch(ctx, statements, func(batch pg.BatchResults) error {
		for range statements {
			if _, err := batch.Exec(); err != nil {
				return fmt.Errorf("billing: write the reservation: %w", err)
			}
		}
		return nil
	})
}

// insertReservationArgs passes InsertReservation's parameters in the order its generated function does. The insert is
// pipelined, so it cannot go through that function; the batch reservation test compares both paths' rows.
func insertReservationArgs(p InsertReservationParams) []any {
	return []any{
		p.WorkspaceID, p.WorkspaceWindow, p.SiteID, p.AccountID, p.Amount, p.KeyID, p.KeyWindow, p.RequestID,
		p.HolderID, p.LifetimeSeconds, p.Prices, p.Fallbacks, p.Models, p.Endpoints, p.ExchangeRates, p.PriceVersions,
	}
}

func addClamp(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}
