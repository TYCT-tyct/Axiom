package vendors_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/vendors"
)

// listClient answers every request with one model list and keeps what it was sent.
type listClient struct {
	body []byte
	sent []adapter.Request
}

func (c *listClient) Do(_ context.Context, r adapter.Request) (adapter.Response, error) {
	c.sent = append(c.sent, r)
	return adapter.Response{Status: http.StatusOK, Body: c.body}, nil
}

// TestEveryRowReachesItsProvider: each row is a type the registry starts with, checks a key at its model list with a
// bearer token and reads the provider's answer, sends Chat under its prefix and, with an Anthropic-compatible API,
// Messages under that API's root. The samples in testdata follow each provider's documented example answer; they are
// not recorded against a live account.
func TestEveryRowReachesItsProvider(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind              adapter.Kind
		models, chat, msg string // msg is empty for a row without an Anthropic-compatible API
		names             []string
	}{
		{"openai", "/v1/models", "/v1/chat/completions", "", []string{"model-id-0", "model-id-1"}},
		{"google", "/v1beta/openai/models", "/v1beta/openai/chat/completions", "", []string{"gemini-2.5-flash", "text-embedding-004"}},
		{"moonshot", "/v1/models", "/v1/chat/completions", "/anthropic/v1/messages", []string{"kimi-k3", "kimi-k3.1-pro"}},
		{"deepseek", "/v1/models", "/v1/chat/completions", "/anthropic/v1/messages", []string{"deepseek-flash", "deepseek-v4-pro"}},
		{"ark", "/api/v3/models", "/api/v3/chat/completions", "", []string{"doubao-seed-2-1-pro-260915"}},
		{"qianfan", "/v2/models", "/v2/chat/completions", "", []string{"deepseek-v3.2"}},
		{"minimax", "/v1/models", "/v1/chat/completions", "/anthropic/v1/messages", []string{"MiniMax-M3", "MiniMax-M3.1-Flash"}},
		{"xai", "/v1/models", "/v1/chat/completions", "", []string{"grok-420-reasoning", "grok-imagine-image", "latest"}},
		{"mistral", "/v1/models", "/v1/chat/completions", "", []string{"mistral-large-4"}},
	}
	if len(cases) != len(vendors.New()) {
		t.Fatalf("%d cases for %d rows: every row has a case", len(cases), len(vendors.New()))
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			t.Parallel()
			a := row(t, tc.kind)
			if err := a.(adapter.Described).Type().Check(); err != nil {
				t.Fatalf("type: %v", err)
			}
			body, err := os.ReadFile(filepath.Join("testdata", string(tc.kind)+"-models.json"))
			if err != nil {
				t.Fatal(err)
			}
			c := &listClient{body: body}
			got := a.(adapter.Direct).CheckKey(t.Context(), c, adapter.FamilyOpenAI, secret.New("sk-test-key"), nil)
			if got.Outcome != adapter.KeyPassed {
				t.Fatalf("check: %+v", got)
			}
			if diff := cmp.Diff(tc.names, got.Models); diff != "" {
				t.Errorf("listed names (-want +got):\n%s", diff)
			}
			if r := c.sent[0]; r.Path != tc.models || r.Header.Get("Authorization") != "Bearer sk-test-key" {
				t.Errorf("check asked %s with %q", r.Path, r.Header.Get("Authorization"))
			}
			wire := a.Calls().(adapter.Wire)
			if got := wire.Path(protocol.Chat, "/v1/chat/completions"); got != tc.chat {
				t.Errorf("chat path %s, want %s", got, tc.chat)
			}
			if tc.msg != "" {
				if got := wire.Path(protocol.Messages, "/v1/messages"); got != tc.msg {
					t.Errorf("messages path %s, want %s", got, tc.msg)
				}
			}
		})
	}
}

// row is the table's adapter of the kind.
func row(t *testing.T, kind adapter.Kind) adapter.Adapter {
	t.Helper()
	for _, a := range vendors.New() {
		if a.Kind() == kind {
			return a
		}
	}
	t.Fatalf("no row %s", kind)
	return nil
}

// TestBalanceRowsReadTheirKeysWallet: the rows that say Balance (DeepSeek and Kimi, no other) read a key's balance at
// the provider's path with a bearer token, exactly as the provider's answer gives it: DeepSeek's in the entry's
// currency among those the account holds, Kimi's number in the entry's currency, which its answer does not name. The
// samples follow each provider's documented example answer; they are not recorded against a live account.
func TestBalanceRowsReadTheirKeysWallet(t *testing.T) {
	t.Parallel()
	cases := map[adapter.Kind]struct{ path, entry, amount, currency string }{
		"deepseek": {"/user/balance", "CNY", "110", "CNY"},
		"moonshot": {"/v1/users/me/balance", "USD", "49.58894", "USD"},
	}
	for _, a := range vendors.New() {
		tc, reads := cases[a.Kind()]
		if a.(adapter.Described).Type().Balance != reads {
			t.Errorf("%s: says Balance %v", a.Kind(), !reads)
		}
		if !reads {
			continue
		}
		body, err := os.ReadFile(filepath.Join("testdata", string(a.Kind())+"-balance.json"))
		if err != nil {
			t.Fatal(err)
		}
		entry, err := platform.ParseCurrency(tc.entry)
		if err != nil {
			t.Fatal(err)
		}
		c := &listClient{body: body}
		got, err := a.(adapter.Balancer).ReadBalance(t.Context(), c, secret.New("sk-test-key"), nil, entry)
		want, wantErr := adapter.ParseMoney(tc.amount, tc.currency)
		if err != nil || wantErr != nil || got.Amount.Cmp(want.Amount) != 0 || got.Currency != want.Currency {
			t.Errorf("%s: %+v (%v), want %s %s", a.Kind(), got, err, tc.amount, tc.currency)
		}
		if r := c.sent[0]; r.Path != tc.path || r.Header.Get("Authorization") != "Bearer sk-test-key" {
			t.Errorf("%s: asked %s with %q", a.Kind(), r.Path, r.Header.Get("Authorization"))
		}
	}
}
