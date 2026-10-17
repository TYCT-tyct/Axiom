package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// The compact endpoint is the one call an upstream answers whole: the gateway passes the request and the reply through
// as they are, reserving in full and settling what the reply reports (docs/domains/gateway.md「转发规则」).

// compacted is a reply of the Responses compact endpoint: a compacted conversation and what it used.
const compacted = `{"object":"response.compaction","output":[{"type":"compaction","id":"cmp_1","encrypted_content":"enc"}],` +
	`"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":10,` +
	`"output_tokens_details":{"reasoning_tokens":4}}}`

// compact makes the call one to the Responses compact endpoint: the client's request as it wrote it, for an endpoint of
// that format.
func compact(t *testing.T, c *Call) {
	t.Helper()
	c.Compact = true
	c.Body = []byte(`{"model":"m","input":"Hello!"}`)
	req, err := protocol.Inspect(protocol.Responses, c.Body)
	if err != nil {
		t.Fatal(err)
	}
	req.ServiceTiers = 1 << catalog.ServiceTierStandard
	c.Request = req
	for i := range c.Plan.Entries {
		c.Plan.Entries[i].Target.Format = protocol.Responses
	}
}

// answer is an upstream that answers with a whole body of a status, as the compact endpoint does.
func wholeAnswer(status int, body string) scripted {
	return func(context.Context) (*http.Response, *suppliers.Failure) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

// TestTheCompactEndpointIsPassedThrough: the upstream gets the request as the client wrote it, at the compact path, and
// the client gets its answer as it came, in one write, after the settlement, which is charged for the usage the reply
// reports.
func TestTheCompactEndpointIsPassedThrough(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{mutate: func(c *Call) { compact(t, c) }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{wholeAnswer(http.StatusOK, compacted)}
		h.run()
		if h.w.status != http.StatusOK || h.w.header.Get("Content-Type") != "application/json" || h.w.body() != compacted || h.w.wrote() != 1 {
			t.Fatalf("status %d, %d writes:\n%s", h.w.status, h.w.wrote(), h.w.body())
		}
		if call := h.up.endpoint(1).calls[0]; call.Path != compactPath || string(call.Body) != `{"model":"m","input":"Hello!"}` ||
			call.Format != protocol.Responses {
			t.Errorf("the request: path %q, format %v, body %s", call.Path, call.Format, call.Body)
		}
		s := h.ended()
		if s.body != "" {
			t.Errorf("the reply was written before the settlement:\n%s", s.body)
		}
		a := s.outcome.Attempts
		if len(a) != 1 || a[0].Result != billing.Aggregated || a[0].Finish != billing.FinishStop || s.outcome.Cancelled {
			t.Fatalf("ending %+v", s.outcome)
		}
		u := a[0].Segments[0]
		for _, it := range []struct {
			item catalog.Item
			want int64
		}{
			{catalog.ItemInput, 60}, {catalog.ItemCacheRead, 40}, {catalog.ItemOutput, 6}, {catalog.ItemReasoning, 4},
		} {
			if got := u.Usage.Count(it.item); got != it.want || !u.Reported.Has(it.item) {
				t.Errorf("%s is %d (reported %t), want %d", it.item, got, u.Reported.Has(it.item), it.want)
			}
		}
		if n := len(h.up.journal); n == 0 || !h.up.journal[n-1].Committed {
			t.Errorf("the journal does not say the reply reached the client: %+v", h.up.journal)
		}
	})
}

// TestTheCompactEndpointWaitsItsOwnTimeForTheAnswer: the compact endpoint compacts a whole conversation, so its headers
// are waited for ten minutes rather than one (docs/domains/gateway.md「路径与格式」).
func TestTheCompactEndpointWaitsItsOwnTimeForTheAnswer(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{mutate: func(c *Call) { compact(t, c) }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{late(11*time.Minute, wholeAnswer(http.StatusOK, compacted))}
		h.run()
		if got := h.elapsed(); got != h.cfg.CompactHeaderWait {
			t.Errorf("the call took %v, the compact wait is %v", got, h.cfg.CompactHeaderWait)
		}
		if h.w.status != http.StatusBadGateway || h.up.endpoint(1).acquired != 1 || h.w.wrote() != 1 {
			t.Errorf("status %d, %d acquired, %d writes", h.w.status, h.up.endpoint(1).acquired, h.w.wrote())
		}
	})
}

// TestACompactReplyThatBreaksIsTriedElsewhere: nothing reached the client, so a reply the upstream broke off is given up
// on for the next entry.
func TestACompactReplyThatBreaksIsTriedElsewhere(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 2, mutate: func(c *Call) { compact(t, c) }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{func(context.Context) (*http.Response, *suppliers.Failure) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
				Body: io.NopCloser(io.MultiReader(strings.NewReader(`{"object":`), iotestErr{io.ErrUnexpectedEOF}))}, nil
		}}
		h.up.eps[uuidOf(ep(2))].script = []scripted{wholeAnswer(http.StatusOK, compacted)}
		h.run()
		if h.w.status != http.StatusOK || h.w.body() != compacted {
			t.Fatalf("status %d:\n%s", h.w.status, h.w.body())
		}
		a := h.ended().outcome.Attempts
		if len(a) != 2 || a[0].Result != billing.FailedBeforeCommit || a[1].Result != billing.Aggregated {
			t.Errorf("attempts %+v", a)
		}
	})
}

// zero is a reader of an endless stream of zeros.
type zero struct{}

func (zero) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// TestACompactReplyHoldsAtMostTheCap: the whole reply is held within the cap an aggregated reply has, a reply of exactly
// the cap included; past it the read stops with the node's own error, and what it held is the caller's to give back.
func TestACompactReplyHoldsAtMostTheCap(t *testing.T) {
	idle := time.NewTimer(time.Hour)
	defer idle.Stop()
	memory := protocol.NewMemory(1 << 30)
	for _, c := range []struct {
		name  string
		size  int64
		reply bool
	}{
		{"exactly the cap", compactLimit, true},
		{"a byte past it", compactLimit + 1, false},
	} {
		body, held, err := readWhole(memory, io.LimitReader(zero{}, c.size), idle, time.Hour)
		if c.reply && (err != nil || int64(len(body)) != c.size) {
			t.Errorf("%s: %d bytes, error %v", c.name, len(body), err)
		}
		if !c.reply && !errors.Is(err, protocol.ErrMemory) {
			t.Errorf("%s: error %v, want the node's", c.name, err)
		}
		memory.Refund(held)
		if got := left(memory); got != 1<<30 {
			t.Fatalf("%s: %d bytes are left of the budget", c.name, got)
		}
	}
}
