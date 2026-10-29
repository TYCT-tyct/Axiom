// Package reconcile matches the supplier's charge records to our attempts: one charge to at most one attempt and the
// other way round, within a tolerance, with what cannot be matched held for a person, and every change told to billing
// as an event with a revision (docs/domains/billing.md「供应商成本」, internal/suppliers/README.md「对账」). Everything
// here runs in a transaction the caller opens in the platform scope.
package reconcile

import (
	"context"
	"fmt"
	"math/big"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
)

// Config is what reconciliation reads from the operator's settings, with the defaults of the design.
type Config struct {
	// Grace is how long a charge or an attempt may wait for its other half before it is held or reported.
	Grace time.Duration
	// Relative is the share of the estimate by which the charge may differ from it, on top of the supplier's rounding.
	Relative *big.Rat
	// After is where the list of waiting attempts goes on from: the Next of the last report, or the zero value to begin.
	After Cursor
	// Batch is the most rows a single statement of a run takes, and MaxBatches the most statements of each kind, so that
	// a backlog is worked off over several runs and no run holds a transaction open for long.
	Batch, MaxBatches int
}

// DefaultConfig is 30 minutes of grace, a tolerance of 1%, and up to 20 batches of 500 rows of each kind in a run.
func DefaultConfig() Config {
	return Config{Grace: 30 * time.Minute, Relative: big.NewRat(1, 100), Batch: 500, MaxBatches: 20}
}

// granularity is the most the supplier's own rounding can move one charge, in its native unit: new-api rounds each
// charge to a whole quota (22.5 is charged as 23); sub2api writes ten decimals of a dollar.
var granularity = map[string]*big.Rat{"quota": big.NewRat(1, 1), "usd": big.NewRat(1, 10000000000)}

// Report is what one run did and what needs a person.
type Report struct {
	Matched        int
	Held           []uuid.UUID // charges moved to held in this run
	Delisted       []uuid.UUID // endpoints moved to cost unknown by this run
	UnderUnchanged []uuid.UUID // underestimated charges whose endpoint was not newly delisted
	Over           []uuid.UUID // charges lower than the estimate by more than the tolerance: only reported
	Unmatched      []Unmatched // attempts that reached the supplier and are still without a charge after the grace
	// Next is where the list of waiting attempts stopped when it was cut at a batch, for the next run to go on from; the
	// zero value when the list reached its end, and then the next run begins again at the oldest.
	Next Cursor
	// More is set when a run stopped at its limit with work left: matching or holding of charges, or the list of attempts
	// waiting, which then holds the oldest of them.
	More       bool
	Violations []string // attempts the verdict could not be made for
}

// Cursor is a position in the list of waiting attempts: the last one reported. The list is in a fixed order, so going on
// from a cursor reaches every attempt once, however many batches that takes.
type Cursor struct {
	At      time.Time
	Request uuid.UUID
	Seq     int16
}

// Unmatched is an attempt with a key and no charge.
type Unmatched struct {
	Request uuid.UUID
	Seq     int16
	Ref     string
}

