package billing

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// Suppliers is what billing needs of suppliers, the narrowest part of internal/suppliers/README.md「接口（草图）」:
// writing a request's attempts and lost requests in billing's transaction, and reading its reconciliation journal.
// suppliers implements it once its call surface and journal exist; until then tests use a double.
type Suppliers interface {
	RecordAttempts(ctx context.Context, tx pg.Tx, request uuid.UUID, attempts []AttemptRecord) error
	// CommittedAttempt is the attempt the journal marks committed, with its reconciliation key, if any.
	CommittedAttempt(ctx context.Context, tx pg.Tx, request uuid.UUID) (JournalAttempt, bool, error)
	RecordLostRequest(ctx context.Context, tx pg.Tx, request uuid.UUID) error
	ResolveLostRequest(ctx context.Context, tx pg.Tx, request uuid.UUID) error
}

// AttemptRecord is one attempt as suppliers stores it; Usage is nil when cleanup wrote it from the journal.
type AttemptRecord struct {
	Seq, Entry int
	Key        string
	Usage      []catalog.Usage
}

// JournalAttempt is a committed attempt as the journal recorded it.
type JournalAttempt struct {
	Seq, Entry int
	Key        string
}

// Outcome is how a call ended: its attempts in order, of which the last is charged, and whether the client cancelled.
type Outcome struct {
	Attempts  []AttemptResult
	Cancelled bool
}

// AttemptResult is one attempt: the route entry it bound, its reconciliation key, its cost to the platform in
// billionths of a credit (suppliers' Cost estimates it), its usage by sampling, and how it ended.
type AttemptResult struct {
	Seq, Entry int
	Key        string
	Cost       int64
	Segments   []Segment
	Result     Result
	Finish     FinishReason
}

// Segment is the usage of one sampling and which of its items the upstream reported; the rest were estimated.
type Segment struct {
	Usage    catalog.Usage
	Reported catalog.ItemSet
}

// Result is what reached the client of an attempt (docs/domains/billing.md「免单规则」). The zero value is the one
// that is charged in full, so a forgotten result never waives a charge.
type Result uint8

// The results.
const (
	Committed          Result = iota // content reached the client
	Aggregated                       // non-streaming, its content returned with the response or the error
	FailedBeforeCommit               // a 2xx that failed before anything was forwarded
	Unknown                          // written, then no headers or usage, or a status that is not a refusal
	NotExecuted                      // known not to have run: not fully written, or refused before execution
)

// FinishReason is why the last attempt stopped; the zero value is none reported.
type FinishReason uint8

// The finish reasons.
const (
	FinishNone FinishReason = iota
	FinishStop
	FinishLength
	FinishToolUse
	FinishRefusal
	FinishError
	FinishTruncated
	FinishTimeout
	FinishCancelled
)

// Final is what the call ends with: the charged attempt's usage, what the user was charged (capped at the floor) and
// whether any of it was estimated.
type Final struct {
	Segments  []Segment
	Charged   platform.Exact
	Estimated bool
}

// ErrNoAttempt is a Settle whose outcome names no attempt on a kept entry: nothing can price it.
var ErrNoAttempt = errors.New("billing: the outcome names no attempt on a kept entry")

// Settle charges the call's last attempt and ends its reservation, in one transaction of the admission's scope
// (README.md「追加与结算」): first what needs no lock, the attempts and their costs, then under the balance row lock
// the charge by 免单规则, capped at the floor with the platform bearing the rest, the counters, and the reservation's
// state. A retry, or a Settle after cleanup took over, charges at most once: it returns the Final already stored, or
// settles the difference by the late-settlement rules.
func (b *Billing) Settle(ctx context.Context, h *Hold, o Outcome) (f Final, err error) {
	if len(o.Attempts) == 0 {
		return Final{}, ErrNoAttempt
	}
	a, last := h.admitted, o.Attempts[len(o.Attempts)-1]
	err = b.db.Tx(ctx, a.Scope, func(tx pg.Tx) error {
		if err := b.recordAttempts(ctx, tx, h.id, a.Site, a.Account, o); err != nil {
			return err
		}
		balance, err := lockAccount(ctx, tx, a.Account)
		if err != nil {
			return err
		}
		r, err := queries.LockReservation(ctx, tx, h.id)
		if err != nil {
			return fmt.Errorf("billing: read the reservation: %w", err)
		}
		f, err = b.settle(ctx, tx, h.id, balance, r, o, last)
		return err
	})
	return f, err
}

