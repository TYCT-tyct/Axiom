package protocol

import "bytes"

// What a conversion holds at once is bounded by what it is given. It copies the body's strings a few times over, and
// reads them into structures, one for every object or null in an array of them and, in an object whose members it does
// not know, one for every member; a body of nothing but "{}," or "null," makes 248 bytes of message of every 3 or 5.
// The body does not say how many there are, but counting its braces, nulls and colons bounds them (any other element
// of such an array is refused where it stands): those inside strings only add. The constants are
// the structures' sizes, doubled for the growth of the slices they sit in, with room to spare (measured 2026-10-16, a
// body of "{}," allocates 1.3 KiB for each object, and 100,000 unknown members 485 bytes each); TestPrepareCost holds
// them to what bodies of the worst shapes allocate.
const (
	convertPerByte   = 6
	convertPerObject = 2048
	convertPerMember = 1024
)

// PrepareCost is the most memory Prepare holds at once for a request, besides the body itself, so that the node's
// budget is asked for it before Prepare allocates it. A rewrite writes into one buffer whose capacity is known before
// it starts, and unquotes an identifier or a response ID into a scratch, copies it to a string and grows the scratch by
// doubling, which the body's size bounds three times over (2.5 measured, for an identifier of escapes); a conversion's
// working set is bounded by the body's size, braces and colons (above). What stays is the capacity of Upstream.Body,
// which is never more than the cost.
func PrepareCost(r *Request, body []byte, t Target, w Rewrites) int64 {
	n := int64(len(body))
	if r.Format == t.Format {
		cost := int64(rewriteSize(rulesFor(r.Format, t), len(body), t, w)) + 3*n + replayCost(r.Format, body)
		if r.Format == Responses {
			cost += replayHistoryCost(w.History)
		}
		if r.Format == Chat && r.legacy {
			cost += nativeRequestCost(body)
		}
		return cost
	}
	if r.Format != Chat && t.Format != Chat {
		// The request is read into the canonical model and written for the other format: the reader's working set
		// (nativeRequestCost) and the writer's (a conversion's) are the two stages' peaks, and the Chat encoding the
		// first writes is alive while the second reads it. Objects and members are counted twice for that reason.
		objects := int64(bytes.Count(body, []byte("{")) + bytes.Count(body, []byte("null")))
		colons := int64(bytes.Count(body, []byte(":")))
		cost := nativeRequestCost(body) + int64(quoted(len(t.Model))) +
			convertPerByte*n + convertPerObject*objects*2 + convertPerMember*colons*2
		if r.Format == Responses { // the history is read as input items, each canonicalized
			cost += replayHistoryCost(w.History)
		}
		return cost
	}
	objects := bytes.Count(body, []byte("{")) + bytes.Count(body, []byte("null")) // a null element is a zero structure too
	if t.Format == Chat {
		cost := nativeRequestCost(body)
		for _, item := range w.History {
			cost += nativeRequestCost(item)
		}
		return cost + int64(quoted(len(t.Model))+3*quoted(w.withDefaults().IdentityLen))
	}
	cost := convertPerByte*n + convertPerObject*int64(objects) + convertPerMember*int64(bytes.Count(body, []byte(":")))
	return cost
}

// quoted is the most bytes a string of n bytes takes as a JSON string: the quotes, and six bytes for each byte that
// needs escaping, the longest escape there is.
func quoted(n int) int { return 6*n + len(`""`) }
