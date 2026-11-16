package billing

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// Admitted is what billing needs of an admission: the tenant, the account's scope for the transactions that follow
// it, and the workspace's and key's limits as admission read them. It stands in for accounts.Admission until Admit
// exists; only accounts builds account scopes.
type Admitted struct {
	Scope                         pg.Scope
	Site, Account, Workspace, Key uuid.UUID
	WorkspaceBudget, KeyLimit     *Limit // nil when there is none
	// Preference is the account's funding order (docs/domains/billing.md「资金来源」); empty is subscription_first.
	Preference string
}

// Limit is a spending limit in billionths of a credit over a window.
type Limit struct {
	Amount int64
	Reset  Reset
}

// Reset is how often a limit starts over, in accounts' words.
type Reset string

// The resets; a window starts at the UTC day, week or month by the database's clock.
const (
	ResetNever   Reset = ""
	ResetDaily   Reset = "daily"
	ResetWeekly  Reset = "weekly"
	ResetMonthly Reset = "monthly"
)

// Hold is a committed reservation of one request: the quotes of the entries it kept, and where its counters are.
type Hold struct {
	Reservation
	id              uuid.UUID
	admitted        Admitted
	workspaceWindow time.Time
	keyWindow       time.Time
	funding         string    // "wallet" or "plan": where the hold is frozen (docs/domains/billing.md「订阅套餐」)
	subscription    uuid.UUID // the plan's subscription, zero for a wallet hold
	windowSeq       int32     // the window the plan hold is frozen in, zero for a wallet hold
}

// plan reports whether the hold is frozen in a subscription window rather than in the balance's reserved counter.
func (h *Hold) plan() bool { return h.funding == "plan" }

// lockedPrices are the kept entries' schedules in canonical JSON by route index, "" for a dropped entry: settlement and
// cleanup price usage with them, whichever process runs them.
func lockedPrices(q []Quotable, kept []int, fallbacks []string) ([]string, []string, error) {
	bodies := make([]string, len(q)*2)
	prices, locked := bodies[:len(q)], bodies[len(q):]
	for _, i := range kept {
		b, err := q[i].Price.MarshalJSON()
		if err != nil {
			return nil, nil, fmt.Errorf("billing: lock the price of entry %d: %w", i, err)
		}
		prices[i], locked[i] = string(b), fallbacks[i]
	}
	return prices, locked, nil
}

// OpenBalance creates an account's balance row with its floor (0 or below), as the account is opened. Without the
// row an account can reserve nothing.
func OpenBalance(ctx context.Context, tx pg.Tx, site, account uuid.UUID, floor int64) error {
	if err := queries.InsertBalance(ctx, tx, InsertBalanceParams{AccountID: account, SiteID: site, Floor: floor}); err != nil {
		return fmt.Errorf("billing: open the balance of %s: %w", account, err)
	}
	return nil
}

// reservationParams is the row a reservation writes: the prices it locked, the routes it kept, the counters' windows,
// and the call's start.
func (b *Billing) reservationParams(a Admitted, id uuid.UUID, q []Quotable, r Reservation, fallbacks []string, day, at time.Time) (InsertReservationAtParams, error) {
	if at.IsZero() {
		at = time.Now()
	}
	prices, locked, err := lockedPrices(q, r.Kept, fallbacks)
	if err != nil {
		return InsertReservationAtParams{}, err
	}
	identifiers := make([]uuid.UUID, len(q)*3)
	models, endpoints, versions := identifiers[:len(q)], identifiers[len(q):2*len(q)], identifiers[2*len(q):]
	for _, i := range r.Kept {
		versions[i] = q[i].Version
		models[i], endpoints[i] = uuid.UUID(q[i].Model.ID), q[i].Endpoint
	}
	rates := frozenRates(q, r.Kept)
	rateJSON, err := jsonx.MarshalCanonical(rates)
	if err != nil {
		return InsertReservationAtParams{}, fmt.Errorf("billing: freeze exchange rates: %w", err)
	}
	return InsertReservationAtParams{
		ExchangeRates: rateJSON, PriceVersions: versions,
		Prices: prices, Fallbacks: locked, Models: models, Endpoints: endpoints,
		RequestID: id, SiteID: a.Site, AccountID: a.Account, WorkspaceID: a.Workspace, KeyID: a.Key, HolderID: b.holder,
		WorkspaceWindow: day, KeyWindow: day, Amount: r.Amount.CeilAmount(),
		LifetimeSeconds: b.config.Lifetime.Seconds(), CreatedAtUs: at.UnixMicro(), Funding: "wallet",
	}, nil
}

