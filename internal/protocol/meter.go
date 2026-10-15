package protocol

import (
	"fmt"
	"slices"
	"strings"

	"axiom/internal/catalog"
)

// usageReport is a usage object mapped onto billing items (catalog README.md「计费项与用量」). tierSet tells a tier the
// object reports from one it leaves out.
type usageReport struct {
	counts  [catalog.NumItems]int64
	seen    catalog.ItemSet
	tier    catalog.ServiceTier
	tierSet bool
	unknown []catalog.UnknownUsage
}

// maxCount bounds a count as catalog.NewUsage does, checked before any arithmetic on counts, so none overflows.
const maxCount = 1e10

// countOK reports whether n is a count a report may hold.
func countOK(n int64) bool { return 0 <= n && n <= maxCount }

func (r *usageReport) set(it catalog.Item, n int64) {
	r.counts[it] = n
	r.seen.Add(it)
}

// sortUnknown orders the unknown usage by field, so a report does not depend on the order of a map.
func (r *usageReport) sortUnknown() {
	slices.SortFunc(r.unknown, func(a, b catalog.UnknownUsage) int { return strings.Compare(a.Field, b.Field) })
}

// meter keeps the reply's usage as reported so far, merged field by field: what a report leaves out keeps its earlier
// value, so a tier or an unknown count reported once stays. Counts only go up: a later 0 leaves the earlier count, as
// some relays clear the input in later events, and a smaller count makes the usage invalid (README.md「转发」). tier
// starts as the request's.
type meter struct {
	usageReport
	compactions []Segment
	final       bool // the format's last usage event has come
	invalid     error
	scratch     map[catalog.Item]int64 // reused, so building a catalog.Usage allocates only the map's first time
}

// finalOnSight are the items whose first report is final: the input is read before the reply starts.
const finalOnSight = catalog.ItemSet(1<<catalog.ItemInput | 1<<catalog.ItemCacheRead | 1<<catalog.ItemCacheWrite |
	1<<catalog.ItemCacheWrite1h)

// report takes a usage object: the reply's counts, and the compactions it lists, each its own segment.
func (m *meter) report(r usageReport, compactions []usageReport) {
	if m.invalid != nil || !m.takeCounts(r) {
		return
	}
	if r.tierSet {
		m.tier = r.tier
	}
	for _, u := range r.unknown {
		if i := slices.IndexFunc(m.unknown, func(v catalog.UnknownUsage) bool { return v.Field == u.Field }); i >= 0 {
			m.unknown[i] = u
		} else {
			m.unknown = append(m.unknown, u)
		}
	}
	m.sortUnknown()
	if compactions != nil {
		m.compact(compactions)
	}
}

// takeCounts takes r's counts, reporting false when one went down, which makes the usage invalid.
func (m *meter) takeCounts(r usageReport) bool {
	for it := range catalog.NumItems {
		n := r.counts[it]
		switch {
		case !r.seen.Has(it), n == 0 && m.counts[it] > 0:
		case n < m.counts[it]:
			m.invalid = fmt.Errorf("usage: %s went down from %d to %d", it, m.counts[it], n)
			return false
		default:
			m.set(it, n)
		}
	}
	return true
}

// compact replaces the compactions with the ones a report lists, at the reply's tier unless they name their own.
func (m *meter) compact(compactions []usageReport) {
	m.compactions = m.compactions[:0]
	for _, c := range compactions {
		if !c.tierSet {
			c.tier = m.tier
		}
		u, err := m.usage(c)
		if err != nil {
			m.invalid = err
			return
		}
		m.compactions = append(m.compactions, Segment{Usage: u, Reported: c.seen})
	}
}

// progress is the reply's counts reported so far, and which of them are final: the input side once reported,
// everything after the last usage event; none when the usage is invalid.
func (m *meter) progress() (Segment, catalog.ItemSet) {
	u, err := m.usage(m.usageReport)
	if err != nil && m.invalid == nil {
		m.invalid = err
	}
	final := m.seen & finalOnSight
	switch {
	case m.invalid != nil:
		return Segment{Usage: u}, 0
	case m.final:
		final = m.seen
	}
	return Segment{Usage: u, Reported: m.seen}, final
}

// result is the attempt's usage, in which only final counts are reported: after a stream that broke off, billing
// estimates the output rather than taking a lower bound of it for exact.
func (m *meter) result() Usage {
	reply, final := m.progress()
	reply.Reported = final
	u := Usage{Segments: append([]Segment{reply}, m.compactions...), Invalid: m.invalid}
	if !m.final || u.Invalid != nil { // compactions come with the last usage event
		for i := range u.Segments[1:] {
			u.Segments[1+i].Reported = 0
		}
	}
	return u
}

func (m *meter) usage(r usageReport) (catalog.Usage, error) {
	if m.scratch == nil {
		m.scratch = make(map[catalog.Item]int64, catalog.NumItems)
	}
	clear(m.scratch)
	for it := range catalog.NumItems {
		if r.seen.Has(it) {
			m.scratch[it] = r.counts[it]
		}
	}
	return catalog.NewUsage(m.scratch, r.tier, r.unknown)
}
