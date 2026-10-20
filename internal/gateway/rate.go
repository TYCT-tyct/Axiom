package gateway

import (
	"context"
	"strconv"
	"sync"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/kv"
	"axiom/internal/routing"
)

// The rate dimensions of README「键」: a key's calls and its count_tokens requests have an allowance each.
const (
	callDimension  = "key"
	countDimension = "count"
)

// RateCounter is the precheck's rate count (docs/domains/routing.md「容量感知」): it counts and never judges, and
// nothing is released. The estimate over the last window is this window's count plus the share of the previous
// window's that the sliding window still covers. Two maps rotate with the window, so the memory held is bounded by
// the keys of two windows however many cold keys pass.
type RateCounter struct {
	mu        sync.Mutex
	window    time.Duration
	now       func() time.Time // time.Now, and a test's clock so that the count can be compared with the shared one
	start     time.Time        // when cur began
	cur, prev map[string]int
}

// NewRateCounter counts over a sliding window of the given length.
func NewRateCounter(window time.Duration) *RateCounter {
	return &RateCounter{window: window, now: time.Now, start: time.Now(), cur: map[string]int{}}
}

// Count adds one for key and returns the estimated count over the last window, this call included.
func (r *RateCounter) Count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if elapsed := now.Sub(r.start); elapsed >= r.window {
		r.prev = r.cur
		if elapsed >= 2*r.window {
			r.prev = nil // nothing in the previous window is in the sliding one
		}
		r.cur = map[string]int{}
		r.start = r.start.Add(elapsed / r.window * r.window)
	}
	r.cur[key]++
	return estimate(int64(r.cur[key]), int64(r.prev[key]), now.Sub(r.start), r.window)
}

// slot is which window an instant falls in, and how much of it has passed. The number is counted from the epoch, so
// every node with the same clock computes the same boundary (README「限流计数」).
func (r *RateCounter) slot(now time.Time) (index int64, elapsed time.Duration) {
	length := r.window.Milliseconds()
	millis := now.UnixMilli()
	return millis / length, time.Duration(millis%length) * time.Millisecond
}

// rateScript is the count script: it adds to this window's key and reads the one before, so the caller weights the two
// the way RateCounter does. The first write of a key gives it its expiry, in the same script, so no key outlives the two
// windows it is read in (README「限流计数」「不变量」).
const rateScript = `
local counts = {}
for i = 1, #KEYS, 2 do
  local current = redis.call('INCR', KEYS[i])
  if current == 1 then redis.call('PEXPIRE', KEYS[i], ARGV[1]) end
  local previous = redis.call('GET', KEYS[i + 1]) or 0
  counts[#counts + 1] = current
  counts[#counts + 1] = tonumber(previous)
end
return counts`

// rateCounter is the script as kv runs it: EVAL every time, because a NOSCRIPT answer would be a second round trip
// (README「接口」).
var rateCounter = kv.NewScript(rateScript)

// estimate is the count a limit is judged against: this window's count plus the share of the previous window's that the
// sliding window still covers. Both implementations end here, so the shared and the in-process count are one formula.
func estimate(current, previous int64, elapsed, window time.Duration) int {
	carried := float64(previous) * (1 - float64(elapsed)/float64(window))
	return int(float64(current) + carried)
}

// countRate is the count the limit is judged against: the shared one while Valkey answers, and this node's own while it
// does not. The node's own counter is advanced on every request either way, so a fallback starts from the traffic the
// node has already seen rather than from zero (README「限流计数」).
func (h *Handler) countRate(ctx context.Context, site accounts.SiteID, dimension, localKey, subject string) int {
	return h.precheck(ctx, site, dimension, localKey, subject, nil, false)
}

// precheck reads the count, and the call's session binding when there is one, in one batch before admission. A cache
// failure retains only the process count: a local sticky binding would disagree with other nodes, including after
// recovery.
func (h *Handler) precheck(ctx context.Context, site accounts.SiteID, dimension, localKey, subject string, call *Call, compacted bool) int {
	local := h.Limits.Rate.Count(localKey)
	if h.KV == nil || !h.KV.Enabled() {
		if call != nil {
			call.sticky = h.stickyLocal(call.Session, compacted)
		}
		return local
	}
	index, elapsed := h.Limits.Rate.slot(h.KV.Now())
	keys := []string{h.rateKey(site, dimension, subject, index), h.rateKey(site, dimension, subject, index-1)}
	var cmds [2]kv.Cmd
	cmds[0] = rateCounter.Cmd(keys, h.Limits.rateTTL)
	n := 1
	if call != nil {
		// One store for the whole call, its eventual write included: the shared one, never the process table.
		call.stickyCache, call.stickyKey = h.KV, routing.StickyKey(h.KV, uuid.UUID(site), call.Session)
		cmds[1] = kv.Get(call.stickyKey)
		if compacted {
			cmds[1] = kv.Delete(call.stickyKey)
		}
		n++
	}
	values, err := h.KV.Do(ctx, kv.Precheck, cmds[:n]...)
	if err != nil {
		return local // the breaker and the deadline have already counted it: the limit falls back, and never to a refusal
	}
	if call != nil && !compacted {
		call.sticky = routing.StickyEndpoint(values[1])
	}
	counts, err := values[0].Ints()
	if err != nil || len(counts) != 2 {
		return local
	}
	return estimate(counts[0], counts[1], elapsed, h.Limits.cfg.RateWindow)
}

// stickyLocal is the binding of single-core mode: the process table, forgotten first when the client says its context
// was compacted, since the cache it stuck for is gone.
func (h *Handler) stickyLocal(session routing.SessionKey, compacted bool) routing.EndpointID {
	if compacted {
		h.Router.Forget(session)
	}
	endpoint, _ := h.Router.Stuck(session)
	return endpoint
}

// rateKey is one window's key for a subject: ax:v1:rate:<site>:<dimension>:<subject>:<window> (README「键」).
func (h *Handler) rateKey(site accounts.SiteID, dimension, subject string, index int64) string {
	return h.KV.Key(kv.RateSpace, uuid.UUID(site), dimension, subject, strconv.FormatInt(index, 10))
}

// keyHex is a key's ID as a cache key carries it: the 32 lowercase hex digits of its UUID, not the raw bytes the
// in-process limits use.
func keyHex(c accounts.Caller) string { return kv.Hex(uuid.UUID(c.KeyID())) }
