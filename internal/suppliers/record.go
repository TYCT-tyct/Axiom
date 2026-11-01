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
	CostNoUsage                  // the supplier reported no usage and the schedule prices no request, so the estimate rests on guessed counts or is zero
)

func (f CostFlag) String() string { return [...]string{"complete", "unpriced", "no_usage"}[f] }

// Conversion says how an attempt's native cost became money. The zero value is no conversion computed, which is how old
// nodes' and legacy writes stay unknown. The names are what attempt_costs.conversion_state stores (its CHECK, migration
// 00007).
type Conversion uint8

// The conversions.
const (
	ConversionUnset Conversion = iota
	ConversionSame
	ConversionConverted
	ConversionMissingRate
	ConversionOverflow
	ConversionUnpriced
	ConversionNotRun // the attempt is known not to have run: nothing to convert
)

func (c Conversion) String() string {
	return [...]string{"", "same_currency", "converted", "missing_rate", "overflow", "unpriced", "not_executed"}[c]
}

// AttemptCost is the estimate of an attempt's cost to us, in the supplier's own unit, from the schedule the attempt was
// priced with. It is not money: the amount is in the unit of the supplier (quota, dollars) and billing turns it into
// money.
type AttemptCost struct {
	// Conversion describes conversion of Native, independently of Flag's coverage of the supplier's usage.
	Conversion Conversion
	Currency   platform.Currency
	Native     platform.Exact
	Flag       CostFlag
	Missing    catalog.ItemSet
	// Unknown is set when the supplier reported usage that has no billing item: it cannot be priced, so the estimate is
	// not complete, and it is not the same as no usage.
	Unknown bool
}

// Cost prices the usage of an attempt, one entry per segment, with the schedule its receipt names. It reads memory
// only. A schedule it does not hold, which can only be from a database that changed under a receipt, prices nothing and
// says so with the flag, so that the cost is never taken for zero. A schedule that prices the request item charges one
// request for the attempt, however many internal samplings it had; the item is not in the upstream usage, so it is
// added once here. The caller passes no usage for a NotExecuted attempt and skips this function for it.
func (s *Service) Cost(r AttemptReceipt, usage []catalog.Usage) (AttemptCost, error) {
	schedule := (*s.schedules.Load())[r.schedule]
	var cost AttemptCost
	var last catalog.Usage
	used := false
	for _, u := range usage {
		last = u
		reported, err := cost.add(schedule, u)
		if err != nil {
			return AttemptCost{}, err
		}
		used = used || reported
	}
	charged, err := cost.chargeRequest(schedule, last)
	if err != nil {
		return AttemptCost{}, err
	}
	used = used || charged
	switch {
	case !used:
		cost.Flag = CostNoUsage
	case cost.Missing != 0 || cost.Unknown:
		cost.Flag = CostUnpriced
	}
	return cost, nil
}

// add prices one entry of the attempt's usage. It reports whether the upstream reported anything to charge, and a
// schedule the service does not hold records the items as missing, so that the cost is never taken for zero.
func (c *AttemptCost) add(schedule *catalog.Schedule, u catalog.Usage) (bool, error) {
	items := usedItems(u)
	unknown := len(u.Unknown()) > 0
	c.Unknown = c.Unknown || unknown
	if schedule == nil {
		c.Missing |= items
		return items != 0 || unknown, nil
	}
	total, missing, err := schedule.Cost(u)
	if err != nil {
		return false, fmt.Errorf("price the usage of an attempt: %w", err)
	}
	if c.Native, err = c.Native.Add(total); err != nil {
		return false, fmt.Errorf("add the cost of an attempt: %w", err)
	}
	c.Missing |= missing
	return items != 0 || unknown, nil
}

// chargeRequest adds the attempt's one request, or records that the schedule prices the item nowhere. It reports whether
// the schedule prices a request item, which is a charge of the attempt even when the upstream reported no usage. The
// upstream reports no count of it, and it is charged at the band of the last segment, which carries the tier it ran on.
func (c *AttemptCost) chargeRequest(schedule *catalog.Schedule, last catalog.Usage) (bool, error) {
	if schedule == nil || !schedule.Priced(1<<catalog.ItemRequest) {
		return false, nil
	}
	price, _, ok := schedule.Rate(last, catalog.ItemRequest)
	if !ok {
		c.Missing.Add(catalog.ItemRequest)
		return true, nil
	}
	native, err := c.Native.Add(price.Mul(1))
	if err != nil {
		return false, fmt.Errorf("add the request cost of an attempt: %w", err)
	}
	c.Native = native
	return true, nil
}

