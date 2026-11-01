package billing

import (
	"math"
	"slices"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// Progress is what one attempt has shown so far; gateway copies it from protocol.Progress after every read.
type Progress struct {
	ContentBytes   int64 // the decoded text, reasoning and tool arguments of complete events, in UTF-8 bytes
	PartialBytes   int64 // the raw bytes of the event still being read
	Searches       int64 // the web search events seen
	ToolCalls      int64 // the client tool calls seen, whose framework the output bound adds
	ToolCallTokens int64 // the tokens each tool call's framework takes in the attempt's family
	Reported       catalog.Usage
	Final          catalog.ItemSet // the reported items whose counts are final
}

// MeterStep tells the meter's owner what to do before the next read: start Extend with Request, stop reading until
// the extension in flight returns, or both, when one read crossed the extension point and the segment together. Left
// is how many more output tokens the approved segments cover, which bounds how long the owner may stop reading
// (internal/gateway/README.md「计量与追加」): math.MaxInt64 for an attempt reserved in full, or once the approved
// segments cover the output limit, which the upstream does not pass, or when output costs nothing, so that no amount of
// it spends a segment. So Left is 0 only while an extension is due or in flight, and that is what the owner waits for.
type MeterStep struct {
	Extend    bool
	Request   ExtendRequest
	Exhausted bool
	Left      int64
}

// ExtendRequest asks for segment Seq of the entry: the cumulative amount Target, which Extend writes if it exceeds
// what is reserved. It is a value, so the extension in flight never sees the meter move on.
type ExtendRequest struct {
	Seq    int
	Entry  int
	Target platform.Exact
}

// Meter meters one attempt against the segments approved for its entry (README.md「计量」). It belongs to the
// attempt's owner goroutine and does not allocate.
type Meter struct {
	q       quote
	entry   int
	trigger int64 // per mille of a segment
	seq     int
	ceiling ceiling        // what the approved segments cover
	next    ceiling        // what segment seq+1 covers once approved
	request ExtendRequest  // the request for segment seq+1, resent unchanged until it succeeds
	asked   bool           // request is set
	flying  bool           // request is in flight
	stop    platform.Exact // spend past it exhausts the approved segments
	ask     platform.Exact // spend past it asks for the next segment
	done    completed      // the final usage of the samplings before the current one
}

// completed is what the hops that already ended spent. A model-side loop reads a fresh relay per hop, whose progress
// starts at zero; without this the meter would compare every hop with the first segment alone.
type completed struct {
	contentBytes, searches, toolCalls, output int64
}

// Complete records the final progress of one hop. The caller invokes it once, after the hop has ended; the next hop's
// Observe adds its own progress to it. A hop without a final output count falls back to the byte bound, which is what
// Observe would have used for it.
func (m *Meter) Complete(p Progress) {
	out := p.Reported.OutputTotal()
	if !p.Final.Has(catalog.ItemOutput) {
		out = max(out, add(add(p.ContentBytes, p.PartialBytes), mul(max(p.ToolCalls, 0), max(p.ToolCallTokens, 0))))
	}
	m.done.contentBytes += p.ContentBytes
	m.done.searches += max(p.Searches, p.Reported.Count(catalog.ItemWebSearch))
	m.done.toolCalls += p.ToolCalls
	m.done.output += out
}

// OutputCap is the most output tokens the attempt can have: the entry's output limit on each choice. Metering holds the
// output to it, and so must any estimate of what the upstream did not report; it is one value for both.
func (m *Meter) OutputCap() int64 { return m.q.outputCap }

// ceiling is what a run of segments covers: output tokens up to tokens, of which the last segment began at start, and
// web searches beyond the quote up to excess.
type ceiling struct{ tokens, start, excess int64 }

// Bind returns the meter of an attempt on a kept entry. Binding an entry the quote dropped is a bug in the caller:
// nothing was reserved for it.
func (r *Reservation) Bind(entry int) *Meter {
	if !slices.Contains(r.Kept, entry) {
		panic("billing: Bind of an entry the reservation dropped")
	}
	q := r.quotes[entry]
	m := &Meter{q: q, entry: entry, trigger: r.trigger, seq: 1, ceiling: ceiling{tokens: min(q.outputCap, q.segment)}}
	m.limits()
	return m
}

// limits prices the approved ceiling: the spend that exhausts it, and the spend past the extension point, which is
// the drawn share of the current segment or, once the output limit is covered, only searches beyond it.
func (m *Meter) limits() {
	c := m.ceiling
	m.stop = sum(m.q.output.Mul(c.tokens), m.searchCost(c.excess))
	point := add(c.start, mul(m.q.segment, m.trigger)/1000) // rounded down: early, never late
	if c.tokens >= m.q.outputCap {
		point = c.tokens
	}
	m.ask = sum(m.q.output.Mul(point), m.searchCost(c.excess))
}

// searchCost is what searches beyond the quote cost: each one's fee and the samplings it adds, each rereading up to
// the whole context. A saturated count only arises far beyond any balance, where Exhausted stops the attempt anyway.
func (m *Meter) searchCost(excess int64) platform.Exact {
	return sum(m.q.search.Mul(excess), m.q.resample.Mul(mul(excess, m.q.rereads)))
}

// Observe meters the progress and returns what the owner must do. Input-side counts are exact once reported, but
// they never move the meter: the quote holds every sampling's input at its bound, and a count below the bound frees
// nothing, since a later sampling may still reread the whole context. Output is the larger of the reported count and
// the byte bound plus each tool call's framework until the count is final, and at most the output limit; searches are
// counted by event.
func (m *Meter) Observe(p Progress) MeterStep {
	if m.q.segment == 0 {
		return MeterStep{Left: math.MaxInt64} // reserved in full: never extended, never truncated
	}
	out, excess := m.used(p)
	spend := sum(m.q.output.Mul(out), m.searchCost(excess))
	step := MeterStep{Left: max(m.ceiling.tokens-out, 0)}
	if m.ceiling.tokens >= m.q.outputCap || m.q.output.Cmp(platform.UnitPrice{}) == 0 {
		step.Left = math.MaxInt64
	}
	if !m.flying && (m.asked || spend.Cmp(m.ask) > 0) {
		if !m.asked {
			c := m.ceiling
			start := max(c.tokens, out)
			m.next = ceiling{tokens: min(add(start, m.q.segment), m.q.outputCap), start: start, excess: max(c.excess, excess)}
			m.request = ExtendRequest{
				Seq: m.seq + 1, Entry: m.entry,
				Target: sum(m.q.fixed, m.q.allowance, m.q.output.Mul(m.next.tokens), m.searchCost(m.next.excess)),
			}
			m.asked = true
		}
		m.flying, step.Extend, step.Request = true, true, m.request
	}
	step.Exhausted = spend.Cmp(m.stop) > 0
	return step
}

func (m *Meter) used(p Progress) (out, excess int64) {
	// Each relay reports only its own hop. A completed hop's final token count is added as it is; its byte bound must
	// not be compared with the current hop's bytes, or hidden reasoning and visible output would be mixed.
	current := p.Reported.OutputTotal()
	if !p.Final.Has(catalog.ItemOutput) {
		current = max(current, add(add(p.ContentBytes, p.PartialBytes),
			mul(max(p.ToolCalls, 0), max(p.ToolCallTokens, 0))))
	}
	out = m.done.output + current
	searches := m.done.searches + max(p.Searches, p.Reported.Count(catalog.ItemWebSearch))
	return min(out, m.q.outputCap), max(searches-m.q.searches, 0)
}

// Extended applies the outcome of the extension the last step asked for, seq being its Seq. A failure leaves the
// approved segments as they were, and the next Observe resends the same request: the failed one may have committed
// with only its acknowledgement lost, and Extend then reports success for that segment number, so its target must not
// have moved. Need beyond it waits for the next segment number.
func (m *Meter) Extended(seq int, err error) {
	if !m.flying || seq != m.request.Seq {
		panic("billing: Extended without that extension in flight")
	}
	m.flying = false
	if err == nil {
		m.seq, m.ceiling, m.asked = seq, m.next, false
		m.limits()
	}
}
