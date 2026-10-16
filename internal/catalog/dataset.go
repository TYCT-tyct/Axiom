package catalog

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"axiom/internal/platform/jsonx"
)

// The datasets whose prices a model can follow are the price sources litellm and openrouter. Their prices are in USD.
const datasetCurrency = "USD"

// Entry is what a dataset says about one model, read but not yet matched to a model of ours.
type Entry struct {
	Key   string   // the dataset's own name for it
	Names []string // the model names it may stand for, as the dataset writes them
	Text  bool     // the model takes and gives text only
	Raw   jsonx.Raw
	// Err says why the entry's prices cannot be used; the entry still matches by name, so that a model that follows
	// the dataset is held with this reason instead of looking as if the dataset lacked it.
	Err   error
	sheet sheet
}

// levelPrices are the prices of each service tier.
type levelPrices [numServiceTiers]map[Item]string

// sheet is an entry's prices before they are a schedule: per service tier, the base prices and the prices that
// replace them above a threshold, as plain decimal text. A threshold says where a tier starts, not what counts toward
// it, so the datasets give no basis and a schedule can only be made from a sheet next to a current schedule.
type sheet struct {
	base  levelPrices
	tiers map[int64]*levelPrices
}

func (sh *sheet) set(st ServiceTier, above int64, it Item, price string) {
	prices := &sh.base
	if above != 0 {
		if sh.tiers == nil {
			sh.tiers = map[int64]*levelPrices{}
		}
		if sh.tiers[above] == nil {
			sh.tiers[above] = new(levelPrices)
		}
		prices = sh.tiers[above]
	}
	if prices[st] == nil {
		prices[st] = map[Item]string{}
	}
	prices[st][it] = price
}

// covers reports whether sh gives every price o gives, at the same value.
func (sh *sheet) covers(o sheet) bool {
	for st := range o.base {
		if !gives(sh.base[st], o.base[st]) {
			return false
		}
	}
	for above, want := range o.tiers {
		var have levelPrices
		if sh.tiers[above] != nil {
			have = *sh.tiers[above]
		}
		for st := range want {
			if !gives(have[st], want[st]) {
				return false
			}
		}
	}
	return true
}

func gives(have, want map[Item]string) bool {
	for it, price := range want {
		if have[it] != price {
			return false
		}
	}
	return true
}

// ErrDatasetTiers is why a dataset's prices that change with the prompt length cannot be used: the datasets give where a
// tier starts but not which usage adds up to it.
var ErrDatasetTiers = errors.New("the entry has context tiers that differ from the current price's, or there is none to take the threshold basis from")

// schedule makes the entry's schedule. Tiers are taken only when their thresholds are exactly the current schedule's,
// and then with its basis; otherwise ErrDatasetTiers, and the model keeps its price until a person publishes the new one.
func (e Entry) schedule(current *Schedule) (Schedule, error) {
	if e.Err != nil {
		return Schedule{}, e.Err
	}
	if len(e.sheet.base[ServiceTierStandard]) == 0 {
		return Schedule{}, errors.New("the entry has no standard price")
	}
	w := scheduleJSON{V: scheduleVersion, Currency: datasetCurrency, Rates: ratesJSON(e.sheet.base[ServiceTierStandard])}
	thresholds := slices.Sorted(maps.Keys(e.sheet.tiers))
	if len(thresholds) > 0 {
		if current == nil || current.basis == 0 || !slices.Equal(current.above, thresholds) {
			return Schedule{}, ErrDatasetTiers
		}
		w.ContextTiers = &contextTiersJSON{
			Basis: current.basis.names(itemNames[:]), Method: wholeRequest, Tiers: e.sheet.tiersJSON(ServiceTierStandard, thresholds),
		}
	}
	w.ServiceTiers = e.sheet.serviceTiersJSON(thresholds)
	b, err := jsonx.MarshalCanonical(w)
	if err != nil {
		return Schedule{}, err
	}
	s, err := ParseSchedule(b)
	if err != nil {
		return Schedule{}, fmt.Errorf("the entry's prices: %w", err)
	}
	return s, nil
}

// tiersJSON is the service tier's prices above each threshold; a threshold where it has none is there with no rates, as
// every service tier has the standard tier's thresholds.
func (sh *sheet) tiersJSON(st ServiceTier, thresholds []int64) []tierJSON {
	if len(thresholds) == 0 {
		return nil
	}
	tiers := make([]tierJSON, len(thresholds))
	for i, above := range thresholds {
		tiers[i] = tierJSON{Above: &thresholds[i], Rates: ratesJSON(sh.tiers[above][st])}
	}
	return tiers
}

// serviceTiersJSON is the levels other than the standard one that have any price.
func (sh *sheet) serviceTiersJSON(thresholds []int64) map[string]levelJSON {
	var levels map[string]levelJSON
	for st := ServiceTierStandard + 1; st < numServiceTiers; st++ {
		l := levelJSON{Rates: ratesJSON(sh.base[st]), ContextTiers: sh.tiersJSON(st, thresholds)}
		if len(l.Rates) == 0 && !slices.ContainsFunc(l.ContextTiers, func(t tierJSON) bool { return len(t.Rates) > 0 }) {
			continue
		}
		if levels == nil {
			levels = map[string]levelJSON{}
		}
		levels[serviceTierNames[st]] = l
	}
	return levels
}

func ratesJSON(m map[Item]string) map[string]string {
	out := make(map[string]string, len(m))
	for it, price := range m {
		out[itemNames[it]] = price
	}
	return out
}