// Credits multiplies native cost, the attempt's unit value and the reservation's exchange rate, rounding up once to
// billionths (docs/domains/billing.md「钱的表示」). known is false when usage cannot all be priced, the frozen pair is
// missing, or the amount overflows. The priced part is still given when convertible; Cost serves native reconciliation.
func (s *Service) Credits(r AttemptReceipt, usage []catalog.Usage, currency platform.Currency) (c AttemptCost, credits int64, known bool, err error) {
	c, err = s.Cost(r, usage)
	c.Currency = currency
	if err != nil {
		c.Conversion = ConversionUnpriced
		return c, 0, false, err
	}
	money := new(big.Rat).Mul(c.Native.Rat(), r.unitValue.Rat())
	c.Conversion = ConversionSame
	if r.currency != currency {
		rate, ok := r.exchange.Between(r.currency, currency)
		if !ok {
			c.Conversion = ConversionMissingRate
			return c, 0, false, nil
		}
		money.Mul(money, rate)
		c.Conversion = ConversionConverted
	}
	q, m := new(big.Int).DivMod(new(big.Int).Mul(money.Num(), big.NewInt(1e9)), money.Denom(), new(big.Int))
	if m.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		c.Conversion = ConversionOverflow
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
// zero for a request of the platform's own. Test marks a test generation, whose estimate is a bound of our own choosing
// (TestEndpoint): no tenant pays it, and reconciliation never judges it.
type AttemptRecord struct {
	Request       platform.RequestID
	Seq           platform.AttemptSeq
	Hop           int16
	Site, Account uuid.UUID
	Receipt       AttemptReceipt
	Usage         []catalog.Usage
	Cost          AttemptCost
	FinishedAt    time.Time
	Test          bool
}

// setBasis saves what the cost was computed from; an attempt without a rate or a conversion keeps those columns NULL.
func setBasis(row *store.AttemptCostRow, a AttemptRecord) {
	if a.Receipt.unitValueID != (uuid.UUID{}) {
		row.UnitValueID = &a.Receipt.unitValueID
	}
	// Only a conversion that reached the multiplication used the rate; a same-currency overflow used none.
	used := a.Cost.Conversion == ConversionConverted || a.Cost.Conversion == ConversionOverflow
	if used && a.Receipt.currency != a.Cost.Currency {
		row.ExchangeRateID = &a.Receipt.exchange.ID
	}
	if a.Cost.Conversion != ConversionUnset {
		from, to, state := a.Receipt.currency.String(), a.Cost.Currency.String(), a.Cost.Conversion.String()
		row.ConversionFrom, row.ConversionTo, row.ConversionState = &from, &to, &state
	}
}

// RecordAttempts writes the cost and reconciliation keys of a request's attempts in billing's settlement transaction,
// so that they are there if and only if the cost billing booked is. An attempt already recorded stays as it was: a
// settlement repeated after a crash changes nothing.
//
// The legacy attempt_costs row remains one per gateway attempt, with the sum of the hops: that is the row old nodes
// insert and match against during a rolling upgrade. The new attempt_cost_hops table carries the per-hop rows; a
// request whose loop failed after a search keeps the failed request's row even though no Relay consumed it.
func RecordAttempts(ctx context.Context, tx pg.Tx, attempts []AttemptRecord) error {
	details, groups, order, err := attemptGroups(attempts)
	if err != nil {
		return err
	}
	aggregates, err := aggregateAttemptRows(groups, order)
	if err != nil {
		return err
	}
	if len(aggregates) > 0 {
		if err := store.InsertAttemptCosts(ctx, tx, aggregates); err != nil {
			return fmt.Errorf("record attempts: %w", err)
		}
	}
	if len(details) > 0 {
		if err := store.InsertAttemptCostHops(ctx, tx, details); err != nil {
			return fmt.Errorf("record attempt hops: %w", err)
		}
	}
	return nil
}

// attemptGroups validates the records and keeps the detail rows in input order, grouped by the legacy attempt key.
func attemptGroups(attempts []AttemptRecord) ([]store.AttemptCostRow, map[attemptKey][]AttemptRecord, []attemptKey, error) {
	details := make([]store.AttemptCostRow, 0, len(attempts))
	groups := make(map[attemptKey][]AttemptRecord, len(attempts))
	order := make([]attemptKey, 0, len(attempts))
	for i := range attempts {
		a := &attempts[i]
		if !a.Seq.Valid() {
			return nil, nil, nil, fmt.Errorf("record attempts: attempt number %d of request %s", a.Seq, a.Request)
		}
		if a.Hop < 0 || a.Hop > 255 {
			return nil, nil, nil, fmt.Errorf("record attempts: hop %d of attempt %d of request %s", a.Hop, a.Seq, a.Request)
		}
		row, err := attemptCostRow(*a)
		if err != nil {
			return nil, nil, nil, err
		}
		details = append(details, row)
		k := attemptKey{uuid.UUID(a.Request), int16(a.Seq)}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], *a)
	}
	return details, groups, order, nil
}

