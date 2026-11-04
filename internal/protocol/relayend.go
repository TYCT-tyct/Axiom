package protocol

import (
	"bytes"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol/internal/sse"
)

// Final is what the ending reports: the usage and cost settled and whether that usage was estimated, or, when the
// settlement could not be written, Pending and no cost (docs/domains/gateway.md「用量与费用」).
type Final struct {
	Usage    Usage
	Cost     platform.Exact
	Currency platform.Currency
	// ServerToolCost is the settled sale price of the server tools the call ran, nil when none ran. It is a price the
	// client paid, not what the tools cost us.
	ServerToolCost *platform.Exact
	// CacheDiscount is what cache reads saved against the input price.
	CacheDiscount platform.Exact
	// Files are the files the gateway parsed, which an aggregated Chat reply returns as annotations (fileparse.go).
	Files              []FileAnnotation
	Estimated, Pending bool
	// IsBYOK says the attempt that served the call ran on the account's own provider key. It describes the account,
	// so a pending settlement still reports it (docs/domains/gateway.md「用量与费用」).
	IsBYOK bool
}

// Finish returns the ending after the upstream's normal end: the withheld events, the held ending event with the
// cost of f, or for a format whose usage event the relay writes, one with the usage and cost of f, the events without
// content that followed it, and the upstream's last event. It returns nil unless Next returned StepUpstreamDone and no
// ending was written. Like Fail, it builds the ending from bytes already charged and then gives them all back, so it
// charges nothing; a relay that aggregates keeps them charged until Release, for the body it returned.
func (r *Relay) Finish(f Final) []byte {
	if r.state != upstreamDone {
		return nil
	}
	usage, top := cost(&f, r.client), ""
	if f.Pending {
		top = `,"axiom":{"billing_pending":true}`
	}
	out := append(r.endOut(), r.withheld...)
	if r.format.rendersUsage {
		out = r.up.usageEvent(out, reply(f.Usage), usage, top)
	} else {
		out = sse.AppendEvent(out, r.endingName, r.costed(usage, top))
	}
	out = append(out, r.trailing...)
	if r.stop != nil {
		out = sse.AppendEvent(out, r.stopName, r.stop)
	}
	if r.agg != nil {
		return r.finishAggregate(out, f)
	}
	r.state = ended
	r.Release()
	return out
}

// costed is the held ending with the members usage added to its usage object and top to the event: an ending
// without a usage object gets one.
func (r *Relay) costed(usage, top string) []byte {
	at := r.usageAt
	if at < 0 { // no usage object: the ending gets one
		at = bytes.LastIndexByte(r.ending, '}')
		usage = `,"usage":{` + strings.TrimPrefix(usage, ",") + "}"
	} else if bytes.HasSuffix(bytes.TrimRight(r.ending[:at], " \t\r\n"), []byte("{")) {
		// The usage object is empty: the members take no leading comma.
		usage = strings.TrimPrefix(usage, ",")
	}
	last := bytes.LastIndexByte(r.ending, '}') // the data is a JSON object, which ends with its brace
	out := make([]byte, 0, len(r.ending)+len(usage)+len(top))
	out = append(out, r.ending[:at]...)
	out = append(out, usage...)
	out = append(out, r.ending[at:last]...)
	out = append(out, top...)
	return append(out, r.ending[last:]...)
}

// reply is the reply's segment of u.
func reply(u Usage) catalog.Usage {
	if len(u.Segments) == 0 {
		return catalog.Usage{}
	}
	return u.Segments[0].Usage
}

// Fail returns an error ending with the usage and cost of f, or, when f is nil because the settlement was not
// written, with the usage reported so far and no cost. It returns nil once an ending was written.
// A relay that aggregates keeps its charge past Fail too (Release).
func (r *Relay) Fail(e Error, f *Final) []byte {
	if r.state == ended {
		return nil
	}
	u := r.Usage()
	if f != nil {
		u = f.Usage
	}
	var out []byte
	switch {
	case r.agg != nil:
		out = r.failAggregate(e, f, u)
	case r.up != nil: // nil for a pair of formats not relayed
		out = r.up.failure(r.endOut(), e, reply(u), cost(f, r.client))
	}
	r.state = ended
	if r.agg == nil {
		r.Release()
	}
	return out
}

// endOut is out ready for an ending: still holding the events before the commit point when there was none.
func (r *Relay) endOut() []byte {
	if r.committed {
		return r.out[:0]
	}
	return r.out
}
