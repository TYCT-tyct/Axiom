package main

import (
	"fmt"
	"net/http"
	"testing"
	"uuid"

	"axiom/internal/payments"
	"axiom/internal/platform/adminapi"
)

func TestAdminPaymentDiscrepancyInvestigation(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	merchant, id := uuid.NewV7(), uuid.NewV7()
	f.run(fmt.Sprintf(`INSERT INTO payments.merchants(id,site_id,kind,name,currency,enabled,ttl_seconds,refund_enabled,user_refund,sort,rev) SELECT '%s',id,'fake','Gateway','USD',false,1800,false,false,0,1 FROM accounts.sites WHERE parent_site_id IS NULL`, merchant))
	f.run(fmt.Sprintf(`INSERT INTO payments.discrepancies(id,site_id,merchant_id,kind,dedupe,detail) SELECT '%s',site_id,id,'usage','day','{"expected":"1","actual":"2"}' FROM payments.merchants WHERE id='%s'`, id, merchant))
	path := "/payments/discrepancies"
	rec := f.do(http.MethodGet, path+"?state=open", "", as(s))
	expectAdmin(t, "listPaymentDiscrepancies", rec, http.StatusOK, "")
	var list adminapi.PaymentDiscrepancyList
	decode(t, rec, &list)
	if list.Data == nil || len(*list.Data) != 1 {
		t.Fatalf("list lost record: %s", rec.Body)
	}
	rec = f.do(http.MethodGet, path+"/"+id.String(), "", as(s))
	expectAdmin(t, "getPaymentDiscrepancy", rec, http.StatusOK, "")
	key := newKey()
	body := `{"reason":"checked gateway evidence; funds unchanged"}`
	rec = f.change(s, http.MethodPost, path+"/"+id.String()+"/investigate", body, key)
	expectAdmin(t, "investigatePaymentDiscrepancy", rec, http.StatusCreated, "")
	var d adminapi.PaymentDiscrepancy
	decode(t, rec, &d)
	assertPaymentInvestigator(t, d)
	if d.State == nil || string(*d.State) != "resolved" || d.Note == nil || *d.Note != "checked gateway evidence; funds unchanged" {
		t.Fatalf("missing investigation: %s", rec.Body)
	}
	rec = f.change(s, http.MethodPost, path+"/"+id.String()+"/investigate", body, key)
	expectAdmin(t, "replay investigation", rec, http.StatusCreated, "")
	if rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("investigation did not replay")
	}
	expectAdmin(t, "already investigated", f.change(s, http.MethodPost, path+"/"+id.String()+"/investigate", `{"reason":"replace the first note"}`, newKey()), http.StatusConflict, codeConflict)
	f.expectAudit("discrepancy.investigate", map[string]string{"reason": "checked gateway evidence; funds unchanged"})
	if got := f.text("SELECT count(*)::text FROM billing.transactions"); got != "0" {
		t.Fatal("investigation moved funds", got)
	}
	expectAdmin(t, "missing discrepancy", f.do(http.MethodGet, path+"/"+uuid.NewV7().String(), "", as(s)), http.StatusNotFound, codeNotFound)
	rec = f.do(http.MethodGet, path+"?state=open", "", as(s))
	decode(t, rec, &list)
	if list.Data == nil || len(*list.Data) != 0 {
		t.Fatal("investigated record remained open")
	}
}

func assertPaymentInvestigator(t *testing.T, d adminapi.PaymentDiscrepancy) {
	t.Helper()
	if d.InvestigatedBy == nil || d.InvestigatedAt == nil {
		t.Fatal("missing investigation actor or time")
	}
}

func TestPaymentDiscrepancyResponseIdentifiers(t *testing.T) {
	var id uuid.UUID
	body, err := paymentDiscrepancyBody(payments.Discrepancy{Site: id, Merchant: id, Account: &id, Order: &id})
	if err != nil {
		t.Fatal(err)
	}
	for field, pair := range map[string][2]string{
		"site":     {*body.SiteId, "site_00000000000000000000000000"},
		"merchant": {*body.MerchantId, "mch_00000000000000000000000000"},
		"order":    {*body.OrderId, "ord_00000000000000000000000000"},
		"account":  {*body.AccountId, "acct_00000000000000000000000000"},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("%s reference=%s, want %s", field, pair[0], pair[1])
		}
	}
}
