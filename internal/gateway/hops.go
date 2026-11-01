package gateway

import (
	"math"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/suppliers"
)

// recordHops gives suppliers one cost and receipt per supplier request. A request that failed before a Relay has no
// segments, but it still gets a row: the supplier may have charged it, and losing its reference would lose the only
// key reconciliation has. The aggregate Estimate and Cost are sums of the hop results; they are never repriced under
// the last receipt, which would charge a per-request price once instead of once per hop.
func (a *attempt) recordHops(rec *billing.AttemptResult) bool {
	if len(a.receipts) == 0 || len(a.hopEnds) > len(a.receipts) {
		return false
	}
	rec.CostKnown = true
	rec.Estimate = suppliers.AttemptCost{Currency: a.entry.Price.Schedule.Currency()}
	from, first := 0, true
	for i, receipt := range a.receipts {
		hop, ok := a.hopResult(i, receipt, &from)
		if !ok {
			return false
		}
		rec.Hops = append(rec.Hops, hop)
		rec.Cost = addCredits(rec.Cost, hop.Cost)
		rec.CostKnown = rec.CostKnown && hop.CostKnown
		if first {
			rec.Estimate = hop.Estimate
			first = false
		} else if !mergeCost(&rec.Estimate, hop.Estimate) {
			rec.CostKnown = false
		}
	}
	return true
}

// hopResult prices the i-th supplier request. A request that reached no Relay has no segments; its failure tells
// whether the supplier refused it outright (nothing to charge) or may have run it (price the known part, keep the
// attempt's profit unknown).
func (a *attempt) hopResult(i int, receipt suppliers.AttemptReceipt, from *int) (billing.AttemptHop, bool) {
	receipt = receipt.WithExchange(a.entry.Exchange)
	var (
		segs    []billing.Segment
		c       suppliers.AttemptCost
		credits int64
		known   bool
	)
	if i < len(a.hopEnds) {
		end := a.hopEnds[i]
		if end < *from || end > len(a.hops) {
			return billing.AttemptHop{}, false
		}
		segs = a.hops[*from:end]
		*from = end
		usage := make([]catalog.Usage, len(segs))
		for j, seg := range segs {
			usage[j] = seg.Usage
		}
		var err error
		c, credits, known, err = a.s.x.Costs.Credits(receipt, usage, a.entry.Price.Schedule.Currency())
		if err != nil {
			c.Flag, known = suppliers.CostUnpriced, false
		}
		c.Flag = costFlag(segs, c.Flag)
	} else if a.failure != nil && a.failure.Executed == suppliers.NotExecuted {
		c = suppliers.AttemptCost{
			Conversion: suppliers.ConversionNotRun,
			Currency:   a.entry.Price.Schedule.Currency(),
			Flag:       suppliers.CostComplete,
		}
		known = true
	} else {
		var err error
		c, credits, _, err = a.s.x.Costs.Credits(receipt, nil, a.entry.Price.Schedule.Currency())
		if err != nil {
			c.Flag = suppliers.CostUnpriced
		}
		// Usage that never reached a Relay may still have been charged; a zero estimate is not a cost known to be zero.
		known = false
	}
	return billing.AttemptHop{
		Segments: segs, Receipt: receipt, Estimate: c, Cost: credits, CostKnown: known, FinishedAt: time.Now(),
	}, true
}

// addCredits saturates a sum of credits rather than wrapping it. The domain cannot reach the bound, but a cost is
// money and a silent wrap would be worse than an obviously impossible figure.
func addCredits(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

// mergeCost folds one hop's cost into the attempt's summary. It reports whether the native sum fit; a summed flag is
// the weakest of the parts, because one hop that could not be priced makes the total an estimate too.
func mergeCost(dst *suppliers.AttemptCost, src suppliers.AttemptCost) bool {
	dst.Missing |= src.Missing
	dst.Unknown = dst.Unknown || src.Unknown
	dst.Conversion = mergeConversion(dst.Conversion, src.Conversion)
	switch {
	case dst.Flag == suppliers.CostUnpriced || src.Flag == suppliers.CostUnpriced:
		dst.Flag = suppliers.CostUnpriced
	case dst.Flag == suppliers.CostComplete || src.Flag == suppliers.CostComplete:
		dst.Flag = suppliers.CostComplete
	default:
		dst.Flag = suppliers.CostNoUsage
	}
	n, err := dst.Native.Add(src.Native)
	if err != nil {
		dst.Conversion = suppliers.ConversionOverflow
		return false
	}
	dst.Native = n
	return true
}

// mergeConversion keeps the state of the whole sum: a hop that did not run is neutral, and a conversion that failed
// on any hop describes the attempt better than one that succeeded elsewhere.
func mergeConversion(a, b suppliers.Conversion) suppliers.Conversion {
	if a == suppliers.ConversionUnset {
		return b
	}
	if b == suppliers.ConversionUnset || a == b {
		return a
	}
	if a == suppliers.ConversionNotRun {
		return b
	}
	if b == suppliers.ConversionNotRun {
		return a
	}
	for _, c := range []suppliers.Conversion{
		suppliers.ConversionUnpriced, suppliers.ConversionOverflow, suppliers.ConversionMissingRate, suppliers.ConversionConverted,
	} {
		if a == c || b == c {
			return c
		}
	}
	return suppliers.ConversionSame
}
