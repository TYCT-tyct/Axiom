package gateway

import (
	"net/http"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

func TestUnsupportedPluginsAreRejectedBeforeAdmission(t *testing.T) {
	r := newRig(t)
	a := r.newAccount(1e9)
	for _, members := range []string{
		`"plugins":[{"id":"unknown"}]`,
		`"plugins":[{"id":"web"}]`,
		`"plugins":[{"id":"file-parser"}]`,
		`"transforms":["unknown"]`,
		`"plugins":[{"id":"response-healing","unknown":true}]`,
	} {
		resp, body := r.do(http.MethodPost, "/api/v1/chat/completions", a.key, strings.NewReader(`{"model":"acme/alpha",`+members+`,"messages":[{"role":"user","content":"hello"}]}`))
		var envelope struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := jsonx.Unmarshal([]byte(body), &envelope); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if resp.StatusCode != http.StatusBadRequest || envelope.Error.Code != 400 || !strings.Contains(envelope.Error.Message, "not supported") {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
	}
	if len(r.calls()) != 0 || r.count(allRows) != 0 {
		t.Fatal("unsupported plugin was admitted")
	}
	r.idle(1 << 30)
}

func TestCompressionReinspectsTheBodyBeforeAdmission(t *testing.T) {
	r := newRig(t)
	a := r.newAccount(1e9)
	content := "start" + strings.Repeat("middle", 30000) + "end"
	resp, body := r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(`{"model":"acme/alpha","service_tier":"default","max_tokens":100,"plugins":[{"id":"context-compression"}],"messages":[{"role":"user","content":"`+content+`"}]}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	calls := r.calls()
	if len(calls) != 1 {
		t.Fatalf("%d calls", len(calls))
	}
	call := calls[0]
	if len(call.Body) >= len(content) || int64(len(call.Body)) > 128000-100 {
		t.Fatalf("not compressed: %d", len(call.Body))
	}
	inspected, err := protocol.Inspect(protocol.Chat, call.Body)
	if err != nil || inspected.TextBytes != call.Request.TextBytes {
		t.Fatalf("stale summary: %d vs %d, %v", inspected.TextBytes, call.Request.TextBytes, err)
	}
	up, err := protocol.Prepare(&call.Request, call.Body, protocol.Target{Format: protocol.Chat, Model: "up", MaxOutput: 100, Streams: true}, protocol.Rewrites{})
	if err != nil || strings.Contains(string(up.Body), "plugins") || !strings.Contains(string(up.Body), "start") || !strings.Contains(string(up.Body), "end") {
		t.Fatalf("%v: %s", err, up.Body)
	}
	checkCompressionControls(t, r, a, content)
	r.idle(1 << 30)
}

func checkCompressionControls(t *testing.T, r *rig, a account, content string) {
	t.Helper()
	// A caller can explicitly disable the lossy transform, and protected overflow must fail before admission.
	disabled := `{"model":"acme/alpha","service_tier":"default","max_tokens":100,"transforms":[],"messages":[{"role":"user","content":"` + content + `"}]}`
	resp, body := r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(disabled))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("opt-out: %d %s", resp.StatusCode, body)
	}
	if string(r.calls()[1].Body) != disabled {
		t.Fatal("opt-out changed the prompt")
	}
	protected := `{"model":"acme/alpha","service_tier":"default","max_tokens":100,"plugins":[{"id":"context-compression"}],"messages":[{"role":"system","content":"` + content + `"}]}`
	resp, body = r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(protected))
	if resp.StatusCode != http.StatusBadRequest || len(r.calls()) != 2 {
		t.Fatalf("protected prompt: %d %s", resp.StatusCode, body)
	}
}
