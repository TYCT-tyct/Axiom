package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// The compact endpoint is the one call an upstream answers whole: the gateway passes the reply through as it came, sends
// the request rewritten by the members that must not go upstream as they are, reserves in full and settles what the
// reply reports (docs/domains/gateway.md「转发规则」).

// compacted is a reply of the Responses compact endpoint: a compacted conversation and what it used.
const compacted = `{"object":"response.compaction","output":[{"type":"compaction","id":"cmp_1","encrypted_content":"enc"}],` +
	`"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":10,` +
	`"output_tokens_details":{"reasoning_tokens":4}}}`

// compactRequest is the client's compaction request: its own model, a member only a generation takes, the identifiers
// and the routing parameters that must not reach an upstream as they are.
const compactRequest = `{"model":"client-model","input":"Hello!","store":true,"max_output_tokens":7,` +
	`"user":"client-user","prompt_cache_key":"client-key","provider":{"order":["acme"]},"session_id":"abc"}`

// compact makes the call one to the Responses compact endpoint: the client's request as it wrote it, for an endpoint of
// that format.
func compact(t *testing.T, c *Call) {
	t.Helper()
	c.Compact = true
	c.Body = []byte(compactRequest)
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

// bigCompacted is a compact reply holding n bytes of encrypted content, with the usage the sample's has.
func bigCompacted(n int) string {
	return `{"object":"response.compaction","output":[{"type":"compaction","id":"cmp_1","encrypted_content":"` +
		strings.Repeat("x", n) + `"}],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},` +
		`"output_tokens":10,"output_tokens_details":{"reasoning_tokens":4}}}`
}

// wholeAnswer is an upstream that answers with a whole body, as the compact endpoint does.
func wholeAnswer(body string) scripted {
	return func(context.Context) (*http.Response, *suppliers.Failure) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

// TestTheCompactEndpointIsPassedThrough: the upstream gets the request rewritten — the endpoint's model, derived
// identifiers, no routing parameters — at the compact path, and the client gets its answer as it came, in one write,
// after the settlement, which is charged for the usage the reply reports.
func TestTheCompactEndpointIsPassedThrough(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{mutate: func(c *Call) { compact(t, c) }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{wholeAnswer(compacted)}
		h.run()
		checkCompactReply(t, h)
		checkCompactRequest(t, h)
		checkCompactSettlement(t, h)
	})
}

// checkCompactReply: the reply reached the client as it came, in one write, as JSON.
func checkCompactReply(t *testing.T, h *harness) {
	t.Helper()
	if h.w.status != http.StatusOK || h.w.header.Get("Content-Type") != "application/json" || h.w.body() != compacted ||
		h.w.wrote() != 1 {
		t.Fatalf("status %d, %d writes:\n%s", h.w.status, h.w.wrote(), h.w.body())
	}
}

// checkCompactRequest: the upstream got the client's body with the model the endpoint knows, the identifiers derived and
// the routing parameters gone, and every other member as the client wrote it.
func checkCompactRequest(t *testing.T, h *harness) {
	t.Helper()
	derive := h.exec.identity(uuid.UUID(h.call.caller.AccountID()))
	want := `{"model":"up-1","input":"Hello!","store":true,"max_output_tokens":7,` +
		`"user":"` + derive("client-user") + `","prompt_cache_key":"` + derive("client-key") + `"}`
	call := h.up.endpoint(1).calls[0]
	if !call.Compact || call.Format != protocol.Responses || string(call.Body) != want {
		t.Errorf("the request: compact %t, format %v, body\n%s\nwant\n%s", call.Compact, call.Format, call.Body, want)
	}
}

// checkCompactSettlement: one attempt whose reply reached the client, settled before the write, charged for the usage
// the reply reported.
func checkCompactSettlement(t *testing.T, h *harness) {
	t.Helper()
	s := h.ended()
	if s.body != "" {
		t.Errorf("the reply was written before the settlement:\n%s", s.body)
	}
	a := s.outcome.Attempts
	if len(a) != 1 || a[0].Result != billing.Aggregated || a[0].Finish != billing.FinishStop {
		t.Fatalf("ending %+v", s.outcome)
	}
	u := a[0].Segments[0]
	for _, it := range []struct {
		item catalog.Item
		want int64
	}{
		{item: catalog.ItemInput, want: 60},
		{item: catalog.ItemCacheRead, want: 40},
		{item: catalog.ItemOutput, want: 6},
		{item: catalog.ItemReasoning, want: 4},
	} {
		if got := u.Usage.Count(it.item); got != it.want || !u.Reported.Has(it.item) {
			t.Errorf("%s is %d (reported %t), want %d", it.item, got, u.Reported.Has(it.item), it.want)
		}
	}
}

// TestACompactReplyThatNamesNoUsageIsEstimated: what a reply does not report is estimated, not waived and not charged
// as nothing: the input by the bound of the first sampling, marked as not reported, and the output at nothing, since a
// whole reply says nothing of what it generated (docs/domains/billing.md「用量来源与暂估」).
func TestACompactReplyThatNamesNoUsageIsEstimated(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		const noUsage = `{"object":"response.compaction","output":[]}`
		h := newHarness(t, ctx, options{mutate: func(c *Call) { compact(t, c) }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{wholeAnswer(noUsage)}
		h.run()
		if h.w.status != http.StatusOK || h.w.body() != noUsage {
			t.Fatalf("status %d:\n%s", h.w.status, h.w.body())
		}
		input := estimatedInput(t, h, 0)
		u := h.ended().outcome.Attempts[0].Segments[0]
		if u.Usage.Count(catalog.ItemInput) != input || u.Reported.Has(catalog.ItemInput) {
			t.Errorf("the input is %d (reported %t), want %d estimated", u.Usage.Count(catalog.ItemInput),
				u.Reported.Has(catalog.ItemInput), input)
		}
		if u.Usage.Count(catalog.ItemOutput) != 0 {
			t.Errorf("the output is %d, want nothing a whole reply cannot tell", u.Usage.Count(catalog.ItemOutput))
		}
	})
}

// TestTheCompactEndpointWaitsItsOwnTimeForTheAnswer: the compact endpoint compacts a whole conversation, so its headers
// are waited for ten minutes rather than one (docs/domains/gateway.md「路径与格式」).
func TestTheCompactEndpointWaitsItsOwnTimeForTheAnswer(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{mutate: func(c *Call) { compact(t, c) }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{late(11*time.Minute, wholeAnswer(compacted))}
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
			body := io.MultiReader(strings.NewReader(`{"object":`), iotestErr{io.ErrUnexpectedEOF})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(body),
			}, nil
		}}
		h.up.eps[uuidOf(ep(2))].script = []scripted{wholeAnswer(compacted)}
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