type attemptKey struct {
	request uuid.UUID
	seq     int16
}

// aggregateAttemptRows builds one old-format row per attempt. A loop has several detail rows but still one aggregate
// row, because charge_records keeps its foreign key to the old table during a rolling upgrade.
func aggregateAttemptRows(groups map[attemptKey][]AttemptRecord, order []attemptKey) ([]store.AttemptCostRow, error) {
	aggregates := make([]store.AttemptCostRow, 0, len(order))
	for _, k := range order {
		group := groups[k]
		a := group[0]
		a.Hop = 0
		if len(group) > 1 {
			a = mergeAttempts(group)
		}
		row, err := attemptCostRow(a)
		if err != nil {
			return nil, err
		}
		aggregates = append(aggregates, row)
	}
	return aggregates, nil
}

// mergeAttempts summarizes a loop for the legacy one-row table. The charge reference is deliberately empty: each hop
// has its own reference, and leaving one here would let an old matcher attribute a hop's charge to the whole attempt.
func mergeAttempts(group []AttemptRecord) AttemptRecord {
	out := group[len(group)-1]
	out.Hop, out.Usage = 0, nil
	out.Receipt.ref = ""
	out.FinishedAt = group[0].FinishedAt
	out.Cost = group[0].Cost
	for _, a := range group {
		out.Usage = append(out.Usage, a.Usage...)
		out.Test = out.Test || a.Test
		if a.FinishedAt.After(out.FinishedAt) {
			out.FinishedAt = a.FinishedAt
		}
	}
	for _, a := range group[1:] {
		mergeAttemptCost(&out.Cost, a.Cost)
	}
	return out
}

// mergeAttemptCost folds one hop's supplier cost into the legacy aggregate. The summed flag is the weakest part; the
// native amount is added exactly, and an overflow leaves the conversion state so the row cannot be read as known.
func mergeAttemptCost(dst *AttemptCost, src AttemptCost) {
	dst.Missing |= src.Missing
	dst.Unknown = dst.Unknown || src.Unknown
	dst.Conversion = mergeAttemptConversion(dst.Conversion, src.Conversion)
	switch {
	case dst.Flag == CostUnpriced || src.Flag == CostUnpriced:
		dst.Flag = CostUnpriced
	case dst.Flag == CostComplete || src.Flag == CostComplete:
		dst.Flag = CostComplete
	default:
		dst.Flag = CostNoUsage
	}
	n, err := dst.Native.Add(src.Native)
	if err != nil {
		dst.Conversion = ConversionOverflow
		return
	}
	dst.Native = n
}

func mergeAttemptConversion(a, b Conversion) Conversion {
	if a == ConversionUnset {
		return b
	}
	if b == ConversionUnset || a == b {
		return a
	}
	if a == ConversionNotRun {
		return b
	}
	if b == ConversionNotRun {
		return a
	}
	for _, c := range []Conversion{
		ConversionUnpriced, ConversionOverflow, ConversionMissingRate, ConversionConverted,
	} {
		if a == c || b == c {
			return c
		}
	}
	return ConversionSame
}

