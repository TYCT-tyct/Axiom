package gateway

import (
	"net/http"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/routing"
)

// TestEveryFormatRefusesInItsOwnShape: each way a call is refused — the key, the balance, the key's limit, the model,
// the upstream, the rate — answers every inbound format with the right status, in that format's own error shape, with
// the unified error type and the request ID: the header always, and Anthropic's body member for Messages.
func TestEveryFormatRefusesInItsOwnShape(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	for _, e := range up.eps {
		e.script = []scripted{failure(routing.ScopeEndpoint, false)}
	}
	formats := []struct {
		name string
		send func(key, model string) (reply, string)
	}{
		{"chat", func(key, model string) (reply, string) {
			return r.do(http.MethodPost, "/v1/chat/completions", key, strings.NewReader(`{"model":"`+model+`","max_tokens":10,"service_tier":"default","messages":[{"role":"user","content":"hi"}]}`))
		}},
		{"responses", func(key, model string) (reply, string) {
			return r.do(http.MethodPost, "/v1/responses", key, strings.NewReader(`{"model":"`+model+`","max_output_tokens":10,"service_tier":"default","input":"hi"}`))
		}},
		{"messages", func(key, model string) (reply, string) {
			return r.do(http.MethodPost, "/v1/messages", key, strings.NewReader(messageBody(model)), "Anthropic-Version", "2023-06-01")
		}},
		{"gemini", func(key, model string) (reply, string) {
			return r.do(http.MethodPost, "/v1beta/models/"+model+":generateContent", key, strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":10}}`))
		}},
	}
	refusals := []struct {
		name, errorType, google string
		status                  int
		send                    func(send func(key, model string) (reply, string)) (reply, string)
	}{
		{"auth", "authentication", "UNAUTHENTICATED", 401, func(send func(string, string) (reply, string)) (reply, string) {
			return send("sk-ax-v1-nope", "acme/beta")
		}},
		{"balance", "credits_insufficient", "RESOURCE_EXHAUSTED", 402, func(send func(string, string) (reply, string)) (reply, string) {
			return send(r.newAccount(0).key, "acme/beta")
		}},
		{"key limit", "credits_insufficient", "PERMISSION_DENIED", 403, func(send func(string, string) (reply, string)) (reply, string) {
			a := r.funded(1e12)
			r.sql("UPDATE accounts.api_keys SET spend_limit = 0 WHERE account_id = '" + a.id.String() + "'")
			return send(a.key, "acme/beta")
		}},
		{"model not found", "model_not_found", "NOT_FOUND", 404, func(send func(string, string) (reply, string)) (reply, string) {
			return send(r.funded(1e12).key, "acme/none")
		}},
		{"upstream failure", "upstream_error", "UNAVAILABLE", 502, func(send func(string, string) (reply, string)) (reply, string) {
			return send(r.funded(1e12).key, "acme/beta")
		}},
		{"rate limit", "rate_limited", "RESOURCE_EXHAUSTED", 429, func(send func(string, string) (reply, string)) (reply, string) {
			a := r.funded(1e12)
			r.sql("UPDATE accounts.accounts SET rate_requests = 1 WHERE id = '" + a.id.String() + "'")
			send(a.key, "acme/beta") // the one call the window allows
			return send(a.key, "acme/beta")
		}},
	}
	for _, f := range formats {
		for _, c := range refusals {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				resp, body := c.send(f.send)
				id := resp.Header.Get("X-Request-ID")
				if resp.StatusCode != c.status || id == "" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
					t.Fatalf("%d, request ID %q: %s", resp.StatusCode, id, body)
				}
				var got struct {
					Type      string `json:"type"`
					RequestID string `json:"request_id"`
					Error     struct {
						Type      string `json:"type"`
						Message   string `json:"message"`
						Code      int    `json:"code"`
						Status    string `json:"status"`
						ErrorType string `json:"error_type"`
						Metadata  struct {
							ErrorType string `json:"error_type"`
						} `json:"metadata"`
						Details []struct {
							Reason string `json:"reason"`
						} `json:"details"`
					} `json:"error"`
				}
				if err := jsonx.Unmarshal([]byte(body), &got); err != nil || got.Error.Message == "" {
					t.Fatalf("not an error object (%v): %s", err, body)
				}
				e := got.Error
				var ok bool
				switch f.name {
				case "messages":
					ok = got.Type == "error" && e.Type != "" && e.ErrorType == c.errorType && got.RequestID == id
				case "gemini":
					ok = e.Code == c.status && e.Status == c.google && len(e.Details) == 1 && e.Details[0].Reason == c.errorType
				default:
					ok = e.Code == c.status && e.Type != "" && e.Metadata.ErrorType == c.errorType
				}
				if !ok {
					t.Errorf("not %s's shape for %s (request %s): %s", f.name, c.errorType, id, body)
				}
			})
		}
	}
}
