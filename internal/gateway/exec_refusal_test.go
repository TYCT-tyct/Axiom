package gateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

type streamFailureCase struct {
	client, upstream protocol.Format
	fault            string
	content, stream  bool
}

// The same terminal failure retains its usage and meaning across every supported relay path.
func TestStreamFailureMeaningAndUsage(t *testing.T) {
	paths := []struct{ client, upstream protocol.Format }{
		{protocol.Messages, protocol.Messages},
		{protocol.Responses, protocol.Responses},
		{protocol.Chat, protocol.Chat},
		{protocol.Chat, protocol.Messages},
		{protocol.Chat, protocol.Responses},
		{protocol.Messages, protocol.Chat},
		{protocol.Responses, protocol.Chat},
	}
	for _, path := range paths {
		for _, fault := range []string{"invalid_request_error", "context_length_exceeded", "content_policy_violation", "permission_denied", "payload_too_large", "server_error", "rate_limit_error", "unknown_error"} {
			for _, content := range []bool{false, true} {
				for _, stream := range []bool{false, true} {
					c := streamFailureCase{path.client, path.upstream, fault, content, stream}
					t.Run(fmt.Sprintf("%v_from_%v/%s/content=%t/stream=%t", c.client, c.upstream, fault, content, stream), c.run)
				}
			}
		}
	}
}

func (c streamFailureCase) run(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{client: c.client, upstream: &c.upstream, stream: &c.stream, entries: 2})
		var beats []beat
		if c.content {
			beats = append(beats, failurePrelude(h, c.upstream))
		}
		beats = append(beats, beat{data: streamFailure(c.upstream, c.fault)})
		// Keep the connection open: terminal errors must not wait for EOF or an idle timeout.
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(beats...)}
		h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
		h.run()
		c.checkAttempt(t, h)
		c.checkReply(t, h)
		c.checkContent(t, h)
	})
}

func failurePrelude(h *harness, f protocol.Format) beat {
	switch f {
	case protocol.Messages:
		return h.feed(evStart, evBlock, evHello)
	case protocol.Responses:
		return h.feed(0, 2, 3, 4)
	case protocol.Chat:
	}
	return h.feed(0, 1)
}

func streamFailure(f protocol.Format, fault string) string {
	const usage = `{"input_tokens":900,"output_tokens":700}`
	errorObject := `{"type":"` + fault + `","message":"upstream-vendor: bad parameter"}`
	switch f {
	case protocol.Messages:
		return `data: {"type":"error","error":` + errorObject + `,"usage":` + usage + "}\n\n"
	case protocol.Chat:
		return `data: {"error":` + errorObject + `,"usage":{"prompt_tokens":900,"completion_tokens":700}}` + "\n\n"
	case protocol.Responses:
	}
	return `data: {"type":"response.failed","sequence_number":5,"response":{"error":` + errorObject +
		`,"usage":` + usage + "}}\n\n"
}

func (c streamFailureCase) requestStatus() int {
	switch c.fault {
	case "invalid_request_error", "context_length_exceeded":
		return http.StatusBadRequest
	case "content_policy_violation", "permission_denied":
		return http.StatusForbidden
	case "payload_too_large":
		return http.StatusRequestEntityTooLarge
	}
	return 0
}

func (c streamFailureCase) retried() bool {
	return c.requestStatus() == 0 && (!c.content || !c.stream)
}

func (c streamFailureCase) checkAttempt(t *testing.T, h *harness) {
	t.Helper()
	wantAttempts, wantScope := 1, routing.ScopeEndpoint
	if c.requestStatus() != 0 {
		wantScope = routing.ScopeRequest
	}
	if c.retried() {
		wantAttempts = 2
	}
	ends := h.ended().outcome.Attempts
	if len(ends) != wantAttempts || h.up.endpoint(2).sends != wantAttempts-1 {
		t.Fatalf("attempts %+v, second endpoint sends %d", ends, h.up.endpoint(2).sends)
	}
	if ends[0].Finish != billing.FinishError || h.elapsed() != 0 {
		t.Errorf("finish %v, elapsed %v; terminal frame should end immediately", ends[0].Finish, h.elapsed())
	}
	if got := h.call.Plan.Record.Attempts[0].Scope; got != wantScope {
		t.Errorf("scope %v, want %v", got, wantScope)
	}
	u := ends[0].Segments[0]
	if u.Usage.Count(catalog.ItemInput) != 900 || u.Usage.OutputTotal() != 700 {
		t.Errorf("failed attempt lost usage: %+v", u)
	}
	if !u.Reported.Has(catalog.ItemInput) || !u.Reported.Has(catalog.ItemOutput) {
		t.Errorf("failed attempt lost final usage flags: %+v", u)
	}
}