// attemptCostRow is one attempt or hop as the store writes it.
func attemptCostRow(a AttemptRecord) (store.AttemptCostRow, error) {
	usage, err := usageOf(a.Usage)
	if err != nil {
		return store.AttemptCostRow{}, err
	}
	estimate, err := decimal(a.Cost.Native)
	if err != nil {
		return store.AttemptCostRow{}, err
	}
	row := store.AttemptCostRow{
		RequestID: uuid.UUID(a.Request), AttemptSeq: int16(a.Seq), Hop: a.Hop, SiteID: tenant(&a.Site), AccountID: tenant(&a.Account),
		SupplierID: a.Receipt.supplier, CredentialID: a.Receipt.credential, EndpointID: a.Receipt.endpoint,
		ScheduleID: a.Receipt.schedule, Usage: usage, PricedAt: a.Receipt.pricedAt, FinishedAt: a.FinishedAt,
		CostFlag: a.Cost.Flag.String(), EstimatedNative: estimate, Test: a.Test,
	}
	setBasis(&row, a)
	if a.Receipt.ref != "" {
		row.ChargeRef = &a.Receipt.ref
	}
	return row, nil
}

// usageOf is the usage of an attempt as stored: the counts of each item added over the segments, and the tier. An
// OpenAI segment's cache reads and writes may overlap, so its items add up to more than its prompt: the prompts as the
// upstream reported them are kept beside them as physical_input.
func usageOf(segments []catalog.Usage) ([]byte, error) {
	items := map[string]int64{}
	unknown := []map[string]string{}
	tier := catalog.ServiceTierStandard
	var physical int64
	var hasPhysical bool
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
		if n, ok := u.PhysicalInput(); ok {
			physical, hasPhysical = physical+n, true
		}
	}
	doc := map[string]any{"items": items, "unknown": unknown, "tier": tier.String()}
	if hasPhysical {
		doc["physical_input"] = physical
	}
	return jsonx.MarshalCanonical(doc)
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
		RequestID: uuid.UUID(id), SiteID: tenant(&site), AccountID: tenant(&account), EndedAt: at,
	})
	if err != nil {
		return fmt.Errorf("end the request %s: %w", id, err)
	}
	return nil
}

// EndedRequest is one request's end: the database time it ended at and the tenant that owns it.
// EndedRequest is one request's terminal time for EndRequests, with the tenant that owns it.
type EndedRequest struct {
	Request, Site, Account uuid.UUID
	At                     time.Time
}

// EndRequests notes a batch's requests ended for good at the times given, in one statement. A request already ended
// keeps its first time, so a settlement repeated after a crash changes nothing.
func EndRequests(ctx context.Context, tx pg.Tx, ends []EndedRequest) error {
	if len(ends) == 0 {
		return nil
	}
	rows := make([]store.EndedRequestRow, len(ends))
	for i, e := range ends {
		rows[i] = store.EndedRequestRow{RequestID: e.Request, SiteID: e.Site, AccountID: e.Account, EndedAt: e.At}
	}
	if err := store.InsertEndedRequests(ctx, tx, rows); err != nil {
		return fmt.Errorf("end the requests: %w", err)
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

// EndRequests is the package function of the name, with the requests as plain uuids.
func (CallWrites) EndRequests(ctx context.Context, tx pg.Tx, ends []EndedRequest) error {
	return EndRequests(ctx, tx, ends)
}

// tenant is a tenant column: NULL for the zero id, which says a request is the platform's own.
func tenant(id *uuid.UUID) *uuid.UUID {
	if *id == (uuid.UUID{}) {
		return nil
	}
	return id
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

// AssignToLostRequest attributes a held charge to a lost request. It reports false when the charge is not held.
func AssignToLostRequest(ctx context.Context, tx pg.Tx, charge, lost uuid.UUID) (bool, error) {
	return reconcile.AssignToLostRequest(ctx, tx, charge, lost)
}

// AssignUnattributed settles a held charge as a cost of the supplier no user pays for. It reports false when the
// charge is not held.
func AssignUnattributed(ctx context.Context, tx pg.Tx, charge uuid.UUID) (bool, error) {
	return reconcile.AssignUnattributed(ctx, tx, charge)
}
