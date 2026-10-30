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

	currencies := []string{}
	for _, r := range reqs {
		for _, c := range fallbackCurrencies(r.Quotables) {
			if !slices.Contains(currencies, c) {
				currencies = append(currencies, c)
			}
		}
	}
	// The fallbacks are platform data. The lock, the fence and the clock follow them in the same pipeline: the usage
	// statements below start after the lock, so their snapshot sees every change committed before it.
	var (
		balance LockBalanceRow
		day     time.Time
		found   map[string]fallbacks
		bodies  map[string]string
	)
	err := tx.Batch(ctx, []pg.Statement{
		{SQL: readFallbacks, Args: []any{currencies}},
		{SQL: lockBalance, Args: []any{account}},
		{SQL: fenceHolder, Args: []any{b.holder}},
		{SQL: now, Args: nil},
	}, func(batch pg.BatchResults) error {
		rows, err := batch.Query()
		if err != nil {
			return fmt.Errorf("billing: read the fallback prices: %w", err)
		}
		var read []ReadFallbacksRow
		for rows.Next() {
			var r ReadFallbacksRow
			if err := rows.Scan(&r.Currency, &r.Body); err != nil {
				rows.Close()
				return fmt.Errorf("billing: read the fallback prices: %w", err)
			}
			read = append(read, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("billing: read the fallback prices: %w", err)
		}
		if found, bodies, err = parseFallbackRows(read); err != nil {
			return err
		}
		if err := batch.QueryRow().Scan(&balance.Balance, &balance.Floor, &balance.Reserved, &balance.Frozen); err != nil {
			return balanceErr(err)
		}
		var holder uuid.UUID
		if err := batch.QueryRow().Scan(&holder); err != nil {
			return fenceErr(err)
		}
		if err := batch.QueryRow().Scan(&day); err != nil {
			return fmt.Errorf("billing: read the clock: %w", err)
		}
		day = day.UTC().Truncate(24 * time.Hour) // the windows are UTC days, as ReadUsage makes them
		return nil
	})
	switch {
	case errors.Is(err, errNoBalance):
		for i := range errs {
			errs[i] = &Insufficient{Source: SourceBalance}
		}
		return holds, kept, errs, nil
	case errors.Is(err, ErrTerminated):
		for i := range errs {
			errs[i] = ErrTerminated
		}
		return holds, kept, errs, nil
	case err != nil:
		return nil, nil, nil, err
	}

	// The usage reads are separate statements after the lock, in one pipeline. They all use day, so a batch that
	// crosses midnight still checks and books every request in one window.
	usages := make([]pg.Statement, len(reqs))
	for i, r := range reqs {
		p := r.Admitted.usageParams(&day)
		usages[i] = pg.Statement{SQL: readUsage, Args: []any{p.WorkspaceID, p.WorkspaceReset, p.KeyID, p.KeyReset, p.Day}}
	}
	usage := make([]ReadUsageRow, len(reqs))
	if err := tx.Batch(ctx, usages, func(batch pg.BatchResults) error {
		for i := range usage {
			if err := batch.QueryRow().Scan(&usage[i].Day, &usage[i].WorkspaceUsed, &usage[i].KeyUsed); err != nil {
				return fmt.Errorf("billing: read the counters: %w", err)
			}
		}
		return nil
	}); err != nil {
		return nil, nil, nil, err
	}

	type pending struct {
		at     int
		params InsertReservationParams
		quote  Reservation
	}
	reserved := balance.Reserved
	workspaceUsed, keyUsed := map[uuid.UUID]int64{}, map[uuid.UUID]int64{}
	quoted := make([]pending, 0, len(reqs))
	for i, r := range reqs {
		a := r.Admitted
		q, locked, err := fillFallbacks(r.Quotables, found, bodies)
		if err != nil {
			errs[i] = err
			continue
		}
		bal := balance
		bal.Reserved = reserved
		avail := a.available(bal,
			addClamp(usage[i].WorkspaceUsed, workspaceUsed[a.Workspace]),
			addClamp(usage[i].KeyUsed, keyUsed[a.Key]))
		quote, err := Quote(r.Demand, q, b.config.Segments, avail)
		if insufficient, ok := errors.AsType[*Insufficient](err); ok {
			errs[i] = a.transient(insufficient)
			continue
		} else if err != nil {
			errs[i] = err
			continue
		}
		p, err := b.reservationParams(a, r.ID, q, quote, locked, day)
		if err != nil {
			errs[i] = err
			continue
		}
		amount := quote.Amount.CeilAmount()
		reserved = addClamp(reserved, amount)
		workspaceUsed[a.Workspace] = addClamp(workspaceUsed[a.Workspace], amount)
		keyUsed[a.Key] = addClamp(keyUsed[a.Key], amount)
		quoted = append(quoted, pending{at: i, params: p, quote: quote})
	}

	if len(quoted) > 0 {
		statements := make([]pg.Statement, len(quoted))
		for i, p := range quoted {
			statements[i] = pg.Statement{SQL: insertReservation, Args: insertReservationArgs(p.params)}
		}
		if err := tx.Batch(ctx, statements, func(batch pg.BatchResults) error {
			for range statements {
				if _, err := batch.Exec(); err != nil {
					return fmt.Errorf("billing: write the reservation: %w", err)
				}
			}
			return nil
		}); err != nil {
			return nil, nil, nil, err
		}
		for _, p := range quoted {
			a := reqs[p.at].Admitted
			holds[p.at] = &Hold{Reservation: p.quote, id: reqs[p.at].ID, admitted: a, workspaceWindow: day, keyWindow: day}
			kept[p.at] = p.quote.Kept
		}
	}
	return holds, kept, errs, nil
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