// frozenRates keeps missing pairs too, so an absent rate cannot be filled with a later configuration.
func frozenRates(q []Quotable, kept []int) []platform.ExchangeRate {
	rates := []platform.ExchangeRate{}
	seen := map[[2]string]bool{}
	for _, i := range kept {
		r := q[i].Exchange
		pair := [2]string{r.Base, r.Quote}
		if r.Base != "" && !seen[pair] {
			rates = append(rates, r)
			seen[pair] = true
		}
	}
	return rates
}

// Extend makes h's reservation cover the cumulative target r.Target, in a transaction of its own (README.md「追加与结算」):
// it succeeds only when the amount reserved is at least the target once it commits. A target already covered, as
// on a retry whose acknowledgement was lost or by an earlier attempt's extensions, changes nothing; otherwise the
// difference is checked against every source and written with a compare and write on the segment read under the
// lock. The amount only grows. A reservation that ended, or a holder that was reaped, gives ErrTerminated; an
// increase a source cannot afford gives *Insufficient.
func (b *Billing) Extend(ctx context.Context, h *Hold, r ExtendRequest) error {
	return b.db.Tx(ctx, h.admitted.Scope, func(tx pg.Tx) error { return b.ExtendIn(ctx, tx, h, r) })
}

// ExtendIn extends h's reservation in tx, a transaction of the account's scope that may hold other money work (the
// gateway's completion batch). The caller commits the transaction; until it does, the extension is not acknowledged.
func (b *Billing) ExtendIn(ctx context.Context, tx pg.Tx, h *Hold, r ExtendRequest) error {
	a := h.admitted
	balance, err := lockAccount(ctx, tx, a.Account)
	if err != nil {
		return err
	}
	if err := b.fence(ctx, tx); err != nil {
		return err
	}
	row, err := queries.GetReservation(ctx, tx, h.id)
	if err != nil {
		return fmt.Errorf("billing: read the reservation: %w", err)
	}
	if row.State != "pending" {
		return ErrTerminated
	}
	delta := r.Target.CeilAmount() - row.Amount
	if delta <= 0 {
		return nil
	}
	usage, err := a.usage(ctx, tx, &row.WorkspaceWindow) // the reservation's day, which the extension books to
	if err != nil {
		return err
	}
	avail := a.available(balance, usage.WorkspaceUsed, usage.KeyUsed)
	if err := extendRoom(ctx, tx, a, row, platform.AmountExact(delta), avail); err != nil {
		return err
	}
	// Under the balance row lock nothing changed the row since it was read, so the compare must hold.
	if n, err := queries.ExtendReservation(ctx, tx, ExtendReservationParams{
		RequestID: h.id, PreviousSegment: row.Segment, Delta: delta,
	}); err != nil {
		return fmt.Errorf("billing: write the extension: %w", err)
	} else if n != 1 {
		return fmt.Errorf("billing: the extension of %s changed %d rows under the lock", h.id, n)
	}
	// Book to the windows the row holds, the same ones the limits were just checked in.
	booked := Hold{
		id: h.id, admitted: a, workspaceWindow: row.WorkspaceWindow, keyWindow: row.KeyWindow,
		funding: row.Funding, subscription: orElse(row.SubscriptionID, uuid.UUID{}), windowSeq: orElse(row.WindowSeq, 0),
	}
	return booked.addReserved(ctx, tx, delta)
}

