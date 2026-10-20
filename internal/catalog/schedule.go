package catalog

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

// Schedule is a price schedule: a unit price per billing item and service tier, some of which change with the length
// of the prompt. Official prices, supplier cost plans and billing's prices all use it and all store its canonical
// JSON.
type Schedule struct {
	currency platform.Currency
	basis    ItemSet // the items whose counts add up to the prompt length the thresholds compare
	above    []int64 // the context tiers' thresholds, strictly increasing, the same for every service tier
	levels   [numServiceTiers]level
	offered  set[ServiceTier] // the service tiers with prices; a parsed schedule always has the standard tier
}

// Currency is what the schedule's prices are in.
func (s Schedule) Currency() platform.Currency { return s.currency }

// level is one service tier's prices: its base prices, and for each threshold in above, the base prices it replaces
// when the basis adds up to more than the threshold, for the whole request; M1 supports no other method
// (README.md「条件」).
type level struct {
	base  rates
	tiers []rates
}

// rates are the unit prices a level or a tier sets. A zero price is a price; an item without one is unpriced.
type rates struct {
	price  [NumItems]platform.UnitPrice
	priced ItemSet
}

// The stored form, README.md「存储」. The top level holds the standard tier's prices; every other service tier has its
// own level with the same thresholds. Members are required except context_tiers and service_tiers; every rates must
// be given, though it may be empty, and every tier needs its above. Unknown members are refused, so a condition M1
// does not know is rejected rather than ignored.
type (
	scheduleJSON struct {
		ContextTiers *contextTiersJSON    `json:"context_tiers,omitzero"`
		Currency     string               `json:"currency"`
		Rates        map[string]string    `json:"rates"`
		ServiceTiers map[string]levelJSON `json:"service_tiers,omitzero"`
		V            int                  `json:"v"`
	}
	contextTiersJSON struct {
		Basis  []string   `json:"basis"`
		Method string     `json:"method"`
		Tiers  []tierJSON `json:"tiers"`
	}
	levelJSON struct {
		ContextTiers []tierJSON        `json:"context_tiers,omitzero"`
		Rates        map[string]string `json:"rates"`
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
	err := jsonx.UnmarshalKnown(b, &w)
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
	standard := levelJSON{Rates: w.Rates}
	if w.ContextTiers != nil {
		if s.basis, err = w.ContextTiers.parseBasis(); err != nil {
			return s, fmt.Errorf("context tiers: %w", err)
		}
		standard.ContextTiers = w.ContextTiers.Tiers
	}
	if s.above, s.levels[ServiceTierStandard], err = standard.parse(); err != nil {
		return s, err
	}
	s.offered.Add(ServiceTierStandard)
	for name, lw := range w.ServiceTiers {
		st, l, err := parseServiceTier(name, lw, s.above)
		if err != nil {
			return s, fmt.Errorf("service tier %q: %w", name, err)
		}
		s.levels[st] = l
		s.offered.Add(st)
	}
	return s, nil
}

// parseBasis refuses any method but whole, and a basis that is empty or adds up more than the prompt.
func (w *contextTiersJSON) parseBasis() (basis ItemSet, err error) {
	if w.Method != wholeRequest {
		return 0, fmt.Errorf("method %q is not supported, only %q", w.Method, wholeRequest)
	}
	if basis, err = parseSet[Item](itemNames[:], w.Basis); err != nil {
		return 0, fmt.Errorf("basis: %w", err)
	}
	if basis == 0 || len(w.Tiers) == 0 {
		return 0, errors.New("want a basis and at least one tier")
	}
	if basis&^InputSide != 0 {
		return 0, errors.New("basis: only input-side items count")
	}
	return basis, nil
}

// parseServiceTier refuses thresholds that differ from the standard tier's: a service tier that left out the
// long-context tier would price long requests at its base prices.
func parseServiceTier(name string, w levelJSON, standardAbove []int64) (st ServiceTier, l level, err error) {
	if st, err = ParseServiceTier(name); err != nil {
		return st, l, err
	}
	if st == ServiceTierStandard {
		return st, l, errors.New("the standard tier's prices are the top-level ones")
	}
	above, l, err := w.parse()
	if err == nil && !slices.Equal(above, standardAbove) {
		err = errors.New("thresholds differ from the standard tier's")
	}
	return st, l, err
}

// parse refuses a tier without a threshold rather than read it as 0, which would price every prompt at the tier, and
// missing rates rather than read them as pricing nothing.
func (w *levelJSON) parse() (above []int64, l level, err error) {
	if l.base, err = parseRates(w.Rates); err != nil {
		return nil, l, err
	}
	for i, t := range w.ContextTiers {
		if t.Above == nil || *t.Above < 0 {
			return nil, l, errors.New("context tiers: a tier's threshold must be given and non-negative")
		}
		if i > 0 && *t.Above <= above[i-1] {
			return nil, l, errors.New("context tiers: thresholds must strictly increase")
		}
		r, err := parseRates(t.Rates)
		if err != nil {
			return nil, l, fmt.Errorf("context tiers: %w", err)
		}
		above = append(above, *t.Above)
		l.tiers = append(l.tiers, r)
	}
	return above, l, nil
}

// parseRates needs no check for an item priced twice: the JSON decoder already refuses a duplicate member.
func parseRates(m map[string]string) (r rates, err error) {
	if m == nil {
		return r, errors.New("rates must be given")
	}
	for name, text := range m {
		it, err := lookup[Item](itemNames[:], name)
		if err != nil {
			return r, fmt.Errorf("rates: %w", err)
		}
		if r.price[it], err = platform.ParseUnitPrice(text); err != nil {
			return r, fmt.Errorf("rates: %s: %w", name, err)
		}
		r.priced.Add(it)
	}
	return r, nil
}

// parseSet reads a list of names into a set.
func parseSet[T ~uint8](names, list []string) (s set[T], err error) {
	for _, name := range list {
		v, err := lookup[T](names, name)
		if err != nil {
			return 0, err
		}
		if s.Has(v) {
			return 0, fmt.Errorf("%q is listed twice", name)
		}
		s.Add(v)
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

// priceUnits is the finest price a schedule holds: a UnitPrice counts 10⁻¹⁵ of a currency unit.
const priceUnits = 1_000_000_000_000_000

// Scaled is every price times multiplier, in currency: a sale price, which is a reference price times the site's
// multiplier (docs/domains/billing.md「定价」), or a supplier's cost plan in its own unit ("XXX"). Each price is rounded
// up at priceUnits, once: a multiplier is usually no finite decimal, and a price left too low is the mistake to avoid.
// An item without a price stays without one.
func (s Schedule) Scaled(multiplier string, currency platform.Currency) (Schedule, error) {
	m, ok := new(big.Rat).SetString(multiplier)
	if !ok || m.Sign() < 0 {
		return Schedule{}, fmt.Errorf("price schedule: %q is not a multiplier", multiplier)
	}
	out := s
	out.currency = currency
	for st := range numServiceTiers {
		if !s.offered.Has(st) {
			continue
		}
		l, err := s.levels[st].scaled(m)
		if err != nil {
			return Schedule{}, err
		}
		out.levels[st] = l
	}
	return out, nil
}

func (l *level) scaled(m *big.Rat) (out level, err error) {
	if out.base, err = l.base.scaled(m); err != nil {
		return level{}, err
	}
	out.tiers = make([]rates, len(l.tiers))
	for i, r := range l.tiers {
		if out.tiers[i], err = r.scaled(m); err != nil {
			return level{}, err
		}
	}
	return out, nil
}

func (r rates) scaled(m *big.Rat) (rates, error) {
	for it := range NumItems {
		if !r.priced.Has(it) {
			continue
		}
		p, err := scaledUnitPrice(r.price[it], m)
		if err != nil {
			return rates{}, err
		}
		r.price[it] = p
	}
	return r, nil
}

// scaledUnitPrice is p × m rounded up at priceUnits. A price past 1000 leaves the range a price has, which ParseUnitPrice
// reports: a multiplier that large is a mistake, not a price.
func scaledUnitPrice(p platform.UnitPrice, m *big.Rat) (platform.UnitPrice, error) {
	product := new(big.Rat).Mul(p.Rat(), m)
	units := new(big.Int).Mul(product.Num(), big.NewInt(priceUnits))
	var remainder big.Int
	units.QuoRem(units, product.Denom(), &remainder)
	if remainder.Sign() != 0 {
		units.Add(units, big.NewInt(1))
	}
	text := new(big.Rat).SetFrac(units, big.NewInt(priceUnits)).FloatString(15) // exact: the fraction is in priceUnits
	text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	return platform.ParseUnitPrice(text)
}

// MarshalJSON writes the canonical form (jsonx.MarshalCanonical): prices in UnitPrice's canonical text, lists in
// enumeration order.
func (s Schedule) MarshalJSON() ([]byte, error) {
	standard := s.levels[ServiceTierStandard]
	w := scheduleJSON{Currency: s.currency.String(), Rates: standard.base.json(), V: scheduleVersion}
	if len(s.above) > 0 {
		w.ContextTiers = &contextTiersJSON{Basis: s.basis.names(itemNames[:]), Method: wholeRequest, Tiers: s.tiersJSON(standard)}
	}
	for st := ServiceTierStandard + 1; st < numServiceTiers; st++ {
		if s.offered.Has(st) {
			if w.ServiceTiers == nil {
				w.ServiceTiers = make(map[string]levelJSON)
			}
			w.ServiceTiers[st.String()] = levelJSON{ContextTiers: s.tiersJSON(s.levels[st]), Rates: s.levels[st].base.json()}
		}
	}
	return jsonx.MarshalCanonical(w)
}

func (s Schedule) tiersJSON(l level) []tierJSON {
	var tiers []tierJSON
	for i, r := range l.tiers {
		tiers = append(tiers, tierJSON{Above: &s.above[i], Rates: r.json()})
	}
	return tiers
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