func (b *Billing) recordAttempts(ctx context.Context, tx pg.Tx, id, site, account uuid.UUID, o Outcome) error {
	records := make([]AttemptRecord, len(o.Attempts))
	for i, at := range o.Attempts {
		records[i] = AttemptRecord{Seq: at.Seq, Entry: at.Entry, Key: at.Key, Usage: usages(at.Segments)}
		if _, err := post(ctx, tx, event{site: site, account: account, kind: "attempt_cost", request: id, seq: int32(at.Seq)}, //nolint:gosec // G115: at most a few attempts
			line{bookSupplierCost, at.Cost}, line{bookSupplierPrepaid, -at.Cost}); err != nil {
			return err
		}
	}
	if err := b.suppliers.RecordAttempts(ctx, tx, id, records); err != nil {
		return fmt.Errorf("billing: record the attempts: %w", err)
	}
	return nil
}

// settle branches on what the reservation became (README.md「预留的状态」).
func (b *Billing) settle(ctx context.Context, tx pg.Tx, id uuid.UUID, balance LockBalanceRow, r LockReservationRow,
	o Outcome, last AttemptResult,
) (Final, error) {
	stored := Final{Segments: last.Segments, Charged: platform.AmountExact(r.Charged), Estimated: r.State == "estimated"}
	switch r.State {
	case "settled", "estimated", "released":
		return stored, nil
	case "cleanup_released":
		if err := b.suppliers.ResolveLostRequest(ctx, tx, id); err != nil {
			return Final{}, fmt.Errorf("billing: resolve the lost request: %w", err)
		}
	}
	price, estimated, err := b.price(ctx, tx, r, o, last)
	if err != nil {
		return Final{}, err
	}
	c := locked{id: id, row: r, balance: balance}
	switch r.State {
	case "pending", "cleanup_pending": // held: amount is still reserved
		err = c.end(ctx, tx, price, estimated, index(last.Entry), b.config.CorrectionWindow.Seconds())
	default: // cleanup_settled or cleanup_released: a late Settle replaces what cleanup charged, at revision -1
		err = c.late(ctx, tx, price, index(last.Entry))
	}
	return Final{Segments: last.Segments, Charged: platform.AmountExact(c.charged), Estimated: estimated}, err
}

// price is what 免单规则 charges for the outcome, at the locked price of the last attempt's entry, and whether it
// rests on estimated usage. A waivable call is waived while the account's daily cap allows its cost.
func (b *Billing) price(ctx context.Context, tx pg.Tx, r LockReservationRow, o Outcome, last AttemptResult) (int64, bool, error) {
	if last.Result == NotExecuted {
		return 0, false, nil
	}
	s, err := schedule(r.Prices, last.Entry)
	if err != nil {
		return 0, false, err
	}
	perUseOnly := false
	if waivable(o, last) {
		var cost int64
		for _, at := range o.Attempts {
			cost += at.Cost
		}
		waived, err := queries.AddWaiver(ctx, tx, AddWaiverParams{AccountID: r.AccountID, SiteID: r.SiteID, Cost: cost, Cap: b.config.WaiverCap})
		switch {
		case errors.Is(err, pg.ErrNoRows): // over the cap: charged by the zero-output waiver alone
			perUseOnly = zeroOutput(last)
		case err != nil:
			return 0, false, fmt.Errorf("billing: count the waiver: %w", err)
		default:
			perUseOnly = waived // true: the row was written
		}
	}
	return priceSegments(s, last.Segments, perUseOnly)
}

// waivable: a call the platform may not charge for its model usage. A cancelled call never is; one whose content
// never reached the client is; one that did is only when the upstream reported no output and an empty or error
// finish (the zero-output waiver).
func waivable(o Outcome, last AttemptResult) bool {
	delivered := last.Result == Committed || last.Result == Aggregated
	return !o.Cancelled && (!delivered || zeroOutput(last))
}

// zeroOutput is OpenRouter's zero-completion condition: reported usage with no output and reasoning, and an empty or
// error finish. Without reported usage nothing proves the output was zero.
func zeroOutput(last AttemptResult) bool {
	reported, output := false, int64(0)
	for _, s := range last.Segments {
		reported = reported || s.Reported != 0
		output += s.Usage.OutputTotal()
	}
	return reported && output == 0 && (last.Finish == FinishNone || last.Finish == FinishError)
}

// perUseItems are the items charged even when a call is waived (「按次计费的项目照常收费」).
const perUseItems catalog.ItemSet = 1<<catalog.ItemWebSearch | 1<<catalog.ItemCodeExecution

// priceSegments prices each sampling at its own band, sums them exactly and rounds up once. Items with usage the
// upstream did not report, or that the schedule cannot price, make the charge estimated: fallback prices for missing
// items are not built yet (docs/domains/billing.md「价目表缺项」), so they count 0 until the correction.
func priceSegments(s catalog.Schedule, segments []Segment, perUseOnly bool) (int64, bool, error) {
	total, estimated := platform.Exact{}, false
	for _, seg := range segments {
		u := seg.Usage
		if perUseOnly {
			var err error
			if u, err = only(u, perUseItems); err != nil {
				return 0, false, err
			}
		}
		cost, missing, err := s.Cost(u)
		if err == nil {
			total, err = total.Add(cost)
		}
		if err != nil {
			return 0, false, fmt.Errorf("billing: price the usage: %w", err)
		}
		for it := range catalog.NumItems {
			estimated = estimated || u.Count(it) > 0 && (!seg.Reported.Has(it) || missing.Has(it))
		}
	}
	return total.CeilAmount(), estimated, nil
}

