package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// failureFrame is OpenAI's failed response as the tests' upstreams send it: what it says of the failure, and the usage
// of the attempt when it says it.
func failureFrame(code, message, param, usage string) string {
	member := ""
	if param != "" {
		member = `,"param":"` + param + `"`
	}
	return `data: {"type":"response.failed","sequence_number":5,"response":{"id":"resp_1","object":"response",` +
		`"status":"failed","model":"up-1","output":[],"error":{"code":"` + code + `","message":"` + message + `"` + member +
		`},"usage":` + usage + `}}` + "\n\n"
}

// errorFrame is the bare error event some upstreams send ahead of the failed response, or alone.
const errorFrame = `data: {"type":"error","sequence_number":5,"code":"rate_limit_exceeded","param":null,` +
	`"message":"upstream-vendor: slow down"}` + "\n\n"

// usage900 is what the tests' failure frames say the attempt used: far more than the few bytes relayed would estimate,
// which is what tells the reported usage from an estimate of it.
const usage900 = `{"input_tokens":900,"output_tokens":700}`

// TestARefusalInTheStreamIsAnsweredWithTheUpstreamsWords: a failure frame that says the request itself is wrong is the
// client's, not the endpoint's, so no other entry is tried, and the answer carries the pointer's members and words as
// the same supplier's 4xx answer does, its name taken out (docs/domains/gateway.md「错误」). A call that does not stream
// is answered the same way: nothing of the reply was delivered, so nothing is sent twice.
func TestARefusalInTheStreamIsAnsweredWithTheUpstreamsWords(t *testing.T) {
	no := false
	for _, c := range []struct {
		name   string
		stream *bool
	}{
		{"streamed", nil},
		{"aggregated", &no},
	} {
		run(t, func(t *testing.T, ctx context.Context) {
			body := refusalAnswer(t, ctx, c.stream)
			for _, want := range []string{
				`"code":"context_length_exceeded"`, `"param":"input"`,
				`"type":"invalid_request_error"`, `"error_type":"invalid_request"`, `"[hidden]: the prompt is too long"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("%s: the answer has no %s:\n%s", c.name, want, body)
				}
			}
			if strings.Contains(body, "upstream-vendor") {
				t.Errorf("%s: the answer names the supplier:\n%s", c.name, body)
			}
		})
	}
}

// refusalAnswer is the answer to the call an upstream refused in its stream, once the second entry is there to be tried
// and left alone, and once the attempt is known to have failed before the commit point with the supplier's name taken
// out of its words. It returns the body.
func refusalAnswer(t *testing.T, ctx context.Context, stream *bool) string {
	t.Helper()
	h := newHarness(t, ctx, options{client: protocol.Responses, entries: 2, stream: stream})
	refusal := failureFrame("context_length_exceeded", "upstream-vendor: the prompt is too long", "input", "null")
	h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(0), beat{data: refusal})}
	h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
	h.run()
	body := h.w.body()
	if h.w.status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400:\n%s", h.w.status, body)
	}
	if h.up.endpoint(2).sends != 0 {
		t.Error("another entry was tried for a request the upstream refused")
	}
	if got := h.up.endpoint(1).sanitized; got != 1 {
		t.Errorf("%d refusals had the supplier's name taken out, want 1", got)
	}
	if a := h.ended().outcome.Attempts[0]; a.Result != billing.FailedBeforeCommit || a.Finish != billing.FinishError {
		t.Errorf("attempt %+v", a)
	}
	return body
}

// TestAFailureFrameAfterTheCommitPointIsChargedAsItSays: the usage of the frame that ended the attempt is the upstream's
// last word on it, so the items it states are charged as reported rather than estimated from the few bytes relayed, and
// the stream ends in place, settled once, with the format's failure event: the upstream's code and words for a refusal,
// ours for a failure of the endpoint's (docs/domains/billing.md「用量来源与暂估」).
func TestAFailureFrameAfterTheCommitPointIsChargedAsItSays(t *testing.T) {
	for _, c := range []struct {
		name string
		// before is what the upstream says ahead of the failure frame, if anything.
		before, frame, code, words string
	}{{
		name:  "the failure frame alone",
		frame: failureFrame("server_error", "upstream-vendor broke", "", usage900),
		code:  "upstream_error",
		words: "", // the endpoint's failure is not the client's to read: our words answer it
	}, {
		name:   "the error event, then the failure frame with the usage",
		before: errorFrame,
		frame:  failureFrame("server_error", "upstream-vendor broke", "", usage900),
		code:   "upstream_error",
	}, {
		name:  "a refusal",
		frame: failureFrame("context_length_exceeded", "upstream-vendor: too long", "input", usage900),
		code:  "context_length_exceeded",
		words: "[hidden]: too long",
	}} {
		run(t, func(t *testing.T, ctx context.Context) {
			body, a, settled := chargedFailure(t, ctx, c.before, c.frame)
			checkFailureCharge(t, a, 900, 700)
			if !settled {
				t.Errorf("%s: the attempt was not settled once", c.name)
			}
			checkFailureEnding(t, c.name, body, c.code, c.words)
		})
	}
}

// chargedFailure is the call the failure frame ended after the commit point: the body of the answer, the attempt, and
// whether it was settled exactly once. The second entry is there to be tried and left alone.
func chargedFailure(t *testing.T, ctx context.Context, before, frame string) (body string, a billing.AttemptResult, settled bool) {
	t.Helper()
	h := newHarness(t, ctx, options{client: protocol.Responses, entries: 2})
	beats := []beat{h.feed(0, 2, 3, 4)}
	if before != "" {
		beats = append(beats, beat{data: before})
	}
	h.up.eps[uuidOf(ep(1))].script = []scripted{serve(append(beats, beat{data: frame})...)}
	h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
	h.run()
	body, a = h.w.body(), h.ended().outcome.Attempts[0]
	if h.w.status != http.StatusOK {
		t.Fatalf("status %d:\n%s", h.w.status, body)
	}
	if h.up.endpoint(2).sends != 0 {
		t.Error("another entry was tried after the commit point")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return body, a, len(h.settlements) == 1
}

// checkFailureCharge is the attempt the failure frame ended: committed, failed, and charged for the input and output it
// stated.
func checkFailureCharge(t *testing.T, a billing.AttemptResult, input, output int64) {
	t.Helper()
	u := a.Segments[0]
	switch {
	case a.Result != billing.Committed || a.Finish != billing.FinishError:
		t.Errorf("attempt %+v", a)
	case u.Usage.Count(catalog.ItemInput) != input || u.Usage.OutputTotal() != output,
		!u.Reported.Has(catalog.ItemInput), !u.Reported.Has(catalog.ItemOutput):
		t.Errorf("usage %+v, reported %d", u.Usage, u.Reported)
	}
}

// checkFailureEnding is the last event the client reads: the failed response, with the code and the words the attempt is
// answered with, the supplier's name taken out.
func checkFailureEnding(t *testing.T, name, body, code, words string) {
	t.Helper()
	last := body[strings.LastIndex(body, "event: "):]
	if !strings.HasPrefix(last, "event: response.failed\n") {
		t.Errorf("%s: the stream ends with:\n%s", name, last)
	}
	if !strings.Contains(last, `"code":"`+code+`"`) {
		t.Errorf("%s: the ending says\n%s", name, last)
	}
	if words != "" && !strings.Contains(last, `"message":"`+words+`"`) {
		t.Errorf("%s: the ending says\n%s", name, last)
	}
}

// TestAnErrorEventThatEndsTheStreamIsItsFailure: an upstream that sends the error event alone and ends has failed with
// what it said, so the attempt ends as an error, not as out of time, and the usage is what the frames before it
// reported, the output being estimated from the bytes relayed: a count never reported is a lower bound, which billing
// does not take for the count (docs/domains/gateway.md「错误」).
func TestAnErrorEventThatEndsTheStreamIsItsFailure(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{client: protocol.Responses, entries: 2})
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(0, 2, 3, 4), beat{data: errorFrame})}
		h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
		h.run()
		body, a := h.w.body(), h.ended().outcome.Attempts[0]
		u := a.Segments[0]
		if h.w.status != http.StatusOK {
			t.Fatalf("status %d:\n%s", h.w.status, body)
		}
		if h.up.endpoint(2).sends != 0 {
			t.Error("another entry was tried after the commit point")
		}
		if a.Result != billing.Committed || a.Finish != billing.FinishError {
			t.Errorf("attempt %+v, want the upstream's failure and not our idle timeout", a)
		}
		if u.Reported.Has(catalog.ItemOutput) || u.Reported.Has(catalog.ItemInput) {
			t.Errorf("usage reported %d, want nothing reported: %+v", u.Reported, u.Usage)
		}
		if u.Usage.OutputTotal() == 0 {
			t.Errorf("the output of the content relayed was not estimated: %+v", u.Usage)
		}
		if !strings.Contains(body, `"delta":"Hi"`) {
			t.Errorf("the content relayed before the failure is missing:\n%s", body)
		}
		checkFailureEnding(t, "the error event alone", body, "upstream_error", "")
	})
}
