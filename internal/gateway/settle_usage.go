package gateway

import (
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/suppliers"
)

// addToolUsage adds the call's external searches and file parses to the user's side of the charged attempt after the
// supplier's cost was computed from the supplier's own usage. The work is ours, not the supplier's: one explicit zero
// report cannot hide it, and a retry does not put it in another attempt's supplier price. Only the last attempt is
// charged, with the loop it ends (billing.Settle), so the loop's first attempt carries the count (closeLoop). While the
// catalog switch is off a parse keeps the old web_search unit; when it is on, pages are file_parse and cloudflare-ai is
// free. A parse is never silently mixed into the search count when the new unit is on.
func addToolUsage(rec *billing.AttemptResult, searches, parses, pages int64, fileBilling bool) {
	if !fileBilling {
		pages = 0
	} else {
		parses = 0
	}
	if searches <= 0 && parses <= 0 && pages <= 0 || len(rec.Segments) == 0 {
		return
	}
	counts := make(map[catalog.Item]int64, catalog.NumItems)
	for it := range catalog.NumItems {
		counts[it] = rec.Segments[0].Usage.Count(it)
	}
	counts[catalog.ItemWebSearch] += searches + parses
	counts[catalog.ItemFileParse] += pages
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
