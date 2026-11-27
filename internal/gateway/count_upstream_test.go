package gateway

import (
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// TestCountAsksAnUpstream: a count goes to the first Messages endpoint of the model's route, under the endpoint's name
// for the model and with only the members Anthropic's count takes, and answers its number. A relay without the route,
// a failure, no answer in time and an answer that is not a count are all answered with the local estimate, and so is a
// model that no endpoint serves in Messages. The count is sent as one that rests nothing, and its credential is given
// back whatever happened.
func TestCountAsksAnUpstream(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(name string, e *routing.Endpoint) {
			e.Target.Model = "up-" + name
			if name == "beta-1" {
				e.Native = routing.Of(protocol.Chat)
			}
		}
	})
	up := r.fakeUpstreams()
	a := r.newAccount(5e9)
	const (
		messages = `"messages":[{"role":"user","content":"hello"}]`
		body     = `{"model":"acme/alpha","max_tokens":10,"stream":false,"provider":{"sort":"price"},"metadata":{"user_id":"u"},"system":"be brief",` + messages + `}`
		sent     = `{"model":"up-alpha-1","system":"be brief",` + messages + `}`
	)
	up.mu.Lock()
	alpha, beta := up.eps[uuid.UUID(r.endpoints["alpha-1"].ID)], up.eps[uuid.UUID(r.endpoints["beta-1"].ID)]
	up.mu.Unlock()
	for name, c := range map[string]struct {
		model    string
		answer   scripted
		upstream bool // the answer is the upstream's 42, not the estimate
	}{
		"the upstream's count":      {"alpha", wholeAnswer(`{"input_tokens":42}`), true},
		"a relay without the route": {"alpha", failure(routing.ScopeCredential, true), false},
		"a failure":                 {"alpha", failure(routing.ScopeEndpoint, false), false},
		"no answer in time":         {"alpha", late(time.Minute, wholeAnswer(`{"input_tokens":42}`)), false},
		"not a count":               {"alpha", wholeAnswer(`{"input_tokens":"many"}`), false},
		"no Messages endpoint":      {"beta", wholeAnswer(`{"input_tokens":42}`), false},
	} {
		up.mu.Lock()
		alpha.script, beta.script = []scripted{c.answer}, []scripted{c.answer}
		calls := len(alpha.calls)
		up.mu.Unlock()
		asked := strings.Replace(body, "alpha", c.model, 1)
		want := estimateOf(t, asked)
		if c.upstream {
			want = 42
		}
		if got := r.countTokens(a, asked); got != want {
			t.Errorf("%s: %d tokens, want %d", name, got, want)
		}
		wantSent := ""
		if c.model == "alpha" {
			wantSent = sent
		}
		checkCountSent(t, name, up, alpha, calls, wantSent)
		up.mu.Lock()
		if alpha.acquired != alpha.done || beta.acquired != 0 {
			t.Errorf("%s: %d of %d credentials given back; beta's taken %d times", name, alpha.done, alpha.acquired, beta.acquired)
		}
		up.mu.Unlock()
	}
	if r.count(allRows) != 0 || len(r.calls()) != 0 || r.count("SELECT balance FROM billing.balances WHERE account_id = $1", a.id) != 5e9 {
		t.Error("counting reserved, ran or charged something")
	}
}

// checkCountSent: the endpoint was sent one count of the body after its first `before` calls, as a count, or nothing
// when body is empty.
func checkCountSent(t *testing.T, name string, up *fakeUpstreams, e *fakeEndpoint, before int, body string) {
	t.Helper()
	up.mu.Lock()
	defer up.mu.Unlock()
	calls := e.calls[before:]
	if body == "" && len(calls) != 0 || body != "" && (len(calls) != 1 || !calls[0].Count || string(calls[0].Body) != body) {
		t.Errorf("%s: sent %+v, want one count of %q", name, calls, body)
	}
}

// estimateOf is the local estimate of a Messages body.
func estimateOf(t *testing.T, body string) int64 {
	t.Helper()
	req, err := protocol.Inspect(protocol.Messages, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return countInput(req)
}

// countTokens is the count the handler answers for the body, which must be a 200.
func (r *rig) countTokens(a account, body string) int64 {
	r.t.Helper()
	resp, out := r.do(http.MethodPost, "/v1/messages/count_tokens", a.key, strings.NewReader(body))
	var got struct {
		InputTokens int64 `json:"input_tokens"`
	}
	if err := jsonx.Unmarshal([]byte(out), &got); err != nil || resp.StatusCode != http.StatusOK {
		r.t.Errorf("count: %d %s (%v)", resp.StatusCode, out, err)
	}
	return got.InputTokens
}