// TestACompactReplyTooLargeForTheNodeIsNotTriedElsewhere: the reply does not fit the node's budget, which no other
// endpoint can cure, so the call is answered with the node's error and no other entry is tried (README.md「Relay 的
// 所有权」).
func TestACompactReplyTooLargeForTheNodeIsNotTriedElsewhere(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		const budget = 1 << 20
		h := newHarness(t, ctx, options{entries: 2, memory: budget, mutate: func(c *Call) { compact(t, c) }})
		large := `{"object":"response.compaction","output":"` + strings.Repeat("x", 2*budget) + `"}`
		h.up.eps[uuidOf(ep(1))].script = []scripted{wholeAnswer(large)}
		h.run()
		body := h.w.body()
		if h.w.status != http.StatusServiceUnavailable || !strings.Contains(body, `"error_type":"overloaded"`) {
			t.Fatalf("status %d:\n%s", h.w.status, body)
		}
		if h.up.endpoint(2).sends != 0 {
			t.Error("another entry was tried for a reply too large for the node")
		}
		if a := h.ended().outcome.Attempts; len(a) != 1 || a[0].Result != billing.FailedBeforeCommit {
			t.Errorf("attempts %+v", a)
		}
	})
}

// goneAtEOF cancels the client when its reply ends, as one that leaves as the last byte arrives.
type goneAtEOF struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (g *goneAtEOF) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	if errors.Is(err, io.EOF) {
		g.cancel()
	}
	return n, err
}

// TestACompactReplyReadForAClientThatLeft: the reply was read to its end for what it says it used, but nothing of it
// reaches a client that has gone: the attempt is a failure before the commit point, and nothing is written
// (docs/domains/gateway.md「用量与费用」).
func TestACompactReplyReadForAClientThatLeft(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{mutate: func(c *Call) { compact(t, c) }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{func(context.Context) (*http.Response, *suppliers.Failure) {
			body := &goneAtEOF{r: strings.NewReader(compacted), cancel: h.client}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(body)}, nil
		}}
		h.run()
		if h.w.wrote() != 0 || h.w.status != 0 {
			t.Errorf("a client that had gone was written to: %d writes, status %d", h.w.wrote(), h.w.status)
		}
		s := h.ended()
		a := s.outcome.Attempts
		if len(a) != 1 || a[0].Result != billing.FailedBeforeCommit || a[0].Finish != billing.FinishCancelled {
			t.Errorf("ending %+v", s.outcome)
		}
		if a[0].Segments[0].Usage.Count(catalog.ItemInput) != 60 {
			t.Errorf("the reply's usage was not booked: %+v", a[0].Segments[0])
		}
	})
}

