package billing

import "axiom/internal/platform"

// charged is what the user is charged for: the last attempt, and when it continues a model-side loop, the samplings of
// every attempt of that loop with it, in order. The loop is one answer, so 免单规则 judges it by how its last attempt
// ended; a last sampling the supplier refused before running it still ends a loop whose earlier samplings and searches
// ran, which is a failure before the commit point, not a call that never ran.
func charged(attempts []AttemptResult) AttemptResult {
	last := attempts[len(attempts)-1]
	if !last.Continues {
		return last
	}
	first := len(attempts) - 1
	for first > 0 && attempts[first].Continues {
		first--
	}
	var segments []Segment
	for _, at := range attempts[first:] {
		segments = append(segments, at.Segments...)
	}
	last.Segments = segments
	if last.Result == NotExecuted {
		last.Result = FailedBeforeCommit
	}
	return last
}

// result is what a charge for the last attempt writes: the reservation is settled. A charge for a loop records no
// charged attempt: it is the usage of several, and a supplier's record of one of them cannot correct it (correct.go).
// Nor does a charge for an attempt numbered past platform.MaxAttempts, which only a loop's samplings before a fallback
// make: a correction worker of the release before loops drops such a number for good, so while both releases run a
// correction would be lost or made depending on which node took it. Recorded as none, no node corrects it.
func result(p priced, last AttemptResult) (written, error) {
	usage, err := encodeUsage(last.Segments)
	w := written{
		state: "settled", seq: int32p(last.Seq), entry: int32p(last.Entry), price: p.price, waived: p.waived,
		estimated: p.estimated, usage: usage, tiers: p.tiers,
	}
	if last.Continues || last.Seq > platform.MaxAttempts {
		w.seq = nil
	}
	return w, err
}
