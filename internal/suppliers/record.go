package suppliers

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/reconcile"
	"axiom/internal/suppliers/internal/store"
)

// CostFlag says how much of an attempt's usage the estimate covers.
type CostFlag uint8

// The flags. Reconciliation judges an estimate only when it is complete: one that left out an item with no price, or
// had no usage at all, says nothing about whether the price was right.
const (
	CostComplete CostFlag = iota // every item with usage has a price
	CostUnpriced                 // some item with usage has none: the estimate holds only the priced part
	CostNoUsage                  // the supplier reported no usage, so any estimate rests on guessed counts or is zero
)

func (f CostFlag) String() string { return [...]string{"complete", "unpriced", "no_usage"}[f] }

// AttemptCost is the estimate of an attempt's cost to us, in the supplier's own unit, from the schedule the attempt was
// priced with. It is not money: the amount is in the unit of the supplier (quota, dollars) and billing turns it into
// money.
type AttemptCost struct {
	Native  platform.Exact
	Flag    CostFlag
	Missing catalog.ItemSet
	// Unknown is set when the supplier reported usage that has no billing item: it cannot be priced, so the estimate is
	// not complete, and it is not the same as no usage.
	Unknown bool
}

// Cost prices the usage of an attempt, one entry per segment, with the schedule its receipt names. It reads memory
// only. A schedule it does not hold, which can only be from a database that changed under a receipt, prices nothing and
// says so with the flag, so that the cost is never taken for zero.
func (s *Service) Cost(r AttemptReceipt, usage []catalog.Usage) (AttemptCost, error) {
	schedule := (*s.schedules.Load())[r.schedule]
	var cost AttemptCost
	used := false
	for _, u := range usage {
		items := usedItems(u)
		unknown := len(u.Unknown()) > 0
		cost.Unknown = cost.Unknown || unknown
		used = used || items != 0 || unknown
		if schedule == nil {
			cost.Missing |= items
			continue
		}
		total, missing, err := schedule.Cost(u)
		if err != nil {
			return AttemptCost{}, fmt.Errorf("price the usage of an attempt: %w", err)
		}
		if cost.Native, err = cost.Native.Add(total); err != nil {
			return AttemptCost{}, fmt.Errorf("add the cost of an attempt: %w", err)
		}
		cost.Missing |= missing
	}
	switch {
	case !used:
		cost.Flag = CostNoUsage
	case cost.Missing != 0 || cost.Unknown:
		cost.Flag = CostUnpriced
	}
	return cost, nil
}

// Credits is the attempt's Cost in billionths of a credit of currency: its native amount times the unit value the
// attempt began with, rounded up once (docs/domains/billing.md「钱的表示」). known is false when that is not the whole
// cost: the estimate leaves some usage out, the supplier is paid in another currency (there are no exchange rates yet),
// or the amount is beyond what an amount holds. The priced part is still given when it can be told in currency. The
// Cost it was converted from is returned with it, so that one pricing serves both.
func (s *Service) Credits(r AttemptReceipt, usage []catalog.Usage, currency platform.Currency) (c AttemptCost, credits int64, known bool, err error) {
	c, err = s.Cost(r, usage)
	if err != nil || r.currency != currency {
		return c, 0, false, err
	}
	money := new(big.Rat).Mul(c.Native.Rat(), r.unitValue.Rat())
	q, m := new(big.Int).DivMod(new(big.Int).Mul(money.Num(), big.NewInt(1e9)), money.Denom(), new(big.Int))
	if m.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return c, 0, false, nil
	}
	return c, q.Int64(), c.Flag != CostUnpriced, nil
}

// usedItems are the items the usage has a count of.
func usedItems(u catalog.Usage) (items catalog.ItemSet) {
	for it := range catalog.NumItems {
		if u.Count(it) != 0 {
			items.Add(it)
		}
	}
	return items
}

// AttemptRecord is a finished attempt, as billing hands it to RecordAttempts. Site and Account are the request's tenant,
// nil for a request of the platform's own.
type AttemptRecord struct {
	Request       platform.RequestID
	Seq           platform.AttemptSeq
	Site, Account *uuid.UUID
	Receipt       AttemptReceipt
	Usage         []catalog.Usage
	Cost          AttemptCost
	FinishedAt    time.Time
}