// TestASlowClientGetsItsWholeReply: a whole reply goes out in chunks, each within its own write deadline, so a client
// that reads slowly but keeps reading gets all of it, though the reply takes longer than one write deadline
// (docs/domains/gateway.md「转发规则」).
func TestASlowClientGetsItsWholeReply(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		reply := bigCompacted(1 << 20)
		h := newHarness(t, ctx, options{mutate: func(c *Call) { compact(t, c) }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{wholeAnswer(reply)}
		h.w.rate = 96 << 10 // 96 KiB a second: a 64 KiB chunk takes 0.68s, the 1 MiB reply 10.9s
		h.run()
		if h.w.status != http.StatusOK || h.w.body() != reply {
			t.Fatalf("status %d, %d of %d bytes:\n%s", h.w.status, len(h.w.body()), len(reply), h.w.body())
		}
		if want := (len(reply) + respondChunk - 1) / respondChunk; h.w.wrote() != want {
			t.Errorf("%d writes for a reply of %d bytes, want %d chunks", h.w.wrote(), len(reply), want)
		}
		if h.elapsed() <= h.cfg.WriteTimeout {
			t.Errorf("the reply took %v, no longer than one write deadline", h.elapsed())
		}
	})
}

// TestAWholeReplyIsStoppedWhenItTakesTooLong: a reply has a total time of its own — its bytes at respondRate, or one
// write's deadline, whichever is longer — and each chunk's deadline is what is left of it. A client that would take
// longer than that is taken for one that has gone: the writes stop, the settlement stays what it was (the reply was
// read, the report was charged) and the call's slot and memory are given back. The client here reads a 64 KiB chunk in
// 8s, which one write's own deadline would allow; the reply's total of 10s is what stops it after the first chunk
// (docs/domains/gateway.md「转发规则」, README.md「超时与限额」).
func TestAWholeReplyIsStoppedWhenItTakesTooLong(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		reply := bigCompacted(256 << 10)
		h := newHarness(t, ctx, options{mutate: func(c *Call) { compact(t, c) }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{wholeAnswer(reply)}
		h.w.rate = 8 << 10 // 8 KiB a second: a chunk in 8s, the 256 KiB reply in 32s against a total of 16s
		h.run()
		if h.w.status != http.StatusOK || h.w.wrote() != 1 || h.w.body() != reply[:respondChunk] {
			t.Fatalf("status %d, %d writes, %d bytes:\n%s", h.w.status, h.w.wrote(), len(h.w.body()), h.w.body())
		}
		s := h.ended()
		a := s.outcome.Attempts
		if len(a) != 1 || a[0].Result != billing.Aggregated || a[0].Finish == billing.FinishCancelled {
			t.Errorf("ending %+v", s.outcome)
		}
		if got := a[0].Segments[0].Usage.Count(catalog.ItemInput); got != 60 {
			t.Errorf("the reply's usage is %d, want it charged with the report", got)
		}
	})
}

// TestAClientThatStopsReadingAWholeReply: a write that fails stops the reply where it is, and the settlement stands: the
// reply was read and reported, so it is charged as delivered what was written of it (docs/domains/gateway.md
// 「用量与费用」).
func TestAClientThatStopsReadingAWholeReply(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		reply := bigCompacted(256 << 10)
		h := newHarness(t, ctx, options{mutate: func(c *Call) { compact(t, c) }})
		h.up.eps[uuidOf(ep(1))].script = []scripted{wholeAnswer(reply)}
		h.w.failFrom = 2 // the second chunk's write fails, as a client that stopped reading
		h.run()
		if h.w.status != http.StatusOK || h.w.wrote() != 1 || h.w.body() != reply[:respondChunk] {
			t.Fatalf("status %d, %d writes, %d bytes", h.w.status, h.w.wrote(), len(h.w.body()))
		}
		s := h.ended()
		a := s.outcome.Attempts
		if len(a) != 1 || a[0].Result != billing.Aggregated || a[0].Finish == billing.FinishCancelled {
			t.Errorf("ending %+v", s.outcome)
		}
		if got := a[0].Segments[0].Usage.Count(catalog.ItemOutput); got != 6 {
			t.Errorf("the reply's output is %d, want the report charged", got)
		}
	})
}
