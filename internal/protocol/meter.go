package protocol

import (
	"fmt"

	"axiom/internal/catalog"
)

// usageReport is a usage object mapped onto billing items (catalog README.md「计费项与用量」).
type usageReport struct {
	counts  [catalog.NumItems]int64
	seen    catalog.ItemSet
	tier    catalog.ServiceTier
	unknown []catalog.UnknownUsage
}

func (r *usageReport) set(it catalog.Item, n int64) {
	r.counts[it] = n
	r.seen.Add(it)
}

// meter keeps the reply's usage as reported so far. Counts only go up: a later 0 leaves the earlier count, as some
// relays clear the input in later events, and a smaller count makes the usage invalid (README.md「转发」).
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
	if m.invalid != nil {
		return
	}
	for it := range catalog.NumItems {
		n := r.counts[it]
		switch {
		case !r.seen.Has(it), n == 0 && m.counts[it] > 0:
		case n < m.counts[it]:
			m.invalid = fmt.Errorf("usage: %s went down from %d to %d", it, m.counts[it], n)
			return
		default:
			m.set(it, n)
		}
	}
	m.tier, m.unknown, m.compactions = r.tier, r.unknown, m.compactions[:0]
	for _, c := range compactions {
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
