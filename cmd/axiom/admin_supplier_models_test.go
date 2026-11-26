package main

import (
	"fmt"
	"math/big"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/suppliers"
)

// TestSupplierModelsInPiles: a supplier's names are sorted by where the catalog and the supplier's aliases put them — a
// listed model's name with its endpoint, a draft's (by name or by alias) with its family, a retired model's, a dated
// snapshot of a known name and a fine-tune, and the rest to review, with the model a relay's spelling suggests — and
// getSupplier counts each pile; a filter, the pages and their cursor, and the refusals.
func TestSupplierModelsInPiles(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	group := f.group(supplier, "1")
	c := catalogFixture(f)
	onSale := c.ready("acme/on-sale")
	if rec := c.setStatus(onSale, "listed"); rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	c.addName(onSale, "on-sale")
	drafty := c.addName(c.create("acme/drafty"), "drafty")
	c.send(http.MethodPut, "/models/"+drafty.Id+"/family", `{"family":"drafts","rev":0,"reason":"r"}`, http.StatusOK)
	old := c.ready("acme/old")
	if rec := c.setStatus(old, "listed"); rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := c.setStatus(c.send(http.MethodGet, "/models/"+old.Id, "", http.StatusOK), "retired"); rec.Code != http.StatusOK {
		t.Fatalf("retire: %d %s", rec.Code, rec.Body)
	}
	for _, name := range []string{"on-sale", "drafty", "acme/old", "on-sale-2024-05-13", "ft:gpt-4o:acme", "vendor/On-Sale", "mystery", "aliased"} {
		model := "NULL"
		if name == "on-sale" {
			model = "'" + onSale.Id + "'"
		}
		f.run(fmt.Sprintf(`INSERT INTO suppliers.supplier_models (supplier_id, supplier_group_id, upstream_name, formats, model_id)
			VALUES ('%s', '%s', '%s', '{chat}', %s)`, supplier, group, name, model))
	}
	endpoint := f.text(fmt.Sprintf(`INSERT INTO suppliers.endpoints (supplier_id, supplier_group_id, model_id, upstream_name, formats)
		VALUES ('%s', '%s', '%s', 'on-sale', '{chat}') RETURNING id::text`, supplier, group, onSale.Id))
	f.send(http.MethodPost, "/suppliers/"+supplier+"/model-mappings",
		`{"upstream_name":"aliased","group_id":"`+group+`","model_id":"`+drafty.Id+`","reason":"the relay's name for it"}`, http.StatusCreated, new(adminapi.ModelMapping))

	all := f.supplierModels(supplier, "limit=3")
	type pile struct {
		Pile, Model, Family, Endpoint, Suggested string
	}
	got := map[string]pile{}
	for _, m := range all {
		got[m.UpstreamName] = pile{string(m.Pile), deref(m.ModelSlug), deref(m.Family), deref(m.EndpointId), deref(m.SuggestedModelSlug)}
	}
	want := map[string]pile{
		"on-sale":            {"listed", "acme/on-sale", "", endpoint, ""},
		"drafty":             {"known", "acme/drafty", "drafts", "", ""},
		"aliased":            {"known", "acme/drafty", "drafts", "", ""},
		"acme/old":           {"outdated", "acme/old", "", "", ""},
		"on-sale-2024-05-13": {"outdated", "", "", "", ""},
		"ft:gpt-4o:acme":     {"outdated", "", "", "", ""},
		"vendor/On-Sale":     {"review", "", "", "", "acme/on-sale"},
		"mystery":            {"review", "", "", "", ""},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("the piles (-want +got):\n%s", diff)
	}
	if review := f.supplierModels(supplier, "pile=review&group_id="+group); len(review) != 2 || review[0].UpstreamName != "mystery" {
		t.Errorf("the review pile, by name: %+v", review)
	}
	var detail adminapi.SupplierDetail
	decode(t, f.do(http.MethodGet, "/suppliers/"+supplier, "", as(f.s)), &detail)
	if wantCounts := (adminapi.SupplierModelCounts{Review: 2, Listed: 1, Known: 2, Outdated: 3}); detail.ModelCounts == nil || *detail.ModelCounts != wantCounts {
		t.Errorf("model_counts %+v, want %+v", detail.ModelCounts, wantCounts)
	}
	expectAdmin(t, "a cursor that is none", f.do(http.MethodGet, "/suppliers/"+supplier+"/models?cursor=nope", "", as(f.s)), http.StatusBadRequest, codeInvalidRequest)
	expectAdmin(t, "a supplier that is not there", f.do(http.MethodGet, "/suppliers/0198c0a8-0000-7000-8000-00000000dead/models", "", as(f.s)), http.StatusNotFound, codeNotFound)
	f.addUser("nobody@example.com", "")
	expectAdmin(t, "no permission", f.do(http.MethodGet, "/suppliers/"+supplier+"/models", "", as(f.verified("nobody@example.com"))), http.StatusForbidden, codeForbidden)
}

