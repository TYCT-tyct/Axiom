package main

import (
	"net/http"
	"strings"
	"testing"

	"axiom/internal/platform/adminapi"
)

const initialExchange = `{"base":"USD","quote":"CNY","numerator":"71","denominator":"10","expected_version":0,"kind":"initial","reason":"verified direction"}`

func checkExchangeLists(t *testing.T, f pricingFixture) {
	t.Helper()
	for path, want := range map[string]int{
		"/pricing/exchange-rates":                          1,
		"/pricing/exchange-rates/USD/CNY":                  2,
		"/pricing/exchange-rates/CNY/USD?before_version=2": 1,
	} {
		rec := f.do(http.MethodGet, path, "", as(f.s))
		var list adminapi.ExchangeRateList
		decode(t, rec, &list)
		if rec.Code != http.StatusOK || len(list.Data) != want {
			t.Errorf("%s: %d %+v", path, rec.Code, list)
		}
	}
}

func TestAdminExchangeHistoryCorrectionAndReplay(t *testing.T) {
	f := newPricingFixture(t)
	key := newKey()
	rec := f.change(f.s, http.MethodPost, "/pricing/exchange-rates", initialExchange, key)
	expectAdmin(t, "setExchangeRate", rec, http.StatusCreated, "")
	var first adminapi.ExchangeRate
	decode(t, rec, &first)
	if first.Base != "CNY" || first.Quote != "USD" || first.Numerator != "10" || first.Denominator != "71" || first.Version != 1 {
		t.Fatalf("normalized: %+v", first)
	}
	correction := strings.NewReplacer(`"71"`, `"14"`, `"10"`, `"100"`, `"initial"`, `"correction"`, `"expected_version":0`, `"expected_version":1`).Replace(initialExchange)
	rec = f.change(f.s, http.MethodPost, "/pricing/exchange-rates", correction, newKey())
	expectAdmin(t, "setExchangeRate", rec, http.StatusCreated, "")
	rec = f.change(f.s, http.MethodPost, "/pricing/exchange-rates", initialExchange, key)
	expectAdmin(t, "setExchangeRate", rec, http.StatusCreated, "")
	var replay adminapi.ExchangeRate
	decode(t, rec, &replay)
	if replay.Id != first.Id || replay.Version != 1 || rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("lost response replay: %+v", replay)
	}
	rec = f.change(f.s, http.MethodPost, "/pricing/exchange-rates", correction, newKey())
	expectAdmin(t, "setExchangeRate", rec, http.StatusConflict, codeStaleRevision)
	checkExchangeLists(t, f)
	f.expectAudit("exchange_rate.initial", map[string]string{"reason": "verified direction", "target_id": first.Id, "detail->>'version'": "1"})
}

func TestAdminExchangeInvalidFields(t *testing.T) {
	f := newPricingFixture(t)
	for _, tc := range []struct{ old, new, pointer string }{
		{`"USD"`, `"XXX"`, "/base"},
		{`"CNY"`, `"USD"`, "/quote"},
		{`"71"`, `"0"`, "/numerator"},
		{`"10"`, `"-1"`, "/denominator"},
		{`"initial"`, `"unknown"`, "/kind"},
		{`"verified direction"`, `" "`, "/reason"},
	} {
		rec := f.change(f.s, http.MethodPost, "/pricing/exchange-rates", strings.Replace(initialExchange, tc.old, tc.new, 1), newKey())
		expectAdmin(t, "setExchangeRate", rec, http.StatusUnprocessableEntity, codeUnprocessable)
		fields := errorDetailOf(t, rec).Fields
		if fields == nil || len(*fields) == 0 || (*fields)[0].Pointer != tc.pointer {
			t.Errorf("field: %+v want %s", fields, tc.pointer)
		}
	}
}
