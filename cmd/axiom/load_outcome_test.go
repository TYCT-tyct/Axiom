package main

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// outcome is one call as the client saw it, joined with what the upstream saw of it. The client's clock and the
// upstream's are the same process's, so the two can be subtracted.
type outcome struct {
	id        uint64
	request   string // the node's id for the call (X-Generation-Id), which is its reservation's
	group     string // what the scenario tells its calls apart by, such as the size of the request
	start     time.Time
	status    int           // 0 when the request got no answer
	first     time.Duration // from the start to the first byte of the body; 0 when none came
	eof       time.Time     // when the body ended
	done      bool          // the stream was read to message_stop
	pending   bool          // the ending says the call's cost is pending: its settlement did not land in time
	cancelled bool          // the client cancelled it on purpose
	bodyBytes int
	err       string
	up        stamp
	seen      bool // up is what the upstream saw; false when the request never reached it
}

func (o outcome) refused() bool {
	return o.status == http.StatusTooManyRequests || o.status == http.StatusServiceUnavailable
}

// lost is a stream the scenario ended on purpose: its client cancelled it or the upstream cut it.
func (o outcome) lost() bool { return o.cancelled || (o.seen && o.up.cut) }

// overhead is what the first byte took beyond the upstream's own time to it: the request's way in (key, admission,
// routing, the request to the upstream) and the first event's way out. It is the TTFT the gateway adds, plus the loopback
// and the scheduling of a machine that also runs the client, the upstream and the database.
func (o outcome) overhead() (time.Duration, bool) {
	if o.status != http.StatusOK || o.first <= 0 || !o.seen || o.up.firstWrite.IsZero() {
		return 0, false
	}
	return o.first - o.up.firstWrite.Sub(o.up.received), true
}

// settle is the time from the upstream's last byte to the client's: the relay holds the closing events, which carry the
// cost, until the settlement is committed (internal/gateway/README.md invariant I3), so this is that settlement and
// whatever waited in front of it.
func (o outcome) settle() (time.Duration, bool) {
	if !o.done || !o.seen || o.up.endWrite.IsZero() {
		return 0, false
	}
	return max(0, o.eof.Sub(o.up.endWrite)), true
}

// summarize is the table row of outs.
func summarize(name string, outs []outcome) loadRow {
	row := loadRow{Scenario: name, Calls: len(outs)}
	for _, o := range outs {
		switch {
		case o.done:
			row.Done++
		case o.refused():
			row.Refused++
		case o.lost():
			row.Lost++
		default:
			row.Failed++
		}
		if d, ok := o.overhead(); ok {
			row.Overhead = append(row.Overhead, d)
			row.TTFT = append(row.TTFT, o.first)
		}
		if d, ok := o.settle(); ok {
			row.Settle = append(row.Settle, d)
		}
	}
	row.Wait = waitNote(outs)
	return row
}

// waitNote is the queueing the calls met: how many the node turned away for want of room, how many were slow to their
// first byte though the upstream was not, and how many ended without their cost.
func waitNote(outs []outcome) string {
	var busy, overloaded, slow, pending int
	for _, o := range outs {
		switch o.status {
		case http.StatusTooManyRequests:
			busy++
		case http.StatusServiceUnavailable:
			overloaded++
		}
		if d, ok := o.overhead(); ok && d > time.Second {
			slow++
		}
		if o.pending {
			pending++
		}
	}
	return fmt.Sprintf("429: %d, 503: %d, >1s: %d, cost pending: %d", busy, overloaded, slow, pending)
}

// failures says what went wrong in the calls that failed, the most common first: the harness's evidence for a bug report.
func failures(outs []outcome) string {
	counts := map[string]int{}
	for _, o := range outs {
		if !o.done && !o.refused() && !o.lost() {
			counts[fmt.Sprintf("status %d %s", o.status, o.err)]++
		}
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	slices.SortFunc(reasons, func(a, b string) int { return counts[b] - counts[a] })
	for i, reason := range reasons {
		reasons[i] = fmt.Sprintf("%d x %s", counts[reason], strings.TrimSpace(reason))
	}
	return strings.Join(reasons[:min(len(reasons), 5)], "; ")
}

// maxOutputTokens is the max_tokens of every call of the load, and so the most output any reservation holds.
const maxOutputTokens = 8192

// chargeBounds is what the ledger must have charged for outs: exactly the known price for each call that ended with its
// cost, and for each other call that was admitted anything up to the most it could have reserved (the request's input at
// the supplier's price, and the output tokens the load's requests allow).
func chargeBounds(outs []outcome) (low, high int64) {
	var unfinished int64
	for _, o := range outs {
		switch {
		case o.done && o.seen: // by the time the ledger is checked, a pending cost has been settled as well
			low += loadCharge(o.up.deltas)
		case o.status == http.StatusOK:
			unfinished += int64(o.bodyBytes/4+1)*inputNano + maxOutputTokens*outputNano
		}
	}
	return low, low + unfinished
}

// unknownKey is key with its last characters replaced by others of the same kind: it has the shape of a real key and is
// no one's, so the node must ask its database to learn that.
const (
	digits = "0123456789"
	lower  = "abcdefghijklmnopqrstuvwxyz"
	upper  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

func unknownKey(key string, pick func(n int) int) string {
	b := []byte(key)
	for i := max(0, len(b)-16); i < len(b); i++ {
		switch c := b[i]; {
		case strings.IndexByte(digits, c) >= 0:
			b[i] = digits[pick(len(digits))]
		case strings.IndexByte(lower, c) >= 0:
			b[i] = lower[pick(len(lower))]
		case strings.IndexByte(upper, c) >= 0:
			b[i] = upper[pick(len(upper))]
		}
	}
	return string(b)
}

// chargeMismatches compares what the ledger charged each call, by the node's id for it, with what the call was worth, for
// every call whose content completed with the usage the upstream reported, one that ended without its cost included:
// its settlement landed in the background, and must have landed whole. A call with no row is a mismatch, as a zero and
// a double charge are; per call, so that one call's missing charge cannot be made up by another's extra.
func chargeMismatches(outs []outcome, charged map[string]int64) []string {
	var wrong []string
	for _, o := range outs {
		if !o.done || !o.seen || o.request == "" {
			continue
		}
		got, ok := charged[o.request]
		if want := loadCharge(o.up.deltas); !ok || got != want {
			wrong = append(wrong, fmt.Sprintf("call %s charged %d (row found: %v), want %d", o.request, got, ok, want))
		}
	}
	return wrong
}

// unexpected is why a scenario's calls fail its acceptance: a call that ended in none of the ways a load expects. A call
// the node refused for lack of room (429, 503), one the scenario cancelled and one the upstream cut are expected; an
// answer of 500, a stream that stopped short for no reason given, a connection that failed are not.
func (r loadRow) unexpected(outs []outcome) error {
	if r.Failed == 0 {
		return nil
	}
	return fmt.Errorf("%d of %d calls failed in none of the expected ways: %s", r.Failed, r.Calls, failures(outs))
}
