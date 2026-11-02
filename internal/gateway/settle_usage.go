package gateway

import (
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/suppliers"
)

// addToolUsage adds the call's external searches and file parses to the user's side of the charged attempt after the
// supplier's cost was computed from the supplier's own usage. The work is ours, not the supplier's: one explicit zero
// report cannot hide it, and a retry does not put it in another attempt's supplier price. Only the last attempt is
// charged, with the loop it ends (billing.Settle), so the loop's first attempt carries the count (closeLoop). The two
// counts keep their own items: a parse is not a search and is not priced at the search item's rate.
func addToolUsage(rec *billing.AttemptResult, searches, parses int64) {
	if searches <= 0 && parses <= 0 || len(rec.Segments) == 0 {
		return
	}
	counts := make(map[catalog.Item]int64, catalog.NumItems)
	for it := range catalog.NumItems {
		counts[it] = rec.Segments[0].Usage.Count(it)
	}
	counts[catalog.ItemWebSearch] += searches
	counts[catalog.ItemFileParse] += parses
	rec.Segments[0].Usage = recountCapped(rec.Segments[0].Usage, counts)
}

// costFlag is what the supplier's row says of how far its estimate can be trusted, decided here, where the source of the
// usage is known (suppliers' Cost sees counts only): reconciliation judges a price only against a complete estimate. An
// attempt whose upstream reported no usage at all has none, whatever the estimates put in its place; one with some
// counts the upstream did not report, or that the schedule did not price, is unpriced; priced is the flag suppliers gave.
func costFlag(segments []billing.Segment, priced suppliers.CostFlag) suppliers.CostFlag {
	reported, guessed := false, false
	for _, seg := range segments {
		reported = reported || seg.Reported != 0
		for it := range catalog.NumItems {
			guessed = guessed || (seg.Usage.Count(it) > 0 && !seg.Reported.Has(it))
		}
	}
	switch {
	case !reported:
		return suppliers.CostNoUsage
	case priced != suppliers.CostComplete:
		return priced
	case guessed:
		return suppliers.CostUnpriced
	}
	return suppliers.CostComplete
}
