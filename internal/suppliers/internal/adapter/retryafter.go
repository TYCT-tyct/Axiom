package adapter

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// retryAfterBound only keeps an absurd Retry-After from overflowing a Duration; how long a route may be paused is
// routing's rule (Router.Block).
const retryAfterBound = 24 * time.Hour

// RetryAfter reads the wait a Retry-After header states, in either HTTP form: delta-seconds or a date. A missing,
// malformed, negative or past value states no wait and is zero. Seconds are bounded before they become a Duration, so
// an overflowing integer cannot wrap into a short or negative wait.
func RetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v != "" && strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n > uint64(retryAfterBound/time.Second) {
			return retryAfterBound
		}
		return time.Duration(n) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		return min(max(time.Until(at), 0), retryAfterBound)
	}
	return 0
}