func only(u catalog.Usage, items catalog.ItemSet) (catalog.Usage, error) {
	counts := map[catalog.Item]int64{}
	for it := range catalog.NumItems {
		if items.Has(it) {
			counts[it] = u.Count(it)
		}
	}
	return catalog.NewUsage(counts, u.ServiceTier(), nil)
}

func schedule(prices []string, entry int) (catalog.Schedule, error) {
	if entry < 0 || entry >= len(prices) || prices[entry] == "" {
		return catalog.Schedule{}, ErrNoAttempt
	}
	s, err := catalog.ParseSchedule([]byte(prices[entry]))
	if err != nil {
		return catalog.Schedule{}, fmt.Errorf("billing: read the locked price: %w", err)
	}
	return s, nil
}

func index(entry int) *int32 {
	i := int32(entry) //nolint:gosec // G115: a route has at most a few entries
	return &i
}

func usages(segments []Segment) []catalog.Usage {
	u := make([]catalog.Usage, len(segments))
	for i, s := range segments {
		u[i] = s.Usage
	}
	return u
}

// locked is one reservation under its balance row lock, with what is charged once a branch has run.
type locked struct {
	id      uuid.UUID
	row     LockReservationRow
	balance LockBalanceRow
	charged int64
}

// cap is what may be charged without passing the floor, keeping every other reservation covered: the balance less
// the floor, less what is reserved apart from what this reservation still holds.
func (c *locked) cap(held int64) int64 {
	return max(c.balance.Balance-c.balance.Floor-(c.balance.Reserved-held), 0)
}

func (c *locked) event(kind string, revision int64) event {
	return event{site: c.row.SiteID, account: c.row.AccountID, kind: kind, request: c.id, revision: revision}
}

// end settles a held reservation: price capped at the floor, the platform bearing the rest. An estimated charge, or
// one cleanup made, keeps what is left of the reservation for the correction window; otherwise it is released.
func (c *locked) end(ctx context.Context, tx pg.Tx, price int64, estimated bool, entryIndex *int32, holdSeconds float64) error {
	held := c.row.Amount - c.row.Charged
	c.charged = min(price, c.cap(held))
	if _, err := post(ctx, tx, c.event("settle", 0), charge(price, c.charged)...); err != nil {
		return err
	}
	state, keep := "settled", int64(0)
	if estimated {
		state, keep = "estimated", max(c.row.Amount-c.charged, 0)
	}
	if err := c.move(ctx, tx, keep-held, c.charged); err != nil {
		return err
	}
	return c.write(ctx, tx, state, entryIndex, price, holdSeconds)
}

// late replaces what cleanup charged with what the full outcome charges: a refund, or a makeup capped at the floor.
func (c *locked) late(ctx context.Context, tx pg.Tx, price int64, entryIndex *int32) error {
	c.charged = min(price, c.row.Charged+c.cap(0))
	if _, err := post(ctx, tx, c.event("reverse", lateRevision), reversal(c.row.Price, c.row.Charged)...); err != nil {
		return err
	}
	if _, err := post(ctx, tx, c.event("settle", lateRevision), charge(price, c.charged)...); err != nil {
		return err
	}
	if err := c.move(ctx, tx, 0, c.charged-c.row.Charged); err != nil {
		return err
	}
	return c.write(ctx, tx, "settled", entryIndex, price, 0)
}

func (c *locked) move(ctx context.Context, tx pg.Tx, reserved, spent int64) error {
	if err := queries.MoveCounters(ctx, tx, MoveCountersParams{
		Reserved: reserved, Spent: spent, AccountID: c.row.AccountID, WorkspaceID: c.row.WorkspaceID,
		WorkspaceWindow: c.row.WorkspaceWindow, KeyID: c.row.KeyID, KeyWindow: c.row.KeyWindow,
	}); err != nil {
		return fmt.Errorf("billing: move the counters: %w", err)
	}
	return nil
}

// write records the reservation's state and charge; a nil entry keeps the one recorded.
func (c *locked) write(ctx context.Context, tx pg.Tx, state string, entryIndex *int32, price int64, holdSeconds float64) error {
	if err := queries.EndReservation(ctx, tx, EndReservationParams{
		RequestID: c.id, State: state, Entry: entryIndex, Charged: c.charged, Price: price, HoldSeconds: holdSeconds,
	}); err != nil {
		return fmt.Errorf("billing: end the reservation: %w", err)
	}
	return nil
}
