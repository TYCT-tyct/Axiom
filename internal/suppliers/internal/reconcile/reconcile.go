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
}

// DefaultConfig is 30 minutes of grace and a tolerance of 1%.
func DefaultConfig() Config { return Config{Grace: 30 * time.Minute, Relative: big.NewRat(1, 100)} }

// granularity is the most the supplier's own rounding can move one charge, in its native unit: new-api rounds each
// charge to a whole quota (22.5 is charged as 23); sub2api writes ten decimals of a dollar.
var granularity = map[string]*big.Rat{"quota": big.NewRat(1, 1), "usd": big.NewRat(1, 10000000000)}

// Report is what one run did and what needs a person.
type Report struct {
	Matched    int
	Held       []uuid.UUID // charges moved to held in this run
	Delisted   []uuid.UUID // endpoints whose cost proved lower than the supplier's charge
	Over       []uuid.UUID // charges lower than the estimate by more than the tolerance: only reported
	Unmatched  []Unmatched // attempts that reached the supplier and are still without a charge after the grace
	Violations []string    // attempts the verdict could not be made for
}

// Unmatched is an attempt with a key and no charge.
type Unmatched struct {
	Request uuid.UUID
	Seq     int16
	Ref     string
}

// Ingest stores the charge records of a page of the scan. A record already stored stays as it was.
func Ingest(ctx context.Context, tx store.DBTX, supplier uuid.UUID, charges []adapter.Charge) error {
	rows := make([]store.ChargeRow, 0, len(charges))
	for _, c := range charges {
		usage, err := usageJSON(c.Usage)
		if err != nil {
			return err
		}
		funding := "other"
		if c.Funding == adapter.FundingWallet {
			funding = "wallet"
		}
		rows = append(rows, store.ChargeRow{
			KeyRef: string(c.Key), ChargeRef: string(c.Ref), OccurredAt: c.OccurredAt, AmountNative: string(c.Amount),
			Usage: usage, Funding: funding, UpstreamModel: c.Model,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return store.InsertChargeRecords(ctx, tx, supplier, rows)
}

// usageJSON is a charge's usage as stored: counts by item name, and the tier.
func usageJSON(u catalog.Usage) ([]byte, error) {
	items := map[string]int64{}
	for it := range catalog.NumItems {
		if n := u.Count(it); n != 0 {
			items[it.String()] = n
		}
	}
	return jsonx.MarshalCanonical(map[string]any{"items": items, "tier": u.ServiceTier().String()})
}

// Run makes one pass: matches what can be matched, holds the charges that waited past the grace, and lists the attempts
// that did too. It is safe to run twice at once, and again after a crash: a match is made once and an event is kept
// once per revision.
func Run(ctx context.Context, tx store.DBTX, now time.Time, cfg Config) (Report, error) {
	q := store.New()
	var report Report
	matches, err := q.MatchCharges(ctx, tx)
	if err != nil {
		return report, fmt.Errorf("match charges: %w", err)
	}
	for _, m := range matches {
		if err := settleMatch(ctx, tx, q, cfg, m, &report); err != nil {
			return report, err
		}
	}
	report.Matched = len(matches)
	held, err := q.HoldOverdueCharges(ctx, tx, now.Add(-cfg.Grace))
	if err != nil {
		return report, fmt.Errorf("hold overdue charges: %w", err)
	}
	for _, h := range held {
		if err := event(ctx, tx, q, h.ChargeID, h.Revision, map[string]any{"state": "held", "actual_native": h.ActualNative}); err != nil {
			return report, err
		}
		report.Held = append(report.Held, h.ChargeID)
	}
	waiting, err := q.ListUnmatchedAttempts(ctx, tx, now.Add(-cfg.Grace))
	if err != nil {
		return report, fmt.Errorf("list unmatched attempts: %w", err)
	}
	for _, w := range waiting {
		report.Unmatched = append(report.Unmatched, Unmatched{Request: w.RequestID, Seq: w.AttemptSeq, Ref: *w.ChargeRef})
	}
	return report, nil
}

// settleMatch judges a match against the tolerance, delists an endpoint whose estimate was too low, and tells billing.
func settleMatch(ctx context.Context, tx store.DBTX, q *store.Queries, cfg Config, m store.MatchChargesRow, report *Report) error {
	verdict, err := judge(cfg, m)
	if err != nil {
		report.Violations = append(report.Violations, fmt.Sprintf("charge %s: %v", m.ChargeID, err))
	}
	switch verdict {
	case "under":
		if n, err := q.DelistEndpoint(ctx, tx, m.EndpointID); err != nil {
			return fmt.Errorf("delist endpoint: %w", err)
		} else if n > 0 {
			report.Delisted = append(report.Delisted, m.EndpointID)
		}
	case "over":
		report.Over = append(report.Over, m.ChargeID)
	}
	return event(ctx, tx, q, m.ChargeID, m.Revision, map[string]any{
		"attribution": "attempt", "request_id": m.RequestID.String(), "attempt_seq": m.AttemptSeq, "verdict": verdict,
		"actual_native": m.ActualNative, "estimated_native": m.EstimatedNative, "cost_flag": m.CostFlag,
	})
}

// judge compares the charge with the estimate: within the supplier's rounding plus the relative tolerance is "within",
// a charge above that is "under" (we estimated too little: the cost we know is not the cost), below it "over". An
// estimate that leaves out priced items or had no usage says nothing about the price, so it is "unjudged".
func judge(cfg Config, m store.MatchChargesRow) (string, error) {
	if m.CostFlag != "complete" {
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
