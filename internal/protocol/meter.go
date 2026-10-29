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
	counts        [catalog.NumItems]int64
	seen          catalog.ItemSet
	tier          catalog.ServiceTier
	tierSet       bool
	unknown       []catalog.UnknownUsage
	native        catalog.NativeTokens
	nativeSet     nativeSet
	physicalInput tokenTotal
	outputTotal   tokenTotal
	// inputDetailed and outputDetailed tell that the report carried the side's details object, so its split is a new
	// snapshot even when its total is the same as before.
	inputDetailed, outputDetailed bool
}

// tokenTotal is the prompt or output total an upstream reports, and whether it reported one: an explicit 0 is not an
// absent total. It is a value, not a pointer, so that reading a report allocates nothing.
type tokenTotal struct {
	n   int64
	set bool
}

// nativeSet marks each native split a report carried. They are merged field by field: a later output frame may carry
// prediction tokens while saying nothing about the video input an earlier input frame reported.
type nativeSet uint8

const (
	nativeVideo nativeSet = 1 << iota
	nativeAccepted
	nativeRejected
)

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
// value, so a tier or an unknown count reported once stays. Counts only go up, except normalized OpenAI breakdowns
// are replaced together, when the report carries the side's details or a larger total; a report of the same total
// without details leaves the earlier split. A later 0 total leaves the earlier snapshot, as some relays clear the
// input in later events, and a smaller count makes the usage invalid (README.md「转发」). tier starts as the request's.
type meter struct {
	usageReport
	compactions        []Segment
	final              bool            // the format's last usage event has come
	trusted            catalog.ItemSet // items explicitly stated in a terminal failure frame
	trustedCompactions bool            // compactions explicitly listed by that frame
	invalid            error
	scratch            map[catalog.Item]int64 // reused, so building a catalog.Usage allocates only the map's first time
}

// The meter is kept through the settlement, so what it holds is bounded and charged up front as meterMemory: at most
// maxUnknown unknown fields for the reply and as many over its compactions, each a field and a value of at most
// maxUnknownLen bytes, and at most maxCompactions compactions. A report past them makes the usage invalid, which
// billing estimates, as it does any usage it cannot take.
const (
	maxUnknown     = 16
	maxUnknownLen  = 128
	maxCompactions = 8
)

// report takes a usage object: the reply's counts, and the compactions it lists, each its own segment.
func (m *meter) report(r usageReport, compactions []usageReport) {
	if m.invalid == nil {
		m.invalid = m.bound(r, compactions)
	}
	if m.invalid != nil || !m.takeCounts(r) {
		return
	}
	if r.tierSet {
		m.tier = r.tier
	}
	if r.nativeSet&nativeVideo != 0 {
		m.native.VideoInput, m.nativeSet = r.native.VideoInput, m.nativeSet|nativeVideo
	}
	if r.nativeSet&nativeAccepted != 0 {
		m.native.AcceptedPrediction, m.nativeSet = r.native.AcceptedPrediction, m.nativeSet|nativeAccepted
	}
	if r.nativeSet&nativeRejected != 0 {
		m.native.RejectedPrediction, m.nativeSet = r.native.RejectedPrediction, m.nativeSet|nativeRejected
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

// take marks only the items and compactions explicitly reported by a terminal failure as final. Earlier output
// counts remain lower bounds; previously final input stays final (docs/domains/billing.md「用量来源与暂估」).
func (m *meter) take(r usageReport, compactions []usageReport, final bool) {
	m.report(r, compactions)
	if final {
		m.trusted |= r.seen
		m.trustedCompactions = compactions != nil
	}
}

// bound reports a report that would take the meter past its bounds.
func (m *meter) bound(r usageReport, compactions []usageReport) error {
	if len(compactions) > maxCompactions {
		return fmt.Errorf("usage: %d compactions, more than %d", len(compactions), maxCompactions)
	}
	fields, over := len(m.unknown), 0
	for _, u := range r.unknown {
		if !slices.ContainsFunc(m.unknown, func(v catalog.UnknownUsage) bool { return v.Field == u.Field }) {
			fields++
		}
	}
	for _, c := range compactions {
		over += len(c.unknown)
	}
	if fields > maxUnknown || over > maxUnknown {
		return fmt.Errorf("usage: more than %d unknown fields", maxUnknown)
	}
	long := func(c usageReport) bool {
		return slices.ContainsFunc(c.unknown, func(u catalog.UnknownUsage) bool {
			return len(u.Field) > maxUnknownLen || len(u.Value) > maxUnknownLen
		})
	}
	if long(r) || slices.ContainsFunc(compactions, long) {
		return fmt.Errorf("usage: an unknown field or its value is longer than %d bytes", maxUnknownLen)
	}
	return nil
}

// takeCounts takes r's counts, reporting false when one went down, which makes the usage invalid.
func (m *meter) takeCounts(r usageReport) bool {
	if !m.takeSide(r.physicalInput, r.inputDetailed, &m.physicalInput, catalog.InputSide, r) ||
		!m.takeSide(r.outputTotal, r.outputDetailed, &m.outputTotal, catalog.OutputSide, r) {
		return false
	}
	for it := range catalog.NumItems {
		if r.physicalInput.set && catalog.InputSide.Has(it) || r.outputTotal.set && catalog.OutputSide.Has(it) {
			continue // a normalized snapshot must not retain a stale remainder or modality count
		}
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

// takeSide replaces an OpenAI breakdown together: later cache or image details can reduce ordinary tokens to zero
// without reducing the physical total, and merging their maxima would charge those tokens twice. A report of the same
// total without the side's details object says nothing about the split, so the earlier one stays: replacing it would
// turn cached tokens back into ordinary ones. A larger total is a new snapshot, details or not.
func (m *meter) takeSide(total tokenTotal, detailed bool, kept *tokenTotal, side catalog.ItemSet, r usageReport) bool {
	if !total.set {
		return true
	}
	if kept.set {
		switch {
		case total.n == 0 && kept.n > 0:
			return true // some relays clear totals in later events
		case total.n < kept.n:
			m.invalid = fmt.Errorf("usage: physical token total went down from %d to %d", kept.n, total.n)
			return false
		case total.n == kept.n && !detailed:
			return true
		}
	}
	*kept = total
	m.seen = m.seen&^side | r.seen&side
	for it := range catalog.NumItems {
		if side.Has(it) {
			m.counts[it] = r.counts[it]
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

// progress is the reply's counts reported so far, and which of them are final: the input side once reported, what the
// frame that ended the stream stated, everything after the format's last usage event; none when the usage is invalid.
func (m *meter) progress() (Segment, catalog.ItemSet) {
	u, err := m.usage(m.usageReport)
	if err != nil && m.invalid == nil {
		m.invalid = err
	}
	final := m.seen & (catalog.InputSide | m.trusted) // the input is read before the reply starts, so its first report is final
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
	if !m.final && !m.trustedCompactions || u.Invalid != nil {
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
	u, err := catalog.NewUsage(m.scratch, r.tier, r.unknown)
	if err == nil && r.physicalInput.set {
		u, err = u.WithPhysicalInput(r.physicalInput.n)
	}
	if err == nil && r.nativeSet != 0 {
		u, err = u.WithNativeTokens(r.native)
	}
	return u, err
}
