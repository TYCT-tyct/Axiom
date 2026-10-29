package gateway

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
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

// precheck reads the count, and the call's session binding when there is one, in one batch before admission. A cache
// failure retains only the process count: a local sticky binding would disagree with other nodes, including after
// recovery.
func (h *Handler) precheck(ctx context.Context, site accounts.SiteID, dimension, localKey, subject string, call *Call, compacted bool) int {
	local := h.Limits.Rate.Count(localKey)
	if call != nil {
		call.accountRate = h.Limits.AccountRate.Count(accountRateKey(dimension, call.caller))
	}
	if h.KV == nil || !h.KV.Enabled() {
		if call != nil {
			call.rateReset = h.Limits.Rate.now().Add(2*h.Limits.cfg.RateWindow + time.Second - time.Nanosecond).Unix()
			if dimension == callDimension {
				call.sticky = h.stickyLocal(call.Session, compacted)
			}
		}
		return local
	}
	return h.sharedPrecheck(ctx, site, dimension, subject, call, compacted, local)
}

// sharedPrecheck sends the counter dimensions and optional sticky read as one cache operation.
func (h *Handler) sharedPrecheck(ctx context.Context, site accounts.SiteID, dimension, subject string, call *Call, compacted bool, local int) int {
	now := h.KV.Now()
	index, elapsed := h.Limits.Rate.slot(now)
	keys := h.rateKeys(site, dimension, subject, call, index)
	if call != nil {
		call.rateReset = now.Add(2*h.Limits.cfg.RateWindow + time.Second - time.Nanosecond).Unix()
	}
	var cmds [2]kv.Cmd
	cmds[0] = rateCounter.Cmd(keys, h.Limits.rateTTL)
	n := 1
	sticky := call != nil && dimension == callDimension
	if sticky {
		call.sticky = routing.EndpointID{}
		call.stickyCache, call.stickyKey = h.KV, routing.StickyKey(h.KV, uuid.UUID(site), call.Session)
		cmds[1] = kv.Get(call.stickyKey)
		if compacted {
			cmds[1] = kv.Delete(call.stickyKey)
		}
		n++
	}
	values, err := h.KV.Do(ctx, kv.Precheck, cmds[:n]...)
	if err != nil {
		return local
	}
	if sticky && !compacted {
		call.sticky = routing.StickyEndpoint(values[1])
	}
	counts, err := values[0].Ints()
	if err != nil || len(counts) != len(keys) {
		return local
	}
	if call != nil {
		call.accountRate = estimate(counts[2], counts[3], elapsed, h.Limits.cfg.RateWindow)
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

// rateKeys shares one backing string between the two windows of every dimension. Adding the account dimension
// otherwise allocates another string for every key and window number on each request.
func (h *Handler) rateKeys(site accounts.SiteID, dimension, subject string, call *Call, index int64) []string {
	prefix := h.KV.Key(kv.RateSpace, uuid.UUID(site))
	n := 2
	var account [32]byte
	if call != nil {
		n = 4
		id := uuid.UUID(call.caller.AccountID())
		hex.Encode(account[:], id[:])
	}
	var text strings.Builder
	text.Grow(n * (len(prefix) + len(dimension) + max(len(subject), len(account)) + len(":account_::") + 20))
	var ends [4]int
	var window [20]byte
	for i := range n {
		text.WriteString(prefix)
		text.WriteByte(':')
		if i >= 2 {
			text.WriteString("account_")
		}
		text.WriteString(dimension)
		text.WriteByte(':')
		if i >= 2 {
			text.Write(account[:])
		} else {
			text.WriteString(subject)
		}
		text.WriteByte(':')
		text.Write(strconv.AppendInt(window[:0], index-int64(i%2), 10))
		ends[i] = text.Len()
	}
	all := text.String()
	keys := make([]string, n)
	start := 0
	for i := range n {
		keys[i] = all[start:ends[i]]
		start = ends[i]
	}
	return keys
}

// keyHex is a key's ID as a cache key carries it: the 32 lowercase hex digits of its UUID, not the raw bytes the
// in-process limits use.
func keyHex(c accounts.Caller) string { return kv.Hex(uuid.UUID(c.KeyID())) }

func accountRateKey(dimension string, caller accounts.Caller) string {
	return dimension + accountKey(caller)
}

// judgeRate checks node protection before business limits against the single precheck snapshot.
func (h *Handler) judgeRate(a accounts.Admission, c *Call, dimension string) error {
	protection, refusal := h.Limits.cfg.KeyRate, errKeyRate
	if dimension == countDimension {
		protection, refusal = h.Limits.cfg.CountRate, errCountRate
	}
	account, key := a.RateLimits()
	for _, check := range [...]struct {
		count, limit int
		refusal      *Refusal
	}{
		{c.rate, protection, refusal}, {c.accountRate, account, errAccountRate}, {c.rate, key, errBusinessKeyRate},
	} {
		if err := h.checkRate(check.count, check.limit, check.refusal, c.rateReset); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) checkRate(count, limit int, refusal *Refusal, reset int64) error {
	if limit > 0 && count > limit {
		r := *refusal
		r.RateLimit, r.RateReset = limit, reset
		r.RetryAfter = seconds(2 * h.Limits.cfg.RateWindow)
		return &r
	}
	return nil
}
