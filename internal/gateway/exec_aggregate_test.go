package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// A client that wants no stream gets its reply from the upstream's stream, aggregated: the whole body in one write,
// after the settlement whose cost it reports (docs/domains/gateway.md「转发规则」「用量与费用」).

// TestACallThatDoesNotStreamIsAnsweredWithOneReply: every path that has an aggregator answers a client that asked for no
// stream with the whole reply, in one write, after the settlement whose cost is in it (I3), with the ID of the call and
// the model the client asked for.
func TestACallThatDoesNotStreamIsAnsweredWithOneReply(t *testing.T) {
	no := false
	messages, responses, chat := protocol.Messages, protocol.Responses, protocol.Chat
	cases := []struct {
		name  string
		o     options
		shape []string // what the reply of the format must carry
	}{
		{"messages", options{client: messages, stream: &no}, []string{`"type":"message"`, `"stop_reason":"end_turn"`, `"text":"Hello!"`}},
		{"responses", options{client: responses, stream: &no},
			[]string{`"object":"response"`, `"status":"completed"`, `"text":"Hi there! How can I assist you today?"`}},
		{"chat from messages", options{client: chat, upstream: &messages, stream: &no},
			[]string{`"object":"chat.completion"`, `"content":"Hello!"`}},
		{"chat from responses", options{client: chat, upstream: &responses, stream: &no},
			[]string{`"object":"chat.completion"`, `"content":"Hi there! How can I assist you today?"`}},
	}
	for _, c := range cases {
		run(t, func(t *testing.T, ctx context.Context) {
			o := c.o
			o.final = billing.Final{Charged: platform.AmountExact(3703701)}
			h := newHarness(t, ctx, o)
			h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
			h.run()
			checkReply(t, h, c.name, c.shape)
		})
	}
}

// checkReply holds an aggregated reply to what a client that wants no stream is promised: one JSON body, one write, the
// call's number and its model in it, the cost of the settlement, and one attempt whose content reached the client
// (billing.Aggregated), settled before the write and journalled before it.
func checkReply(t *testing.T, h *harness, name string, shape []string) {
	t.Helper()
	body := h.w.body()
	if h.w.status != http.StatusOK || h.w.header.Get("Content-Type") != "application/json" {
		t.Fatalf("%s: status %d, headers %v:\n%s", name, h.w.status, h.w.header, body)
	}
	if n := h.w.wrote(); n != 1 {
		t.Errorf("%s: %d writes for a reply that is one", name, n)
	}
	want := append(shape, h.call.ID.String(), `"model":"`+h.call.Request.Model+`"`, `"cost":0.003703701,"cost_currency":"USD"`)
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s: the reply lacks %s:\n%s", name, w, body)
		}
	}
	s := h.ended()
	if s.body != "" {
		t.Errorf("%s: something was written before the settlement:\n%s", name, s.body)
	}
	if a := s.outcome.Attempts; len(a) != 1 || a[0].Result != billing.Aggregated || a[0].Finish != billing.FinishStop || s.outcome.Cancelled {
		t.Errorf("%s: ending %+v", name, s.outcome)
	}
	for i, e := range h.up.journal {
		if e.Committed && h.up.writes[i] != 0 {
			t.Errorf("%s: the commit was journalled after %d writes", name, h.up.writes[i])
		}
	}
}

// TestAFailedAggregateIsTriedElsewhereAndItsContentIsNotMixed: nothing reached the client, so an attempt that generated
// content and then broke is given up on for the next entry, whose reply alone answers the call
// (docs/domains/gateway.md「转发规则」).
func TestAFailedAggregateIsTriedElsewhereAndItsContentIsNotMixed(t *testing.T) {
	no := false
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 2, stream: &no})
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart, evBlock, evHello), beat{hangup: true})}
		h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
		h.run()
		body := h.w.body()
		ends := h.ended().outcome.Attempts
		if h.w.status != http.StatusOK || h.w.wrote() != 1 || len(ends) != 2 || ends[0].Result != billing.FailedBeforeCommit ||
			ends[1].Result != billing.Aggregated {
			t.Fatalf("status %d, %d writes, attempts %+v:\n%s", h.w.status, h.w.wrote(), ends, body)
		}
		if got := strings.Count(body, "Hello"); got != 1 {
			t.Errorf("the reply holds the content of %d attempts:\n%s", got, body)
		}
	})
}