// extendRoom checks the delta against the source the reservation froze. A plan hold is not capped by the wallet's own
// headroom, but the workspace and key limits count both sources alike; a window short of the delta truncates the answer,
// as a short balance does.
func extendRoom(ctx context.Context, tx pg.Tx, a Admitted, row GetReservationRow, amount platform.Exact, avail []Available) error {
	if row.Funding != "plan" {
		if short := shortSource(amount, avail); short >= 0 {
			return a.transient(&Insufficient{Source: avail[short].Source})
		}
		return nil
	}
	if short := shortSource(amount, avail[1:]); short >= 0 {
		return a.transient(&Insufficient{Source: avail[short+1].Source})
	}
	w, err := queries.Window(ctx, tx, WindowParams{SubscriptionID: *row.SubscriptionID, Seq: *row.WindowSeq})
	if err != nil {
		return fmt.Errorf("billing: read the frozen window: %w", err)
	}
	if amount.Cmp(platform.AmountExact(w.Quota-w.Used-w.Reserved)) > 0 {
		return a.transient(&Insufficient{Source: SourceBalance})
	}
	return nil
}

// SettleOrphan ends the reservation of a request whose admission's COMMIT went unacknowledged (README.md「追加与结算」).
// The balance row lock waits for that admission to end, since it holds the lock until then: a pending reservation it
// committed is released with no attempt made, and if it rolled back a released tombstone takes the request ID, so the
// lost admission can never land later. The caller retries a database error; a dead process leaves it to cleanup.
func (b *Billing) SettleOrphan(ctx context.Context, a Admitted, id uuid.UUID) error {
	return b.db.Tx(ctx, a.Scope, func(tx pg.Tx) error { return b.SettleOrphanIn(ctx, tx, a, id) })
}

// SettleOrphanIn settles the orphan in tx, a transaction of the account's scope that may hold other money work. The
// caller commits the transaction.
func (b *Billing) SettleOrphanIn(ctx context.Context, tx pg.Tx, a Admitted, id uuid.UUID) error {
	if _, err := lockAccount(ctx, tx, a.Account); err != nil {
		return err
	}
	row, err := queries.GetReservation(ctx, tx, id)
	switch {
	case errors.Is(err, pg.ErrNoRows):
		err = queries.InsertTombstone(ctx, tx, InsertTombstoneParams{
			RequestID: id, SiteID: a.Site, AccountID: a.Account, WorkspaceID: a.Workspace, KeyID: a.Key, HolderID: b.holder,
		})
	case err == nil && row.State == "pending":
		h := &Hold{
			id: id, admitted: a, workspaceWindow: row.WorkspaceWindow, keyWindow: row.KeyWindow,
			funding: row.Funding, subscription: orElse(row.SubscriptionID, uuid.UUID{}), windowSeq: orElse(row.WindowSeq, 0),
		}
		if _, err = queries.ReleaseReservation(ctx, tx, id); err == nil {
			err = h.addReserved(ctx, tx, -row.Amount)
		}
	case err == nil: // ended already, and told suppliers so
		return nil
	}
	if err == nil {
		err = b.ended(ctx, tx, id, a.Site, a.Account)
	}
	if err != nil {
		return fmt.Errorf("billing: settle the orphan %s: %w", id, err)
	}
	return nil
}

// ended tells suppliers the request ended, at the database's time.
func (b *Billing) ended(ctx context.Context, tx pg.Tx, id, site, account uuid.UUID) error {
	at, err := queries.Now(ctx, tx)
	if err == nil {
		err = b.suppliers.EndRequest(ctx, tx, id, site, account, at)
	}
	return err
}

// addReserved moves the hold's counters by delta: the balance's reserved for a wallet hold, the frozen window's for a
// plan hold, and the workspace's and key's always, since their limits count both sources alike.
func (h *Hold) addReserved(ctx context.Context, tx pg.Tx, delta int64) error {
	a := h.admitted
	balance := delta
	if h.plan() {
		balance = 0
		if err := moveWindowReserved(ctx, tx, h.subscription, h.windowSeq, delta); err != nil {
			return err
		}
	}
	if err := queries.AddReserved(ctx, tx, AddReservedParams{
		BalanceDelta: balance, SpendDelta: delta, AccountID: a.Account, WorkspaceID: a.Workspace,
		WorkspaceWindow: h.workspaceWindow, KeyID: a.Key, KeyWindow: h.keyWindow,
	}); err != nil {
		return fmt.Errorf("billing: move the reserved counters: %w", err)
	}
	return nil
}

