package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

func TestUnpricedServerToolsNeverReachAdmission(t *testing.T) {
	r := newRig(t)
	a := r.newAccount(1e9)
	for _, tc := range []struct{ path, member, name string }{
		{"messages", `"tools":[{"type":"web_fetch_20250910","max_uses":2}]`, "web_fetch"},
		{"messages", `"mcp_servers":[{"type":"url","name":"m","url":"https://example.com/mcp"}]`, "mcp_servers"},
		{"responses", `"tools":[{"type":"mcp"}],"max_tool_calls":2`, "mcp"},
		{"responses", `"tools":[{"type":"file_search"}],"max_tool_calls":2`, "file_search"},
		{"responses", `"tools":[{"type":"image_generation"}],"max_tool_calls":2`, "image_generation"},
		{"messages", `"tools":[{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}]`, "tool_search_tool_"},
		{"messages", `"tools":[{"type":"tool_search_tool_bm25_20251119","name":"tool_search_tool_bm25"}]`, "tool_search_tool_"},
		{"messages", `"tools":[{"type":"advisor_20260301","name":"advisor"}]`, "advisor_"},
		{"responses", `"tools":[{"type":"shell","environment":{"type":"container_auto"}}],"max_tool_calls":2`, "shell"},
		{"responses", `"tools":[{"type":"tool_search"}],"max_tool_calls":2`, "tool_search"},
		{"responses", `"tools":[{"type":"programmatic_tool_calling"}],"max_tool_calls":2`, "programmatic_tool_calling"},
	} {
		resp, out := r.do(http.MethodPost, "/v1/"+tc.path, a.key, strings.NewReader(`{"model":"acme/beta",`+tc.member+`}`))
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(out, tc.name) {
			t.Errorf("%s = %d %s", tc.name, resp.StatusCode, out)
		}
	}
	if got := r.count("SELECT count(*) FROM billing.reservations"); got != 0 {
		t.Fatalf("refusals created %d reservations", got)
	}
}

func TestCatalogToolDefaultsReachTheQuote(t *testing.T) {
	f := newRouteFixture(t)
	snap, err := f.cat.Current()
	if err != nil {
		t.Fatal(err)
	}
	model, _ := snap.Model(f.served)
	spec := strings.TrimSuffix(specAll, "}") + `,"max_uses":{"web_search":30,"code_execution":2}}`
	if _, err := f.cat.UpdateSpec(t.Context(), catalog.UpdateSpecRequest{
		Actor: audit.System("gateway.test"), Key: uuid.NewV7(), Reason: "test the catalog tool bounds",
		Model: f.served, Rev: model.Rev, Spec: jsonx.Raw(spec),
	}); err != nil {
		t.Fatal(err)
	}
	rate, err := platform.ParseUnitPrice("0.001")
	if err != nil {
		t.Fatal(err)
	}
	f.build([]suppliers.Routable{routable(f.served, protocol.Responses, protocol.Messages, protocol.Chat)})
	for _, tc := range []struct {
		name, request string
		format        protocol.Format
		item          catalog.Item
		want          int64
	}{
		{"default search", `{"model":"m","tools":[{"type":"web_search"}]}`, protocol.Responses, catalog.ItemWebSearch, 30},
		{"default code", `{"model":"m","tools":[{"type":"code_interpreter"}]}`, protocol.Responses, catalog.ItemCodeExecution, 2},
		{"Chat search default", `{"model":"m","web_search_options":{}}`, protocol.Chat, catalog.ItemWebSearch, 30},
		{"Messages code default", `{"model":"m","tools":[{"type":"code_execution_20260521","name":"code_execution"}]}`, protocol.Messages, catalog.ItemCodeExecution, 2},
	} {
		req, err := protocol.Inspect(tc.format, []byte(tc.request))
		if err != nil {
			t.Fatal(err)
		}
		in := call(f.served)
		in.Req = &req
		plan, _, err := f.router.Plan(in)
		if err != nil {
			t.Fatal(err)
		}
		e := plan.Entries[0]
		if got := e.Limits.MaxUses[tc.item]; got != tc.want {
			t.Fatalf("%s: default=%d", tc.name, got)
		}
		if _, err := billing.Quote(routing.DemandOf(&req), []billing.Quotable{{Price: e.Price.Schedule.Fill(map[catalog.Item]platform.UnitPrice{catalog.ItemWebSearch: rate, catalog.ItemCodeExecution: rate}), Model: e.Limits, MaxOutput: e.MaxOutput}}, billing.Segments{}, nil); err != nil {
			t.Fatalf("%s: Quote: %v", tc.name, err)
		}
	}
}

func TestAnUndeclaredTierStopsTheNextRoute(t *testing.T) {
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(_ string, e *routing.Endpoint) {
			e.ServiceTiers = 1 << catalog.ServiceTierStandard
			e.Target = protocol.Target{Model: "up"}
		}
	})
	up := withExecutor(r, &flakyLedger{})
	a := r.funded(1e12)
	sample, _ := sample(t, protocol.Messages)
	sample[evStart] = strings.Replace(sample[evStart], `"usage": {`, `"usage": {"service_tier":"priority",`, 1)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 1, 10, false)}
	}
	resp, body := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(stream("acme/beta")))
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "billing_pending") {
		t.Fatalf("first response: %d %s", resp.StatusCode, body)
	}
	if n := r.count(`SELECT count(*) FROM billing.reservations WHERE charged>0 AND estimated`); n != 1 {
		t.Fatalf("got %d fallback charges", n)
	}
	deps := RouteDeps{
		Catalog: r.catalog, Router: r.router, Models: r.models, Quality: quality.New(nil), Logger: slog.New(slog.DiscardHandler),
		Endpoints: func(context.Context) ([]suppliers.Routable, error) {
			return []suppliers.Routable{routable(r.beta, protocol.Messages)}, nil
		},
		Prices: func(context.Context) (map[catalog.ModelID]billing.Sold, error) { return r.prices(), nil },
		UnknownItems: func(ctx context.Context) (items []billing.UnknownItem, err error) {
			err = r.db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error { items, err = billing.UnknownItems(ctx, tx); return err })
			return items, err
		},
	}
	snap, list, count, err := deps.Build(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	deps.Publish(snap, list, count)
	resp, body = r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(stream("acme/beta")))
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "unknown_item") {
		t.Fatalf("next response: %d %s", resp.StatusCode, body)
	}
	if n := r.count(`SELECT count(*) FROM billing.unknown_items WHERE field='service_tier.priority' AND cleared_at IS NULL`); n != 1 {
		t.Fatalf("got %d missing tiers", n)
	}
	r.ledgerBalanced()
}
