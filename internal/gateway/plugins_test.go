package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

func TestUnsupportedPluginsAreRejectedBeforeAdmission(t *testing.T) {
	r := newRig(t)
	a := r.newAccount(1e9)
	for _, members := range []string{
		`"plugins":[{"id":"unknown"}]`,
		`"plugins":[{"id":"web"}]`,
		`"plugins":[{"id":"file-parser","pdf":{"engine":"mistral-ocr"}}]`,
		`"plugins":[{"id":"file-parser","pdf":{"engine":"tika"}}]`,
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
		if resp.StatusCode != http.StatusBadRequest || envelope.Error.Code != http.StatusBadRequest || !strings.Contains(envelope.Error.Message, "not supported") {
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

func TestDefaultCompressionPreservesServiceableRequests(t *testing.T) {
	r := newRig(t)
	a := r.newAccount(1e9)
	addSmallContextModel(r, "acme/small-context", 4096)
	addSmallContextModel(r, "acme/full-output", 8192)
	for _, tc := range []struct {
		name, model, role string
		words             int
		compressed        bool
	}{
		{"input fits with default output", "acme/small-context", "user", 1000, false},
		{"protected input fits", "acme/small-context", "system", 900, false},
		{"output limit equals context", "acme/full-output", "user", 1, false},
		{"protected overflow is best effort", "acme/small-context", "system", 1800, false},
		{"no output room is best effort", "acme/full-output", "user", 1800, false},
		{"compressible overflow still runs", "acme/small-context", "user", 2400, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"` + tc.model + `","service_tier":"default","messages":[{"role":"` + tc.role + `","content":"` + strings.Repeat("word ", tc.words) + `"},{"role":"user","content":"last"}]}`
			before := len(r.calls())
			resp, result := r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(body))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%d %s", resp.StatusCode, result)
			}
			calls := r.calls()
			if len(calls) != before+1 {
				t.Fatal("request was not admitted")
			}
			call := calls[before]
			if changed := string(call.Body) != body; changed != tc.compressed {
				t.Fatalf("prompt changed=%t, want %t", changed, tc.compressed)
			}
		})
	}
	checkDefaultCompressionBudget(t, r)
	r.idle(1 << 30)
}

func addSmallContextModel(r *rig, name string, output int64) {
	r.t.Helper()
	spec := fmt.Sprintf(`{"items":["input","output"],"tiers":[],"context_length":8192,"max_output_tokens":%d,"streams":true,"abilities":[]}`, output)
	model := r.listed(name, spec, cheap)
	if err := r.catalog.Refresh(r.t.Context()); err != nil {
		r.t.Fatal(err)
	}
	e := r.endpoints["claude-1"]
	e.Context, e.MaxOutput = 8192, output
	r.endpoints["claude-1"] = e
	r.attachEndpoint(model)
}

func checkDefaultCompressionBudget(t *testing.T, r *rig) {
	t.Helper()
	snap, err := r.catalog.Current()
	if err != nil {
		t.Fatal(err)
	}
	model, err := snap.ResolvePublic("acme/small-context")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"acme/small-context","messages":[{"role":"user","content":"` + strings.Repeat("word ", 2400) + `"}]}`)
	req, err := protocol.Inspect(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler{Memory: protocol.NewMemory(0)}
	out, _, err := h.compress(&Call{}, body, req, snap, []catalog.ModelID{model.Model})
	if err != nil || string(out) != string(body) {
		t.Fatalf("default compression changed a budget refusal: %v", err)
	}
	body = []byte(strings.Replace(string(body), `"messages":`, `"plugins":[{"id":"context-compression"}],"messages":`, 1))
	req, err = protocol.Inspect(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = h.compress(&Call{}, body, req, snap, []catalog.ModelID{model.Model})
	if !errors.Is(err, ErrOverloaded) {
		t.Fatalf("explicit compression: %v", err)
	}
}
