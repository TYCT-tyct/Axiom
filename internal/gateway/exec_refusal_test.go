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
			h := newHarness(t, ctx, options{client: protocol.Responses, entries: 2, stream: c.stream})
			refusal := failureFrame("context_length_exceeded", "upstream-vendor: the prompt is too long", "input", "null")
			h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(0), beat{data: refusal})}
			h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
			h.run()
			body := h.w.body()
			switch {
			case h.w.status != http.StatusBadRequest:
				t.Errorf("%s: status %d, want 400:\n%s", c.name, h.w.status, body)
			case h.up.endpoint(2).sends != 0:
				t.Errorf("%s: another entry was tried for a request the upstream refused", c.name)
			case h.up.endpoint(1).sanitized != 1:
				t.Errorf("%s: %d refusals had the supplier's name taken out, want 1", c.name, h.up.endpoint(1).sanitized)
			case !strings.Contains(body, `"code":"context_length_exceeded"`), !strings.Contains(body, `"param":"input"`),
				!strings.Contains(body, `"type":"invalid_request_error"`), !strings.Contains(body, `"error_type":"invalid_request"`),
				!strings.Contains(body, `"[hidden]: the prompt is too long"`), strings.Contains(body, "upstream-vendor"):
				t.Errorf("%s: the client got:\n%s", c.name, body)
			}
			if a := h.ended().outcome.Attempts[0]; a.Result != billing.FailedBeforeCommit || a.Finish != billing.FinishError {
				t.Errorf("%s: attempt %+v", c.name, a)
			}
		})
	}
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
			h := newHarness(t, ctx, options{client: protocol.Responses, entries: 2})
			beats := []beat{h.feed(0, 2, 3, 4)}
			if c.before != "" {
				beats = append(beats, beat{data: c.before})
			}
			h.up.eps[uuidOf(ep(1))].script = []scripted{serve(append(beats, beat{data: c.frame})...)}
			h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
			h.run()
			body, a := h.w.body(), h.ended().outcome.Attempts[0]
			last := body[strings.LastIndex(body, "event: "):]
			u := a.Segments[0]
			switch {
			case h.w.status != http.StatusOK:
				t.Errorf("%s: status %d:\n%s", c.name, h.w.status, body)
			case h.up.endpoint(2).sends != 0:
				t.Errorf("%s: another entry was tried after the commit point", c.name)
			case a.Result != billing.Committed || a.Finish != billing.FinishError:
				t.Errorf("%s: attempt %+v", c.name, a)
			case u.Usage.Count(catalog.ItemInput) != 900 || u.Usage.OutputTotal() != 700,
				!u.Reported.Has(catalog.ItemInput), !u.Reported.Has(catalog.ItemOutput):
				t.Errorf("%s: usage %+v, reported %d", c.name, u.Usage, u.Reported)
			case !strings.HasPrefix(last, "event: response.failed\n"):
				t.Errorf("%s: the stream ends with:\n%s", c.name, last)
			case !strings.Contains(last, `"code":"`+c.code+`"`):
				t.Errorf("%s: the ending says\n%s", c.name, last)
			case c.words != "" && !strings.Contains(last, `"message":"`+c.words+`"`):
				t.Errorf("%s: the ending says\n%s", c.name, last)
			}
			h.mu.Lock()
			settled := len(h.settlements)
			h.mu.Unlock()
			if settled != 1 {
				t.Errorf("%s: %d settlements", c.name, settled)
			}
		})
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
		switch {
		case h.w.status != http.StatusOK:
			t.Errorf("status %d:\n%s", h.w.status, body)
		case h.up.endpoint(2).sends != 0:
			t.Error("another entry was tried after the commit point")
		case a.Result != billing.Committed || a.Finish != billing.FinishError:
			t.Errorf("attempt %+v", a)
		case u.Reported.Has(catalog.ItemOutput), u.Reported.Has(catalog.ItemInput):
			t.Errorf("usage reported %d, want nothing reported: %+v", u.Reported, u.Usage)
		case u.Usage.OutputTotal() == 0:
			t.Errorf("the output of the content relayed was not estimated: %+v", u.Usage)
		case !strings.Contains(body, `"delta":"Hi"`):
			t.Errorf("the content relayed before the failure is missing:\n%s", body)
		}
		if last := body[strings.LastIndex(body, "event: "):]; !strings.HasPrefix(last, "event: response.failed\n") ||
			!strings.Contains(last, `"code":"upstream_error"`) {
			t.Errorf("the stream ends with:\n%s", last)
		}
	})
}
