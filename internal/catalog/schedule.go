package catalog

import (
	"errors"
	"fmt"
	"slices"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

// Schedule is a price schedule: a unit price per billing item, some of which change with the length of the prompt.
// Official prices, supplier cost plans and billing's prices all use it and all store its canonical JSON.
type Schedule struct {
	currency platform.Currency
	base     rates
	basis    ItemSet       // the items whose counts add up to the prompt length the tiers compare
	tiers    []contextTier // thresholds strictly increase
}

// contextTier replaces the base prices it lists when the basis adds up to more than above, for the whole request:
// M1 supports no other method (README.md「条件」).
type contextTier struct {
	above int64
	rates rates
}

// rates are the unit prices a schedule or a tier sets. A zero price is a price; an item without one is unpriced.
type rates struct {
	price  [NumItems]platform.UnitPrice
	priced ItemSet
}

// The stored form, README.md「存储」. Members are required except rates, which may price nothing, and context_tiers;
// within a tier, above and a non-empty rates are required. Unknown members are refused, so a condition M1 does not
// know is rejected rather than ignored.
type (
	scheduleJSON struct {
		ContextTiers *contextTiersJSON `json:"context_tiers,omitzero"`
		Currency     string            `json:"currency"`
		Rates        map[string]string `json:"rates"`
		V            int               `json:"v"`
	}
	contextTiersJSON struct {
		Basis  []string   `json:"basis"`
		Method string     `json:"method"`
		Tiers  []tierJSON `json:"tiers"`
	}
	tierJSON struct {
		Above *int64            `json:"above"` // nil when left out or null
		Rates map[string]string `json:"rates"`
	}
)

const (
	scheduleVersion = 1
	wholeRequest    = "whole" // the only tier method: the whole request is priced at the tier
)

// ParseSchedule reads a stored or submitted schedule. Members may come in any order and prices in any decimal spelling;
// MarshalJSON writes the canonical form.
func ParseSchedule(b []byte) (Schedule, error) {
	var w scheduleJSON
	err := jsonx.UnmarshalStrict(b, &w)
	var s Schedule
	if err == nil {
		s, err = w.schedule()
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("price schedule: %w", err)
	}
	return s, nil
}

func (w *scheduleJSON) schedule() (s Schedule, err error) {
	if w.V != scheduleVersion {
		return s, fmt.Errorf("version %d, want %d", w.V, scheduleVersion)
	}
	if s.currency, err = platform.ParseCurrency(w.Currency); err != nil {
		return s, err
	}
	if s.base, err = parseRates(w.Rates); err != nil {
		return s, err
	}
	if w.ContextTiers != nil {
		s.basis, s.tiers, err = w.ContextTiers.parse()
	}
	return s, err
}

func (w *contextTiersJSON) parse() (basis ItemSet, tiers []contextTier, err error) {
	if w.Method != wholeRequest {
		return 0, nil, fmt.Errorf("context tiers: method %q is not supported, only %q", w.Method, wholeRequest)
	}
	if basis, err = parseSet[Item](itemNames[:], w.Basis); err != nil {
		return 0, nil, fmt.Errorf("context tiers: basis: %w", err)
	}
	if basis == 0 || len(w.Tiers) == 0 {
		return 0, nil, errors.New("context tiers: want a basis and at least one tier")
	}
	if basis&^inputSide != 0 {
		return 0, nil, errors.New("context tiers: basis: only input-side items count")
	}
	tiers = make([]contextTier, len(w.Tiers))
	for i, t := range w.Tiers {
		if tiers[i], err = t.parse(); err != nil {
			return 0, nil, fmt.Errorf("context tiers: %w", err)
		}
		if i > 0 && tiers[i].above <= tiers[i-1].above {
			return 0, nil, errors.New("context tiers: thresholds must strictly increase")
		}
	}
	return basis, tiers, nil
}

// parse refuses a tier without a threshold rather than read it as 0, which would price every prompt at the tier, and
// a tier without prices, which changes nothing.
func (t *tierJSON) parse() (tier contextTier, err error) {
	if t.Above == nil || *t.Above < 0 {
		return tier, errors.New("a tier's threshold must be given and non-negative")
	}
	if tier.rates, err = parseRates(t.Rates); err != nil {
		return tier, err
	}
	if tier.rates.priced == 0 {
		return tier, errors.New("a tier lists no price")
	}
	tier.above = *t.Above
	return tier, nil
}

// parseRates needs no check for an item priced twice: the JSON decoder already refuses a duplicate member.
func parseRates(m map[string]string) (r rates, err error) {
	for name, text := range m {
		it, err := lookup[Item](itemNames[:], name)
		if err != nil {
			return r, fmt.Errorf("rates: %w", err)
		}
		if r.price[it], err = platform.ParseUnitPrice(text); err != nil {
			return r, fmt.Errorf("rates: %s: %w", name, err)
		}
		r.priced.add(it)
	}
	return r, nil
}

func parseSet[T ~uint8](names, list []string) (s set[T], err error) {
	for _, name := range list {
		v, err := lookup[T](names, name)
		if err != nil {
			return 0, err
		}
		if s.Has(v) {
			return 0, fmt.Errorf("%q is listed twice", name)
		}
		s.add(v)
	}
	return s, nil
}

func lookup[T ~uint8](names []string, name string) (T, error) {
	i := slices.Index(names, name)
	if i < 0 {
		return 0, fmt.Errorf("unknown name %q", name)
	}
	return T(i), nil
}

// MarshalJSON writes the canonical form (jsonx.MarshalCanonical): prices in UnitPrice's canonical text, lists in
// enumeration order.
func (s Schedule) MarshalJSON() ([]byte, error) {
	w := scheduleJSON{
		Currency: s.currency.String(),
		Rates:    s.base.json(),
		V:        scheduleVersion,
	}
	if len(s.tiers) > 0 {
		w.ContextTiers = &contextTiersJSON{Basis: s.basis.names(itemNames[:]), Method: wholeRequest}
		for _, t := range s.tiers {
			w.ContextTiers.Tiers = append(w.ContextTiers.Tiers, tierJSON{Above: &t.above, Rates: t.rates.json()})
		}
	}
	return jsonx.MarshalCanonical(w)
}

func (s set[T]) names(all []string) []string {
	var names []string
	for i, name := range all {
		if s.Has(T(i)) {
			names = append(names, name)
		}
	}
	return names
}

func (r rates) json() map[string]string {
	m := make(map[string]string)
	for it := range NumItems {
		if r.priced.Has(it) {
			m[it.String()] = r.price[it].String()
		}
	}
	return m
}
