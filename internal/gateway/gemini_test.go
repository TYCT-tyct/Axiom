package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/protocol"
)

// TestGeminiPaths: the Gemini API's paths answer a call routed and billed as any other, in Gemini's shape: streamed
// with alt=sse, whole, counted, and refused with Google's error object; the key comes as Google's clients send it.
func TestGeminiPaths(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	sample, _ := sample(t, protocol.Chat) // the rig's endpoints speak Chat too, which a Gemini call runs as
	for _, e := range up.eps {
		e.script = []scripted{serve(beat{data: strings.Join(sample, "")})}
	}
	a := r.funded(1e12)
	const body = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":64}}`
	for _, c := range []struct {
		name, path, key, body string
		headers               []string
		status                int
		contentType           string
		want                  []string
	}{
		{
			"streamed, key in x-goog-api-key", "/v1beta/models/acme/beta:streamGenerateContent?alt=sse", "", body,
			[]string{"X-Goog-Api-Key", a.key},
			http.StatusOK, "text/event-stream",
			[]string{`data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP","index":0}]`, `"finishReason":"STOP"`, `"usageMetadata":{"candidatesTokenCount":`},
		},
		{
			"whole, key in the query", "/v1beta/models/acme/beta:generateContent?key=" + a.key, "", body,
			nil, http.StatusOK, "application/json",
			[]string{`"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP","index":0}]`, `"usageMetadata":{"candidatesTokenCount":`},
		},
		{"counted", "/v1beta/models/acme/beta:countTokens", a.key, body, nil, http.StatusOK, "application/json", []string{`{"totalTokens":`}},
		{
			"counted as the whole request it would be", "/v1beta/models/acme/beta:countTokens", a.key,
			`{"generateContentRequest":{"model":"models/acme/beta","systemInstruction":{"parts":[{"text":"Rules"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}],` +
				`"tools":[{"functionDeclarations":[{"name":"f","parametersJsonSchema":{"type":"object"}}]}]}}`,
			nil, http.StatusOK, "application/json",
			[]string{`{"totalTokens":`},
		},
		{
			"counted twice over", "/v1beta/models/acme/beta:countTokens", a.key,
			`{"contents":[{"parts":[{"text":"a"}]}],"generateContentRequest":{"model":"models/acme/beta","contents":[]}}`,
			nil, http.StatusBadRequest, "application/json",
			[]string{`"status":"INVALID_ARGUMENT"`, "generateContentRequest"},
		},
		{"no key", "/v1beta/models/acme/beta:generateContent", "", body, nil, http.StatusUnauthorized, "application/json", []string{`"code":401`, `"status":"UNAUTHENTICATED"`}},
		{"a stream without alt=sse", "/v1beta/models/acme/beta:streamGenerateContent", a.key, body, nil, http.StatusBadRequest, "application/json", []string{`"status":"INVALID_ARGUMENT"`, "alt=sse"}},
		{"a method not served", "/v1beta/models/acme/beta:embedContent", a.key, body, nil, http.StatusNotFound, "application/json", []string{`"status":"NOT_FOUND"`}},
		{"Google's own tool", "/v1beta/models/acme/beta:generateContent", a.key, `{"contents":[],"tools":[{"googleSearch":{}}]}`, nil, http.StatusBadRequest, "application/json", []string{"googleSearch"}},
		{"a model nobody serves", "/v1beta/models/acme/none:generateContent", a.key, body, nil, http.StatusNotFound, "application/json", []string{`"status":"NOT_FOUND"`, `"reason":"model_not_found"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp, got := r.do(http.MethodPost, c.path, c.key, strings.NewReader(c.body), c.headers...)
			if resp.StatusCode != c.status || !strings.HasPrefix(resp.Header.Get("Content-Type"), c.contentType) {
				t.Fatalf("%d %s: %s", resp.StatusCode, resp.Header.Get("Content-Type"), got)
			}
			checkGeminiAnswer(t, got, c.want)
		})
	}
}

// TestGeminiFailureChargesOnlyWhatReachedTheClient: a call that fails after the upstream began answering is charged its
// model usage only when the Gemini client got some of it (docs/domains/billing.md「免单规则」). A whole reply that broke
// off is a 502 without the text, so nothing was delivered; a stream delivers the text it held back, but neither the
// arguments of a call that never finished nor the thoughts the client did not ask for.
func TestGeminiFailureChargesOnlyWhatReachedTheClient(t *testing.T) {
	t.Parallel()
	const chunk = `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":%s,"finish_reason":null}]}` + "\n\n"
	text := fmt.Sprintf(chunk, `{"role":"assistant","content":"Half an answer"}`)
	call := fmt.Sprintf(chunk, `{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"ci"}}]}`)
	thought := fmt.Sprintf(chunk, `{"reasoning_content":"Weighing it."}`)
	const body = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":64}}`
	for _, c := range []struct {
		name, method, data string
		status             int
		charged            bool
	}{
		{"a whole reply cut off", "generateContent", text, http.StatusBadGateway, false},
		{"a stream of text cut off", "streamGenerateContent?alt=sse", text, http.StatusOK, true},
		{"a stream of a call's arguments cut off", "streamGenerateContent?alt=sse", call, http.StatusOK, false},
		{"a stream of hidden thoughts cut off", "streamGenerateContent?alt=sse", thought, http.StatusOK, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := ledgerRig(t) // a rig each: the attempts one case spends on the retry budget would refuse the next
			up := withExecutor(r, &flakyLedger{})
			a := r.funded(1e12)
			for _, e := range up.eps {
				e.script = []scripted{serve(beat{data: c.data}, beat{hangup: true})}
			}
			resp, got := r.do(http.MethodPost, "/v1beta/models/acme/beta:"+c.method, a.key, strings.NewReader(body))
			if resp.StatusCode != c.status {
				t.Fatalf("%d: %s", resp.StatusCode, got)
			}
			r.exec.Outstanding.Drain(t.Context(), time.Minute)
			if charged := r.charged(r.lastCall().ID); (charged > 0) != c.charged {
				t.Errorf("charged %d, want a charge %v: %s", charged, c.charged, got)
			}
		})
	}
}

// TestGeminiStreamCutForFundsChargesOnlyWhatReachedTheClient: a stream the gateway cuts because the reservation could
// not be topped up is charged its model usage only when the Gemini client got some of it: text, which it did, and not
// thoughts it did not ask for (docs/domains/billing.md「免单规则」).
func TestGeminiStreamCutForFundsChargesOnlyWhatReachedTheClient(t *testing.T) {
	t.Parallel()
	const chunk = `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":%s,"finish_reason":null}]}` + "\n\n"
	long := strings.Repeat("word ", 400) // past the reserved segment, so the reservation is topped up
	const body = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":1000}}`
	for _, c := range []struct {
		name, member string
		charged      bool
	}{
		{"text", "content", true},
		{"hidden thoughts", "reasoning_content", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := ledgerRig(t)
			up := withExecutor(r, &flakyLedger{extend: func() (bool, error) { return false, errors.New("connection refused") }})
			a := r.funded(1e12)
			for _, e := range up.eps {
				e.script = []scripted{hold(beat{data: fmt.Sprintf(chunk, `{"role":"assistant","`+c.member+`":"A start."}`)},
					beat{data: fmt.Sprintf(chunk, `{"`+c.member+`":"`+long+`"}`)})}
			}
			resp, got := r.do(http.MethodPost, "/v1beta/models/acme/beta:streamGenerateContent?alt=sse", a.key, strings.NewReader(body))
			if resp.StatusCode != http.StatusOK || !strings.Contains(got, "billing_unavailable") {
				t.Fatalf("%d: %s", resp.StatusCode, got)
			}
			r.exec.Outstanding.Drain(t.Context(), time.Minute)
			if charged := r.charged(r.lastCall().ID); (charged > 0) != c.charged {
				t.Errorf("charged %d, want a charge %v: %s", charged, c.charged, got)
			}
		})
	}
}

// checkGeminiAnswer: the answer holds what it must, and nothing of Chat's shape.
func checkGeminiAnswer(t *testing.T, got string, want []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("the answer lacks %s:\n%s", w, got)
		}
	}
	if strings.Contains(got, `"choices"`) || strings.Contains(got, "[DONE]") {
		t.Errorf("Chat's shape leaked:\n%s", got)
	}
}
