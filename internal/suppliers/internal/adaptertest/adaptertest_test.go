package adaptertest_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
)

// reference is a small adapter that follows every rule; the broken ones below each change one thing.
type reference struct {
	adapter.Adapter
	kind     adapter.Kind
	buckets  []adapter.BucketSpec
	classify func(status int, h http.Header, body []byte) adapter.Failure
	formats  func(adapter.GroupFacts) []protocol.Format
	ref      func(http.Header) (adapter.ChargeRef, bool)
}

func (r reference) Kind() adapter.Kind                                { return r.kind }
func (r reference) Buckets() []adapter.BucketSpec                     { return r.buckets }
func (r reference) Calls() adapter.CallQuirks                         { return r }
func (reference) Policy() adapter.CallPolicy                          { return adapter.CallPolicy{Streams: true} }
func (r reference) Formats(g adapter.GroupFacts) []protocol.Format    { return r.formats(g) }
func (r reference) ChargeRef(h http.Header) (adapter.ChargeRef, bool) { return r.ref(h) }
func (r reference) Classify(status int, h http.Header, body []byte) adapter.Failure {
	return r.classify(status, h, body)
}

// good classifies by status the way a careful adapter does: pages are blocks at the egress, a 429 is a rate limit, any
// other 4xx is the request's fault and refused before running, and anything else is unknown.
func good(status int, _ http.Header, body []byte) adapter.Failure {
	switch {
	case strings.HasPrefix(string(body), "<!DOCTYPE html"):
		return adapter.Failure{Scope: adapter.ScopeEgress, Cause: adapter.Unknown{Status: status, SuspectedEgressBlock: true}}
	case status == http.StatusTooManyRequests:
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeEndpoint, Cause: adapter.RateLimited{RetryAfter: 7 * time.Second}}
	case status >= 500:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.UpstreamError{Status: status}}
	case status == http.StatusBadRequest:
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	default:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: status}}
	}
}

func goodAdapter() reference {
	return reference{
		kind:     "reference",
		buckets:  []adapter.BucketSpec{{Name: "global", Window: time.Minute, Limit: 10}},
		classify: good,
		formats:  func(adapter.GroupFacts) []protocol.Format { return []protocol.Format{protocol.Chat} },
		ref: func(h http.Header) (adapter.ChargeRef, bool) {
			v := h.Get("X-Request-ID")
			return adapter.ChargeRef(v), v != ""
		},
	}
}

func TestAReferenceAdapterPasses(t *testing.T) {
	t.Parallel()
	adaptertest.Run(t, goodAdapter())
}

// TestTheSuiteCatchesEachBrokenRule: an adapter that breaks one rule fails, naming it.
func TestTheSuiteCatchesEachBrokenRule(t *testing.T) {
	t.Parallel()
	override := func(change func(*reference)) reference {
		a := goodAdapter()
		change(&a)
		return a
	}
	cases := []struct {
		name string
		a    reference
		want string
	}{
		{"a kind that cannot be stored", override(func(a *reference) { a.kind = "Ref-1" }), "cannot be stored"},
		{"a bucket twice", override(func(a *reference) { a.buckets = append(a.buckets, a.buckets...) }), "declared twice"},
		{"a bucket nothing passes", override(func(a *reference) { a.buckets[0].Limit = 0 }), "positive limit"},
		{"a panic", override(func(a *reference) {
			a.classify = func(status int, h http.Header, body []byte) adapter.Failure { return good(status, h, body[:1]) }
		}), "panics"},
		{"no cause", override(func(a *reference) {
			a.classify = func(int, http.Header, []byte) adapter.Failure { return adapter.Failure{} }
		}), "no cause"},
		{"a transport failure from an adapter", override(func(a *reference) {
			a.classify = func(int, http.Header, []byte) adapter.Failure { return adapter.Failure{Cause: adapter.Transport{}} }
		}), "only the shared call code"},
		{"an unrecognised response taken as free", override(func(a *reference) {
			a.classify = func(status int, _ http.Header, body []byte) adapter.Failure {
				f := good(status, nil, body)
				if _, ok := f.Cause.(adapter.Unknown); ok {
					f.Executed = adapter.NotExecuted
				}
				return f
			}
		}), "does not recognise for not executed"},
		{"a 5xx taken as free", override(func(a *reference) {
			a.classify = func(status int, _ http.Header, body []byte) adapter.Failure {
				f := good(status, nil, body)
				if status >= 500 {
					f.Executed = adapter.NotExecuted
				}
				return f
			}
		}), "5xx as not executed"},
		{"an error page taken for the supplier's answer", override(func(a *reference) {
			a.classify = func(status int, _ http.Header, _ []byte) adapter.Failure {
				return good(status, nil, nil)
			}
		}), "HTML page"},
		{"an unknown that lost its status", override(func(a *reference) {
			a.classify = func(status int, _ http.Header, body []byte) adapter.Failure {
				f := good(status, nil, body)
				if u, ok := f.Cause.(adapter.Unknown); ok && !u.SuspectedEgressBlock {
					f.Cause = adapter.Unknown{}
				}
				return f
			}
		}), "carry the response's status"},
		{"a negative wait", override(func(a *reference) {
			a.classify = func(int, http.Header, []byte) adapter.Failure {
				return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.RateLimited{RetryAfter: -time.Second}}
			}
		}), "negative Retry-After"},
		{"a charge ref out of nothing", override(func(a *reference) {
			a.ref = func(http.Header) (adapter.ChargeRef, bool) { return "r", true }
		}), "ChargeRef finds"},
		{"a format twice", override(func(a *reference) {
			a.formats = func(adapter.GroupFacts) []protocol.Format { return []protocol.Format{protocol.Chat, protocol.Chat} }
		}), "twice"},
		{"formats that panic on bad facts", override(func(a *reference) {
			a.formats = func(g adapter.GroupFacts) []protocol.Format { _ = g[0]; return nil }
		}), "panics on unexpected facts"},
	}
	for _, c := range cases {
		problems := adaptertest.Problems(c.a)
		if !strings.Contains(strings.Join(problems, "\n"), c.want) {
			t.Errorf("%s: problems %q do not say %q", c.name, problems, c.want)
		}
	}
}

// scanning is a Manager whose Charges is the function given.
type scanning struct {
	adapter.Manager
	charges func(adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error)
}

func (s scanning) Charges(_ context.Context, state adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	return s.charges(state)
}

// TestScanProblemFindsWhatBreaksTheProtocol: a manager that follows the protocol passes; one that refuses a position it
// returned, one that returns none, and one that never ends a pass each fail.
func TestScanProblemFindsWhatBreaksTheProtocol(t *testing.T) {
	t.Parallel()
	done := adapter.ChargePage{Done: true}
	for name, c := range map[string]struct {
		charges func(adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error)
		fails   bool
	}{
		"follows it": {func(adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
			return done, []byte(`{"v":1}`), nil
		}, false},
		"refuses a position": {func(s adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
			if len(s) > 0 {
				return adapter.ChargePage{}, s, errors.New("unknown position")
			}
			return done, []byte(`{"v":1}`), nil
		}, true},
		"returns no position": {func(adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) { return done, nil, nil }, true},
		"never ends a pass": {func(adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
			return adapter.ChargePage{}, []byte(`{"v":1}`), nil
		}, true},
	} {
		if problem := adaptertest.ScanProblem(scanning{charges: c.charges}); (problem != "") != c.fails {
			t.Errorf("%s: ScanProblem = %q, fails expected: %v", name, problem, c.fails)
		}
	}
}
