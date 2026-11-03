package gateway

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
)

func TestPresetMergeRefusalClassification(t *testing.T) {
	t.Parallel()
	_, err := protocol.MergePreset(protocol.Chat, []byte(`{"model":"@preset/p"}`), []byte(`{"model":"m","system":"help"}`), "")
	for _, format := range []protocol.Format{protocol.Chat, protocol.Messages, protocol.Responses} {
		ref, expected := refusal(format, err)
		if !expected || ref.Status != http.StatusBadRequest {
			t.Fatalf("merge classified as unexpected overload: %v, %v", ref, expected)
		}
	}
}

func presetMergeRig(t *testing.T) (*rig, account) {
	t.Helper()
	r := newRig(t)
	a := r.newAccount(1e9)
	err := r.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		for slug, config := range map[string]string{
			"object-tools": `{"model":"acme/alpha","tools":{"a":1}}`,
			"scalar-tools": `{"model":"acme/alpha","tools":[1]}`,
			"system":       `{"model":"acme/alpha","system":"help"}`,
			"list":         `{"models":["acme/alpha","acme/beta"]}`,
		} {
			_, err := accounts.SavePreset(t.Context(), tx, audit.System("gateway.test"), accounts.SiteID(siteID), accounts.AccountID(a.id), accounts.Preset{Slug: slug, Name: slug, Config: jsonx.Raw(config)}, true)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.accounts.Load(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	return r, a
}

func TestPresetMergeHTTPInvalid(t *testing.T) {
	t.Parallel()
	r, a := presetMergeRig(t)
	for _, tc := range []struct{ path, body string }{
		{"messages", `{"model":"@preset/object-tools","tools":[]}`},
		{"messages", `{"model":"@preset/scalar-tools","tools":[]}`},
		{"chat/completions", `{"model":"@preset/system"}`},
	} {
		resp, body := r.do(http.MethodPost, "/v1/"+tc.path, a.key, strings.NewReader(tc.body))
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "cannot merge preset") {
			t.Fatalf("merge response: %d %s", resp.StatusCode, body)
		}
	}
	r.idle(1 << 30)
}

func TestPresetMessagesFallbackHTTPOverride(t *testing.T) {
	t.Parallel()
	r, a := presetMergeRig(t)
	_, resp, body := r.sendNamed(a, "@preset/list", `"fallbacks":[{"model":"anthropic/claude-live"}],`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fallback response: %d %s", resp.StatusCode, body)
	}
	calls := r.calls()
	call := calls[len(calls)-1]
	if call.Request.Model != "acme/alpha" || len(call.Request.Models) != 1 || call.Request.Models[0] != "anthropic/claude-live" {
		t.Fatalf("fallback summary: %+v", call.Request)
	}
	for _, entry := range call.Plan.Entries {
		if entry.Model == r.beta {
			t.Fatal("preset alternate survived the request override")
		}
	}
	r.idle(1 << 30)
}