// addWindowReserved moves a window's reserved amount, refusing to cross its quota: a request that would is refused
// before, so 0 rows here is an accounting error rather than a shortage.
func moveWindowReserved(ctx context.Context, tx pg.Tx, subscription uuid.UUID, seq int32, delta int64) error {
	n, err := queries.AddWindowReserved(ctx, tx, AddWindowReservedParams{
		SubscriptionID: subscription, Seq: seq, Delta: delta,
	})
	if err != nil {
		return fmt.Errorf("billing: move the window's reserved amount: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("billing: the window %s/%d cannot take %d reserved under the lock", subscription, seq, delta)
	}
	return nil
}

// errNoBalance is an account without a balance row: no lock to take, so no money may move.
var errNoBalance = errors.New("billing: the account has no balance row")

// lockAccount locks the account's balance row, the one serialisation point of its money.
func lockAccount(ctx context.Context, tx pg.Tx, account uuid.UUID) (LockBalanceRow, error) {
	row, err := queries.LockBalance(ctx, tx, account)
	return row, balanceErr(err)
}

func balanceErr(err error) error {
	if errors.Is(err, pg.ErrNoRows) {
		return errNoBalance
	}
	if err != nil {
		return fmt.Errorf("billing: lock the balance: %w", err)
	}
	return nil
}

// usage reads what the workspace and the key have spent and hold reserved in the windows of their limits that hold
// day, or today when day is nil.
func (a Admitted) usage(ctx context.Context, tx pg.Tx, day *time.Time) (ReadUsageRow, error) {
	u, err := queries.ReadUsage(ctx, tx, a.usageParams(day))
	if err != nil {
		return u, fmt.Errorf("billing: read the counters: %w", err)
	}
	return u, nil
}

func (a Admitted) usageParams(day *time.Time) ReadUsageParams {
	return ReadUsageParams{
		Day: day, WorkspaceID: a.Workspace, KeyID: a.Key,
		WorkspaceReset: string(reset(a.WorkspaceBudget)), KeyReset: string(reset(a.KeyLimit)),
	}
}

// available is what each source still allows: the balance less its reservations and freezes down to the floor, and each limit
// less what its window has spent and holds reserved. Exact, so no combination of stored values can wrap.
func (a Admitted) available(balance LockBalanceRow, workspaceUsed, keyUsed int64) []Available {
	left := func(have int64, less ...int64) platform.Exact {
		e := platform.AmountExact(have)
		for _, n := range less {
			e = sum(e, platform.AmountExact(n).Neg())
		}
		return e
	}
	avail := []Available{{SourceBalance, left(balance.Balance, balance.Reserved, balance.Frozen, balance.Floor)}}
	if a.WorkspaceBudget != nil {
		avail = append(avail, Available{SourceWorkspace, left(a.WorkspaceBudget.Amount, workspaceUsed)})
	}
	if a.KeyLimit != nil {
		avail = append(avail, Available{SourceKey, left(a.KeyLimit.Amount, keyUsed)})
	}
	return avail
}

// transient marks a refusal by a limit that resets: retrying later may succeed, which a short balance never does.
func (a Admitted) transient(e *Insufficient) *Insufficient {
	switch e.Source {
	case SourceWorkspace:
		e.Transient = reset(a.WorkspaceBudget) != ResetNever
	case SourceKey:
		e.Transient = reset(a.KeyLimit) != ResetNever
	case SourceBalance, SourceSiteBalance, SourceSubscription:
	}
	return e
}

func reset(l *Limit) Reset {
	if l == nil {
		return ResetNever
	}
	return l.Reset
}