// Ingest stores the charge records of a page of the scan. A record already stored stays as it was. A record that was not
// drawn on the supplier's wallet (a subscription's) is stored and does nothing else, never matched, held or settled:
// the supplier is paused for a person to look at, and the number of such records is returned.
func Ingest(ctx context.Context, tx store.DBTX, supplier uuid.UUID, charges []adapter.Charge) (nonWallet int, err error) {
	rows := make([]store.ChargeRow, 0, len(charges))
	for _, c := range charges {
		usage, err := usageJSON(c.Usage, c.Reported)
		if err != nil {
			return 0, err
		}
		funding := "other"
		if c.Funding == adapter.FundingWallet {
			funding = "wallet"
		} else {
			nonWallet++
		}
		rows = append(rows, store.ChargeRow{
			KeyRef: string(c.Key), ChargeRef: string(c.Ref), OccurredAt: c.OccurredAt, AmountNative: string(c.Amount),
			Usage: usage, Funding: funding, UpstreamModel: c.Model,
		})
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := store.InsertChargeRecords(ctx, tx, supplier, rows); err != nil {
		return 0, err
	}
	if nonWallet > 0 {
		if _, err := store.New().PauseSupplierForFunding(ctx, tx, supplier); err != nil {
			return 0, fmt.Errorf("pause the supplier whose charges are not on its wallet: %w", err)
		}
	}
	return nonWallet, nil
}

// usageJSON is a charge's usage as stored: the count of each item the record gave or counted, the items it states
// ("reported", which may include one it states as zero, and leaves out the one it did not state), the usage the
// supplier gave that has no billing item, and the tier. Whether an item is reported tells whether its count may correct
// an estimate, so it is kept as it came.
func usageJSON(u catalog.Usage, reported catalog.ItemSet) ([]byte, error) {
	return jsonx.MarshalCanonical(usageDoc(u, reported))
}

// usageDoc is the stored form of a charge's usage.
func usageDoc(u catalog.Usage, reported catalog.ItemSet) map[string]any {
	items := map[string]int64{}
	var states []string
	for it := range catalog.NumItems {
		if n := u.Count(it); n != 0 || reported.Has(it) {
			items[it.String()] = n
		}
		if reported.Has(it) {
			states = append(states, it.String())
		}
	}
	unknown := []map[string]string{}
	for _, k := range u.Unknown() {
		unknown = append(unknown, map[string]string{"field": k.Field, "value": k.Value})
	}
	doc := map[string]any{"items": items, "reported": states, "unknown": unknown, "tier": u.ServiceTier().String()}
	if n, ok := u.PhysicalInput(); ok {
		doc["physical_input"] = n
	}
	return doc
}

// Run makes one pass: matches what can be matched, holds the charges that waited past the grace, and lists the attempts
// that did too. It is safe to run twice at once, and again after a crash: a match is made once and an event is kept
// once per revision.
func Run(ctx context.Context, tx store.DBTX, now time.Time, cfg Config) (Report, error) {
	q := store.New()
	var report Report
	if err := matchAll(ctx, tx, q, cfg, &report); err != nil {
		return report, err
	}
	if err := holdOverdue(ctx, tx, q, now, cfg, &report); err != nil {
		return report, err
	}
	waiting, err := q.ListUnmatchedAttempts(ctx, tx, store.ListUnmatchedAttemptsParams{
		Before: now.Add(-cfg.Grace), AfterAt: cfg.After.At, AfterRequest: cfg.After.Request, AfterSeq: cfg.After.Seq,
		Batch: int32(cfg.Batch), //nolint:gosec // a batch size set in configuration
	})
	if err != nil {
		return report, fmt.Errorf("list unmatched attempts: %w", err)
	}
	for _, w := range waiting {
		report.Unmatched = append(report.Unmatched, Unmatched{Request: w.RequestID, Seq: w.AttemptSeq, Ref: *w.ChargeRef})
	}
	if len(waiting) == cfg.Batch {
		last := waiting[len(waiting)-1]
		report.Next = Cursor{At: last.FinishedAt, Request: last.RequestID, Seq: last.AttemptSeq}
		report.More = true
	}
	return report, nil
}

// matchAll matches in batches, until one comes back short or the run has used its batches.
func matchAll(ctx context.Context, tx store.DBTX, q *store.Queries, cfg Config, report *Report) error {
	for range cfg.MaxBatches {
		matches, err := q.MatchCharges(ctx, tx, int32(cfg.Batch)) //nolint:gosec // a batch size set in configuration
		if err != nil {
			return fmt.Errorf("match charges: %w", err)
		}
		for _, m := range matches {
			if err := settleMatch(ctx, tx, q, cfg, m, report); err != nil {
				return err
			}
		}
		report.Matched += len(matches)
		if len(matches) < cfg.Batch {
			return nil
		}
	}
	report.More = true
	return nil
}

// holdOverdue holds, in batches, the charges that waited past the grace.
func holdOverdue(ctx context.Context, tx store.DBTX, q *store.Queries, now time.Time, cfg Config, report *Report) error {
	for range cfg.MaxBatches {
		held, err := q.HoldOverdueCharges(ctx, tx, store.HoldOverdueChargesParams{Before: now.Add(-cfg.Grace), Batch: int32(cfg.Batch)}) //nolint:gosec // as above
		if err != nil {
			return fmt.Errorf("hold overdue charges: %w", err)
		}
		for _, h := range held {
			if err := event(ctx, tx, q, h.ChargeID, h.Revision, map[string]any{"state": "held", "actual_native": h.ActualNative}); err != nil {
				return err
			}
			report.Held = append(report.Held, h.ChargeID)
		}
		if len(held) < cfg.Batch {
			return nil
		}
	}
	report.More = true
	return nil
}

// settleMatch judges a match against the tolerance, delists an endpoint whose estimate was too low, and tells billing.
func settleMatch(ctx context.Context, tx store.DBTX, q *store.Queries, cfg Config, m store.MatchChargesRow, report *Report) error {
	verdict, err := judge(cfg, m)
	if err != nil {
		report.Violations = append(report.Violations, fmt.Sprintf("charge %s: %v", m.ChargeID, err))
	}
	delisted := false
	switch verdict {
	case "under":
		if n, err := q.DelistEndpoint(ctx, tx, store.DelistEndpointParams{ID: m.EndpointID, CostScheduleID: &m.CostScheduleID}); err != nil {
			return fmt.Errorf("delist endpoint: %w", err)
		} else if n > 0 {
			delisted = true
			report.Delisted = append(report.Delisted, m.EndpointID)
		} else {
			report.UnderUnchanged = append(report.UnderUnchanged, m.ChargeID)
		}
	case "over":
		report.Over = append(report.Over, m.ChargeID)
	}
	return event(ctx, tx, q, m.ChargeID, m.Revision, map[string]any{
		"attribution": "attempt", "request_id": m.RequestID.String(), "attempt_seq": m.AttemptSeq, "verdict": verdict,
		"actual_native": m.ActualNative, "estimated_native": m.EstimatedNative, "cost_flag": m.CostFlag, "test": m.Test,
		"cost_schedule_id": m.CostScheduleID.String(), "endpoint_delisted": delisted,
	})
}

// judge compares the charge with the estimate: within the supplier's rounding plus the relative tolerance is "within",
// a charge above that is "under" (we estimated too little: the cost we know is not the cost), below it "over". An
// estimate that leaves out priced items or had no usage says nothing about the price, so it is "unjudged", and so does
// a test generation, whose estimate is a deliberate upper bound: a probe charges the platform, not a tenant, and its
// own price must never delist a payable endpoint (H-I9).
func judge(cfg Config, m store.MatchChargesRow) (string, error) {
	if m.CostFlag != "complete" || m.Test {
		return "unjudged", nil
	}
	actual, ok1 := new(big.Rat).SetString(m.ActualNative)
	estimated, ok2 := new(big.Rat).SetString(m.EstimatedNative)
	step, ok3 := granularity[m.NativeUnit]
	if !ok1 || !ok2 || !ok3 {
		return "unjudged", fmt.Errorf("amounts %q and %q in unit %q", m.ActualNative, m.EstimatedNative, m.NativeUnit)
	}
	tolerance := new(big.Rat).Add(step, new(big.Rat).Mul(estimated, cfg.Relative))
	diff := new(big.Rat).Sub(actual, estimated)
	switch {
	case diff.Cmp(tolerance) > 0:
		return "under", nil
	case new(big.Rat).Neg(diff).Cmp(tolerance) > 0:
		return "over", nil
	}
	return "within", nil
}

func event(ctx context.Context, tx store.DBTX, q *store.Queries, charge uuid.UUID, revision int32, payload map[string]any) error {
	b, err := jsonx.MarshalCanonical(payload)
	if err != nil {
		return err
	}
	if err := q.InsertChargeEvent(ctx, tx, store.InsertChargeEventParams{ChargeRecordID: charge, Revision: revision, Payload: b}); err != nil {
		return fmt.Errorf("record the event of charge %s: %w", charge, err)
	}
	return nil
}

// AssignToLostRequest attributes a held charge to a lost request, which billing takes as part of that request's estimate.
// It reports false when the charge is not held.
func AssignToLostRequest(ctx context.Context, tx store.DBTX, charge, lost uuid.UUID) (bool, error) {
	q := store.New()
	rows, err := q.AssignChargeToLostRequest(ctx, tx, store.AssignChargeToLostRequestParams{ID: charge, LostRequestID: &lost})
	if err != nil || len(rows) == 0 {
		return false, err
	}
	return true, event(ctx, tx, q, rows[0].ChargeID, rows[0].Revision, map[string]any{
		"attribution": "lost_request", "lost_request_id": lost.String(), "actual_native": rows[0].ActualNative,
	})
}

// AssignUnattributed settles a held charge as a cost of the supplier that no user pays for. It reports false when the
// charge is not held.
func AssignUnattributed(ctx context.Context, tx store.DBTX, charge uuid.UUID) (bool, error) {
	q := store.New()
	rows, err := q.AssignChargeUnattributed(ctx, tx, charge)
	if err != nil || len(rows) == 0 {
		return false, err
	}
	return true, event(ctx, tx, q, rows[0].ChargeID, rows[0].Revision, map[string]any{
		"attribution": "unattributed", "actual_native": rows[0].ActualNative,
	})
}
