package gateway

import (
	"net/http"
	"strings"
	"testing"

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
			[]string{"X-Goog-Api-Key", a.key}, http.StatusOK, "text/event-stream",
			[]string{`data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"index":0}]`, `"finishReason":"STOP"`, `"usageMetadata":{"candidatesTokenCount":`},
		},
		{
			"whole, key in the query", "/v1beta/models/acme/beta:generateContent?key=" + a.key, "", body,
			nil, http.StatusOK, "application/json",
			[]string{`"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP","index":0}]`, `"usageMetadata":{"candidatesTokenCount":`},
		},
		{"counted", "/v1beta/models/acme/beta:countTokens", a.key, body, nil, http.StatusOK, "application/json", []string{`{"totalTokens":`}},
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
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("the answer lacks %s:\n%s", want, got)
				}
			}
			if strings.Contains(got, `"choices"`) || strings.Contains(got, "[DONE]") {
				t.Errorf("Chat's shape leaked:\n%s", got)
			}
		})
	}
}