func (c streamFailureCase) status() int {
	if c.requestStatus() != 0 && !c.content {
		return c.requestStatus()
	}
	if c.content && !c.stream && c.client == protocol.Messages && !c.retried() {
		return http.StatusBadGateway
	}
	return http.StatusOK
}

func (c streamFailureCase) checkReply(t *testing.T, h *harness) {
	t.Helper()
	body := h.w.body()
	if h.w.status != c.status() {
		t.Fatalf("status %d, want %d: %s", h.w.status, c.status(), body)
	}
	if strings.Contains(body, "upstream-vendor") {
		t.Errorf("supplier leaked in response: %s", body)
	}
	if c.requestStatus() != 0 && !strings.Contains(body, "[hidden]: bad parameter") {
		t.Errorf("refusal lost upstream words: %s", body)
	}
	if c.requestStatus() == 0 && strings.Contains(body, "bad parameter") {
		t.Errorf("endpoint error exposed upstream words: %s", body)
	}
	if c.requestStatus() != 0 && c.fault != "invalid_request_error" && !strings.Contains(body, `"error_type":"`+c.fault+`"`) {
		t.Errorf("lost typed refusal %s: %s", c.fault, body)
	}
	if strings.Contains(h.ended().body, `"cost"`) {
		t.Error("cost was sent before settlement")
	}
}

func (c streamFailureCase) checkContent(t *testing.T, h *harness) {
	t.Helper()
	if !c.content || c.retried() {
		return
	}
	body := h.w.body()
	if c.stream {
		ending := map[protocol.Format]string{protocol.Chat: `"finish_reason":"error"`, protocol.Messages: "event: error", protocol.Responses: "event: response.failed"}[c.client]
		if !strings.Contains(body, ending) {
			t.Errorf("missing protocol error ending %s: %s", ending, body)
		}
	}
	if c.client == protocol.Messages && !c.stream {
		return
	}
	word := "Hello"
	if c.upstream == protocol.Responses {
		word = "Hi"
	}
	if !strings.Contains(body, word) {
		t.Errorf("partial content lost: %s", body)
	}
}

func TestStreamRefusalMatchesHTTP400(t *testing.T) {
	for _, f := range []protocol.Format{protocol.Chat, protocol.Messages, protocol.Responses} {
		for _, stream := range []bool{false, true} {
			run(t, func(t *testing.T, ctx context.Context) {
				httpBody := refusalAnswer(t, ctx, f, stream, false)
				sseBody := refusalAnswer(t, ctx, f, stream, true)
				if httpBody != sseBody {
					t.Errorf("format %v stream %t: HTTP %s; SSE %s", f, stream, httpBody, sseBody)
				}
			})
		}
	}
}

func refusalAnswer(t *testing.T, ctx context.Context, f protocol.Format, stream, inStream bool) string {
	t.Helper()
	h := newHarness(t, ctx, options{client: f, stream: &stream, entries: 2})
	said := protocol.Rejection{Type: "invalid_request_error", Message: "[hidden]: invalid input", Code: "invalid_value", Param: "input"}
	if !inStream {
		h.up.eps[uuidOf(ep(1))].script = []scripted{func(context.Context) (*http.Response, *suppliers.Failure) {
			return nil, failing(routing.ScopeRequest, true, suppliers.Failure{Rejection: &said})
		}}
	} else {
		obj := `{"type":"invalid_\u0072equest_error","message":"upstream-vendor: invalid input","code":"invalid_value","param":"input"}`
		frame := `data: {"type":"error","error":` + obj + "}\n\n"
		if f == protocol.Responses {
			frame = `data: {"type":"error","code":"invalid_value","message":"upstream-vendor: invalid input","param":"input"}` + "\n\n"
		}
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(beat{data: frame})}
	}
	h.run()
	if h.w.status != 400 || h.up.endpoint(2).sends != 0 {
		t.Fatalf("format %v inStream %t: status %d, sends %d: %s", f, inStream, h.w.status, h.up.endpoint(2).sends, h.w.body())
	}
	return h.w.body()
}