// supplierModels reads every page of a supplier's names, following the cursor.
func (f supplierFixture) supplierModels(supplier, query string) []adminapi.SupplierModel {
	f.t.Helper()
	var all []adminapi.SupplierModel
	cursor := ""
	for range 10 {
		path := "/suppliers/" + supplier + "/models?" + query + cursor
		rec := f.do(http.MethodGet, path, "", as(f.s))
		expectAdmin(f.t, "listSupplierModels "+path, rec, http.StatusOK, "")
		var list adminapi.SupplierModelList
		decode(f.t, rec, &list)
		all = append(all, list.Data...)
		if list.NextCursor == nil {
			return all
		}
		cursor = "&cursor=" + *list.NextCursor
	}
	f.t.Fatal("more than 10 pages")
	return nil
}

// TestSupplierEndpointCostThroughTheAPI: an endpoint's price lines are its plan's rates at each condition, in quota and
// in the settlement currency at the unit value, with where the plan came from and the factors the plan stored for each
// line before the unit value; an endpoint of unknown cost has none, and one the supplier lacks is a 404.
func TestSupplierEndpointCostThroughTheAPI(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	supplier := f.connect().Id
	group := f.group(supplier, "1")
	model := catalogFixture(f).ready("acme/alpha").Id
	endpoint := f.text(fmt.Sprintf(`INSERT INTO suppliers.endpoints (supplier_id, supplier_group_id, model_id, upstream_name, formats)
		VALUES ('%s', '%s', '%s', 'alpha', '{chat}') RETURNING id::text`, supplier, group, model))
	path := "/suppliers/" + supplier + "/endpoints/" + endpoint + "/cost"
	var unknown adminapi.EndpointCost
	decode(t, f.do(http.MethodGet, path, "", as(f.s)), &unknown)
	if unknown.CostState != "unknown" || len(unknown.Lines) != 0 || unknown.PlanId != nil || unknown.UnitValue != nil {
		t.Errorf("an endpoint of unknown cost: %+v", unknown)
	}
	plan := f.text(fmt.Sprintf(`INSERT INTO suppliers.cost_schedules (supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint, derivation)
		VALUES ('%s', '%s', 'quota', '{"v":1,"currency":"XXX","rates":{"input":"0.75","output":"3"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":200000,"rates":{"input":"1.5"}}]}}', 'published', 'v1',
		'[{"item":"input","service_tier":"standard","factors":[{"kind":"model_ratio","value":"0.5"},{"kind":"group_ratio","value":"1.5"}]}]')
		RETURNING id::text`, supplier, endpoint))
	f.run(fmt.Sprintf(`UPDATE suppliers.endpoints SET cost_state = 'known', cost_schedule_id = '%s' WHERE id = '%s'`, plan, endpoint),
		fmt.Sprintf(`INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ('%s', 0.000002, 'test', 'confirmed')`, supplier))
	rec := f.do(http.MethodGet, path, "", as(f.s))
	expectAdmin(t, "getSupplierEndpointCost", rec, http.StatusOK, "")
	var cost adminapi.EndpointCost
	decode(t, rec, &cost)
	uv := []adminapi.CostFactor{{Kind: "unit_value", Value: "0.000002"}}
	line := func(item adminapi.MeteringItem, above *int64, native, price string) adminapi.CostLine {
		return adminapi.CostLine{Item: item, Unit: "token", Above: above, NativePrice: &native, Price: &price, Factors: uv}
	}
	above := int64(200000)
	baseInput := line("input", nil, "0.75", "0.0000015")
	baseInput.Factors = append([]adminapi.CostFactor{{Kind: "model_ratio", Value: "0.5"}, {Kind: "group_ratio", Value: "1.5"}}, uv...)
	want := adminapi.EndpointCost{
		EndpointId: endpoint, CostState: "known", Source: new(adminapi.CostSource("read")), SettlementCurrency: "USD",
		UnitValue: new("0.000002"), PlanId: &plan,
		Lines: []adminapi.CostLine{
			baseInput, line("output", nil, "3", "0.000006"),
			line("input", &above, "1.5", "0.000003"), line("output", &above, "3", "0.000006"),
		},
	}
	if diff := cmp.Diff(want, cost); diff != "" {
		t.Errorf("the cost (-want +got):\n%s", diff)
	}
	expectAdmin(t, "an endpoint the supplier lacks", f.do(http.MethodGet, "/suppliers/"+supplier+"/endpoints/0198c0a8-0000-7000-8000-00000000dead/cost", "", as(f.s)), http.StatusNotFound, codeNotFound)
	f.addUser("nobody@example.com", "")
	expectAdmin(t, "no permission", f.do(http.MethodGet, path, "", as(f.verified("nobody@example.com"))), http.StatusForbidden, codeForbidden)
}

