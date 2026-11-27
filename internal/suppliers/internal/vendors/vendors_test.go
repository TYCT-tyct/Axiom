package vendors_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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
	cases := []rowCase{
		{"openai", "/v1/models", "/v1/chat/completions", "", []string{"model-id-0", "model-id-1"}},
		{"moonshot", "/v1/models", "/v1/chat/completions", "/anthropic/v1/messages", []string{"kimi-k3", "kimi-k3.1-pro"}},
		{"deepseek", "/v1/models", "/v1/chat/completions", "/anthropic/v1/messages", []string{"deepseek-flash", "deepseek-v4-pro"}},
		{"ark", "/api/v3/models", "/api/v3/chat/completions", "", []string{"doubao-seed-2-1-pro-260915"}},
		{"qianfan", "/v2/models", "/v2/chat/completions", "", []string{"deepseek-v3.2"}},
		{"minimax", "/v1/models", "/v1/chat/completions", "/anthropic/v1/messages", []string{"MiniMax-M3", "MiniMax-M3.1-Flash"}},
		{"zhipu", "/api/paas/v4/models", "/api/paas/v4/chat/completions", "", []string{"glm-5.3", "glm-5.3-flash"}},
		{"bailian", "/compatible-mode/v1/models", "/compatible-mode/v1/chat/completions", "/apps/anthropic/v1/messages", []string{"qwen3.8-flash", "qwen4-max", "text-embedding-v4"}},
		{"openrouter", "/api/v1/models/user", "/api/v1/chat/completions", "", []string{"liquid/lfm-2.5-2.6b:free", "openai/gpt-4"}},
		{"siliconflow", "/v1/models", "/v1/chat/completions", "", []string{"Qwen/Qwen3-Embedding-8B", "deepseek-ai/DeepSeek-V4-Pro"}},
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
			checkList(t, a, tc)
			checkPaths(t, a, tc)
		})
	}
}

// rowCase is what one row is expected to do; msg is empty for a row without an Anthropic-compatible API.
type rowCase struct {
	kind              adapter.Kind
	models, chat, msg string
	names             []string
}

// checkList checks a key against the row's sample answer: the path and bearer token asked, and the names read.
func checkList(t *testing.T, a adapter.Adapter, tc rowCase) {
	t.Helper()
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
}

// checkPaths checks where Chat, and Messages for a row with an Anthropic-compatible API, are sent.
func checkPaths(t *testing.T, a adapter.Adapter, tc rowCase) {
	t.Helper()
	wire := a.Calls().(adapter.Wire)
	if got := wire.Path(protocol.Chat, "/v1/chat/completions"); got != tc.chat {
		t.Errorf("chat path %s, want %s", got, tc.chat)
	}
	if got := wire.Path(protocol.Messages, "/v1/messages"); tc.msg != "" && got != tc.msg {
		t.Errorf("messages path %s, want %s", got, tc.msg)
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

// TestOnlyAnAuthorsOwnAPIIsPricedFromTheCatalog: a row sells at the catalog's official prices only the models of the
// one author whose own API it is (its vendor); an aggregator, which sells any author's models at prices of its own,
// names no author, so none of its endpoints is priced from the catalog (docs/domains/suppliers-direct.md「聚合商按自己的
// 价」). OpenRouter's list does carry prices, but they are one provider's of the several a call may be routed to, not
// what the call is charged.
func TestOnlyAnAuthorsOwnAPIIsPricedFromTheCatalog(t *testing.T) {
	t.Parallel()
	aggregators := map[adapter.Kind]bool{"openrouter": true, "siliconflow": true}
	for _, a := range vendors.New() {
		typ := a.(adapter.Described).Type()
		want := []string{typ.Vendor}
		if aggregators[a.Kind()] {
			want = nil
		}
		if diff := cmp.Diff(want, typ.Serves); diff != "" {
			t.Errorf("%s serves (-want +got):\n%s", a.Kind(), diff)
		}
	}
}

// TestARowSpeaksMessagesOnlyWhereTheKeysBalancePaysForIt: Zhipu's Anthropic-compatible API draws an older key's coding
// plan quota while the plan lasts and refuses the key once it ends, while the key still works for Chat
// (docs/domains/suppliers-direct.md「决策与理由」); so Zhipu speaks OpenAI's family alone, and a Messages request reaches it
// through the gateway's conversion.
func TestARowSpeaksMessagesOnlyWhereTheKeysBalancePaysForIt(t *testing.T) {
	t.Parallel()
	if got := row(t, "zhipu").(adapter.Described).Type().Families; !slices.Equal(got, []adapter.Family{adapter.FamilyOpenAI}) {
		t.Errorf("zhipu speaks %v, want OpenAI's family alone", got)
	}
}

// TestBailiansEntriesAreItsAccountSites: an Alibaba Cloud account settles at its own site's prices in its site's
// currency wherever its key is used, so Bailian's entries are named by the account the person picks by: a China-site
// account in yuan at Beijing, whose prices are the catalog's yuan list; a China-site account in yuan at Singapore,
// whose prices the catalog does not hold (the yuan list is Beijing's), so the line there sells nothing at a catalog
// price and its cost is unknown; and an international-site account in dollars.
func TestBailiansEntriesAreItsAccountSites(t *testing.T) {
	t.Parallel()
	want := []adapter.PriceEntry{
		{ID: "cn", Origin: "https://dashscope.aliyuncs.com", Currency: "CNY", Account: "中国站（阿里云）账号"},
		{ID: "cn-sg", Origin: "https://dashscope-intl.aliyuncs.com", Currency: "CNY", Account: "中国站（阿里云）账号 · 新加坡", Unpriced: true},
		{ID: "global", Origin: "https://dashscope-intl.aliyuncs.com", Currency: "USD", Account: "国际站（Alibaba Cloud）账号"},
	}
	typ := row(t, "bailian").(adapter.Described).Type()
	if diff := cmp.Diff(want, typ.Entries); diff != "" {
		t.Errorf("entries (-want +got):\n%s", diff)
	}
	for _, e := range want {
		if serves := typ.ServesAt(e.Origin, e.Currency); (len(serves) == 0) != e.Unpriced {
			t.Errorf("%s serves %v at its entry", e.ID, serves)
		}
	}
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