// RecordAttempts writes the cost and reconciliation key of a request's attempts, all in one statement, in the
// transaction of billing's settlement, so that they are there if and only if the cost billing booked is. An attempt
// already recorded stays as it was: a settlement repeated after a crash changes nothing.
func RecordAttempts(ctx context.Context, tx pg.Tx, attempts []AttemptRecord) error {
	rows := make([]store.AttemptCostRow, 0, len(attempts))
	for _, a := range attempts {
		if !a.Seq.Valid() {
			return fmt.Errorf("record attempts: attempt number %d of request %s", a.Seq, a.Request)
		}
		usage, err := usageOf(a.Usage)
		if err != nil {
			return err
		}
		estimate, err := decimal(a.Cost.Native)
		if err != nil {
			return err
		}
		row := store.AttemptCostRow{
			RequestID: uuid.UUID(a.Request), AttemptSeq: int16(a.Seq), SiteID: a.Site, AccountID: a.Account,
			SupplierID: a.Receipt.supplier, CredentialID: a.Receipt.credential, EndpointID: a.Receipt.endpoint,
			ScheduleID: a.Receipt.schedule, Usage: usage, PricedAt: a.Receipt.pricedAt, FinishedAt: a.FinishedAt,
			CostFlag: a.Cost.Flag.String(), EstimatedNative: estimate,
		}
		if a.Receipt.ref != "" {
			row.ChargeRef = &a.Receipt.ref
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	if err := store.InsertAttemptCosts(ctx, tx, rows); err != nil {
		return fmt.Errorf("record attempts: %w", err)
	}
	return nil
}

// usageOf is the usage of an attempt as stored: the counts of each item added over the segments, and the tier.
func usageOf(segments []catalog.Usage) ([]byte, error) {
	items := map[string]int64{}
	unknown := []map[string]string{}
	tier := catalog.ServiceTierStandard
	for i, u := range segments {
		if i == 0 {
			tier = u.ServiceTier()
		}
		for it := range catalog.NumItems {
			if n := u.Count(it); n != 0 {
				items[it.String()] += n
			}
		}
		for _, k := range u.Unknown() {
			unknown = append(unknown, map[string]string{"field": k.Field, "value": k.Value})
		}
	}
	return jsonx.MarshalCanonical(map[string]any{"items": items, "unknown": unknown, "tier": tier.String()})
}

// decimal writes an exact amount as the decimal text the database reads, without a float in between.
func decimal(e platform.Exact) (string, error) {
	r := e.Rat()
	if r.Sign() < 0 {
		return "", fmt.Errorf("a cost of %s is negative", r.FloatString(15))
	}
	text := r.FloatString(15) // the amount is in units of 10⁻¹⁵, so 15 decimals are exact
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	return text, nil
}

// RecordLostRequest notes that a request's node was lost before it settled, when billing gives up on it: only the
// request is known, and the route it was frozen with. It never changes, and writing it again changes nothing.
func RecordLostRequest(ctx context.Context, tx pg.Tx, id platform.RequestID, site, account uuid.UUID, route []uuid.UUID) error {
	err := store.New().InsertLostRequest(ctx, tx, store.InsertLostRequestParams{
		RequestID: uuid.UUID(id), SiteID: site, AccountID: account, RouteEndpointIds: route,
	})
	if err != nil {
		return fmt.Errorf("record the lost request %s: %w", id, err)
	}
	return nil
}

// ResolveLostRequest notes that a node came back with the real attempts of a lost request, which billing records with
// RecordAttempts in the same transaction. It is an insert, not a change of the lost request, and takes the tenant from
// it: a request never recorded as lost has nothing to resolve, and that is an error, since billing resolves only what
// its own state says is lost.
func ResolveLostRequest(ctx context.Context, tx pg.Tx, id platform.RequestID) error {
	named, err := store.New().InsertLostRequestResolution(ctx, tx, uuid.UUID(id))
	if err != nil {
		return fmt.Errorf("resolve the lost request %s: %w", id, err)
	}
	if named == 0 {
		return fmt.Errorf("resolve the lost request %s: it was never recorded as lost", id)
	}
	return nil
}

// EndRequest notes that a request ended for good at the time given: billing calls it in the transaction that ends the
// request, when it is settled, released, or cleaned up, and never before. Telling it again changes nothing.
func EndRequest(ctx context.Context, tx pg.Tx, id platform.RequestID, site, account uuid.UUID, at time.Time) error {
	err := store.New().InsertEndedRequest(ctx, tx, store.InsertEndedRequestParams{
		RequestID: uuid.UUID(id), SiteID: tenant(site), AccountID: tenant(account), EndedAt: at,
	})
	if err != nil {
		return fmt.Errorf("end the request %s: %w", id, err)
	}
	return nil
}

// CallWrites is the writes above as billing.Suppliers names them, with the request as a plain uuid: billing calls them
// in the transaction of its own settlement and cleanup, which is how a call's supplier side commits exactly when the
// ledger's cost does (docs/architecture.md 规则 5). It has no state: the zero value is the one to hand billing.
type CallWrites struct{}

// RecordAttempts is the package function of the name.
func (CallWrites) RecordAttempts(ctx context.Context, tx pg.Tx, attempts []AttemptRecord) error {
	return RecordAttempts(ctx, tx, attempts)
}

// RecordLostRequest is the package function of the name, with the request as a uuid.
func (CallWrites) RecordLostRequest(ctx context.Context, tx pg.Tx, request, site, account uuid.UUID, route []uuid.UUID) error {
	return RecordLostRequest(ctx, tx, platform.RequestID(request), site, account, route)
}

// ResolveLostRequest is the package function of the name, with the request as a uuid.
func (CallWrites) ResolveLostRequest(ctx context.Context, tx pg.Tx, request uuid.UUID) error {
	return ResolveLostRequest(ctx, tx, platform.RequestID(request))
}

// EndRequest is the package function of the name, with the request as a uuid.
func (CallWrites) EndRequest(ctx context.Context, tx pg.Tx, request, site, account uuid.UUID, at time.Time) error {
	return EndRequest(ctx, tx, platform.RequestID(request), site, account, at)
}

// tenant is a tenant column: NULL for the zero id, which says a request is the platform's own.
func tenant(id uuid.UUID) *uuid.UUID {
	if id == (uuid.UUID{}) {
		return nil
	}
	return &id
}

// ReconcileConfig is what reconciliation reads from the operator's settings.
type ReconcileConfig = reconcile.Config

// ReconcileCursor is where the list of waiting attempts goes on from, between runs.
type ReconcileCursor = reconcile.Cursor

// ReconcileReport is what a run did and what needs a person.
type ReconcileReport = reconcile.Report

// DefaultReconcileConfig is 30 minutes of grace and a tolerance of 1%.
func DefaultReconcileConfig() ReconcileConfig { return reconcile.DefaultConfig() }

// Reconcile makes one pass in the transaction of the caller, which has the platform scope: matches charges to attempts,
// holds the charges that waited past the grace, and reports the attempts that did, the endpoints delisted because their
// estimate was lower than the charge, and the charges lower than the estimate. Every change is an event for billing.
func Reconcile(ctx context.Context, tx pg.Tx, now time.Time, cfg ReconcileConfig) (ReconcileReport, error) {
	return reconcile.Run(ctx, tx, now, cfg)
}

// RecordCharges stores a page of the supplier's charge records, as the scan of its adapter read them, in the sync
// engine's transaction. A record already stored stays as it was. A record not drawn on the wallet is stored and
// otherwise ignored, and pauses the supplier; the number of them is returned for the engine to alert on.
func RecordCharges(ctx context.Context, tx pg.Tx, supplier uuid.UUID, charges []adapter.Charge) (nonWallet int, err error) {
	return reconcile.Ingest(ctx, tx, supplier, charges)
}

// AssignToLostRequest attributes a held charge to a lost request. It reports false when the charge is not held.
func AssignToLostRequest(ctx context.Context, tx pg.Tx, charge, lost uuid.UUID) (bool, error) {
	return reconcile.AssignToLostRequest(ctx, tx, charge, lost)
}

// AssignUnattributed settles a held charge as a cost of the supplier no user pays for. It reports false when the
// charge is not held.
func AssignUnattributed(ctx context.Context, tx pg.Tx, charge uuid.UUID) (bool, error) {
	return reconcile.AssignUnattributed(ctx, tx, charge)
}