// TestTheLastAttemptOfAnAggregateAnswersWithWhatItMade: when no entry is left, what the last attempt generated answers
// the call with the error in it for Chat and Responses, and Messages, which has no stop reason for an error, answers the
// error alone, 502 (docs/domains/gateway.md「错误」).
func TestTheLastAttemptOfAnAggregateAnswersWithWhatItMade(t *testing.T) {
	no := false
	messages, responses := protocol.Messages, protocol.Responses
	cases := []struct {
		name    string
		o       options
		beats   func(h *harness) []beat
		status  int
		content string // what the reply holds, if anything
		shape   []string
	}{
		{
			"chat", options{client: protocol.Chat, upstream: &messages},
			func(h *harness) []beat { return []beat{h.feed(evStart, evBlock, evHello), beat{hangup: true}} },
			http.StatusOK, "Hello", []string{`"finish_reason":"error"`, `"metadata":{"error_type":"upstream_error"}`},
		},
		{
			"responses", options{client: responses},
			func(h *harness) []beat { return []beat{h.feed(0, 1, 2, 3, 4), beat{hangup: true}} },
			http.StatusOK, "Hi", []string{`"status":"failed"`, `"error":{"code":"upstream_error"`},
		},
		{
			"messages", options{},
			func(h *harness) []beat { return []beat{h.feed(evStart, evBlock, evHello), beat{hangup: true}} },
			http.StatusBadGateway, "", []string{`"error_type":"upstream_error"`},
		},
	}
	for _, c := range cases {
		run(t, func(t *testing.T, ctx context.Context) {
			o := c.o
			o.stream = &no
			h := newHarness(t, ctx, o)
			h.up.eps[uuidOf(ep(1))].script = []scripted{serve(c.beats(h)...)}
			h.run()
			body := h.w.body()
			if h.w.status != c.status {
				t.Fatalf("%s: status %d:\n%s", c.name, h.w.status, body)
			}
			for _, w := range c.shape {
				if !strings.Contains(body, w) {
					t.Errorf("%s: the reply lacks %s:\n%s", c.name, w, body)
				}
			}
			if c.content != "" && !strings.Contains(body, c.content) {
				t.Errorf("%s: the reply does not hold what it made:\n%s", c.name, body)
			}
			a := h.ended().outcome.Attempts
			want := billing.Aggregated
			if c.status != http.StatusOK { // what it made is not in the reply: Messages answers the error alone
				want = billing.FailedBeforeCommit
			}
			if len(a) != 1 || a[0].Result != want || a[0].Finish != billing.FinishError {
				t.Errorf("%s: attempts %+v", c.name, a)
			}
		})
	}
}

// TestAnAggregatePastTheCapIsRefusedWithoutAnotherEntry: a reply may hold 16 MiB (protocol's limit), past which the call
// ends with the node's own capacity error, which no other endpoint can cure (README.md「Relay 的所有权」).
func TestAnAggregatePastTheCapIsRefusedWithoutAnotherEntry(t *testing.T) {
	no := false
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 2, stream: &no})
		beats := []beat{h.feed(evStart, evBlock)}
		for range 33 { // 16.5 MiB of text, in chunks that fit one event
			beats = append(beats, beat{data: text(512 << 10)})
		}
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(append(beats, h.feed(evBlockStop, evMsgDelta, evMsgStop))...)}
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