// TestBreakEvenOf: the fewest tokens whose sale covers a request's price, at the mix of the last 24 hours, after what
// the sale charges a request itself; all input without traffic, and none when no token of the mix is sold.
func TestBreakEvenOf(t *testing.T) {
	t.Parallel()
	price := func(s string) platform.UnitPrice {
		p, err := platform.ParseUnitPrice(s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	sale := func(rates string) catalog.Schedule {
		s, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{` + rates + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	tokens := sale(`"input":"0.000001","output":"0.000004"`)
	in, out := catalog.ItemInput, catalog.ItemOutput
	for _, c := range []struct {
		name    string
		request string
		rate    int64
		sale    catalog.Schedule
		traffic suppliers.Traffic
		want    *adminapi.CostBreakEven
	}{
		// 0.01 at a mean of (3×1 + 1×4)/4 = 1.75 per million: 5714.28… tokens, so 5715.
		{
			"the mix of the day", "0.01", 1, tokens,
			suppliers.Traffic{Requests: 2, Units: map[catalog.Item]int64{in: 3000, out: 1000}},
			&adminapi.CostBreakEven{RequestPrice: "0.01", Tokens: 5715, MeanTokens24h: new(int64(2000)), Requests24h: new(int64(2))},
		},
		{
			"no traffic: all input", "0.01", 1, tokens,
			suppliers.Traffic{Units: map[catalog.Item]int64{}},
			&adminapi.CostBreakEven{RequestPrice: "0.01", Tokens: 10000, Requests24h: new(int64(0))},
		},
		{
			"into the site's currency", "0.01", 7, tokens,
			suppliers.Traffic{Units: map[catalog.Item]int64{}},
			&adminapi.CostBreakEven{RequestPrice: "0.01", Tokens: 70000, Requests24h: new(int64(0))},
		},
		{
			"a sale's own request price covers it", "0.01", 1, sale(`"input":"0.000001","request":"0.02"`),
			suppliers.Traffic{Units: map[catalog.Item]int64{}},
			&adminapi.CostBreakEven{RequestPrice: "0.01", Tokens: 0, Requests24h: new(int64(0))},
		},
		{"no token is sold", "0.01", 1, sale(`"request":"0.001"`), suppliers.Traffic{Units: map[catalog.Item]int64{}}, nil},
	} {
		got := breakEvenOf(price(c.request), big.NewRat(c.rate, 1), c.sale, c.traffic)
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.name, diff)
		}
	}
}