// TestAClientThatLeavesWhileItsReplyIsAggregated: the disconnect rules are a stream's (docs/domains/gateway.md
// 「用量与费用」): an endpoint that passes cancellation on is stopped at once with what it had made charged, one that
// does not is read to its end, and nothing is written to the client that left.
func TestAClientThatLeavesWhileItsReplyIsAggregated(t *testing.T) {
	no := false
	cases := []struct {
		name     string
		cancels  bool
		took     time.Duration
		output   int64
		reported bool
	}{
		{"cancels", true, 2 * time.Second, 5, false}, // "Hello", counted by its bytes, estimated
		{"does not", false, 10 * time.Second, 15, true},
	}
	for _, c := range cases {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, options{stream: &no, policy: suppliers.CallPolicy{Streams: true, Cancels: c.cancels}})
			h.up.eps[uuidOf(ep(1))].script = []scripted{serve(
				h.feed(evStart, evBlock, evHello), h.after(5*time.Second, evBang), h.after(5*time.Second, evBlockStop, evMsgDelta, evMsgStop),
			)}
			done := h.start()
			time.Sleep(2 * time.Second)
			h.client()
			<-done
			h.check()
			if got := h.elapsed(); got != c.took {
				t.Errorf("%s: the call took %v", c.name, got)
			}
			if h.w.wrote() != 0 || h.w.status != 0 {
				t.Errorf("%s: a client that had gone was written to: %d writes, status %d", c.name, h.w.wrote(), h.w.status)
			}
			s := h.ended()
			a := s.outcome.Attempts[0]
			u := a.Segments[0]
			if !s.outcome.Cancelled || a.Result != billing.FailedBeforeCommit || a.Finish != billing.FinishCancelled ||
				u.Usage.OutputTotal() != c.output || u.Reported.Has(catalog.ItemOutput) != c.reported {
				t.Errorf("%s: ending %+v, usage %+v", c.name, s.outcome, u)
			}
		})
	}
}

// TestAnAggregateMetersItsStreamAsItGoes: the upstream is still read as a stream, so the reservation is extended as the
// reply grows, on the same path as a stream's (docs/domains/gateway.md「转发规则」).
func TestAnAggregateMetersItsStreamAsItGoes(t *testing.T) {
	no := false
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{stream: &no}))
		beats := []beat{h.feed(evStart), h.feed(evBlock)}
		for range 9 {
			beats = append(beats, beat{wait: time.Second, data: text(100)})
		}
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(append(beats, h.feed(evBlockStop, evMsgDelta, evMsgStop))...)}
		h.run()
		if h.elapsed() != 9*time.Second || h.w.status != http.StatusOK {
			t.Fatalf("took %v, status %d:\n%s", h.elapsed(), h.w.status, h.w.body())
		}
		checkExtensions(t, h)
		if a := h.ended().outcome.Attempts[0]; a.Result != billing.Aggregated || a.Finish != billing.FinishStop {
			t.Errorf("attempt %+v", a)
		}
	})
}

// TestAnAggregateWhoseSettlementFails: a reply that completed is a 200 without a cost and with the pending flag, as a
// stream's is (docs/domains/gateway.md「用量与费用」).
func TestAnAggregateWhoseSettlementFails(t *testing.T) {
	no := false
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{stream: &no, settleErr: io.ErrClosedPipe})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		body := h.w.body()
		if h.w.status != http.StatusOK || !strings.Contains(body, `"billing_pending":true`) || strings.Contains(body, `"cost"`) {
			t.Errorf("status %d:\n%s", h.w.status, body)
		}
		if a := h.ended().outcome.Attempts; len(a) != 1 || a[0].Result != billing.Aggregated {
			t.Errorf("attempts %+v", a)
		}
	})
}

// TestATruncatedAggregateAnswersWithTheBillingError: the reservation could not be extended, so the call is answered with
// the billing error, without what was made and without another entry, whose attempt would draw on the same account
// (docs/domains/gateway.md「错误」).
func TestATruncatedAggregateAnswersWithTheBillingError(t *testing.T) {
	no := false
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{entries: 2, stream: &no, extend: func(context.Context, billing.ExtendRequest) error {
			return &billing.Insufficient{Source: billing.SourceBalance}
		}}))
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart), h.feed(evBlock), beat{data: text(250)})}
		h.run()
		body := h.w.body()
		if h.w.status != http.StatusPaymentRequired || !strings.Contains(body, `"error_type":"credits_insufficient"`) ||
			!strings.Contains(body, `"limit_source":"balance"`) || strings.Contains(body, strings.Repeat("x", 250)) {
			t.Fatalf("status %d:\n%s", h.w.status, body)
		}
		if h.up.endpoint(2).sends != 0 {
			t.Error("another entry was tried for a call the account could not pay for")
		}
		if a := h.ended().outcome.Attempts; len(a) != 1 || a[0].Result != billing.FailedBeforeCommit || a[0].Finish != billing.FinishTruncated {
			t.Errorf("attempts %+v", a)
		}
	})
}
