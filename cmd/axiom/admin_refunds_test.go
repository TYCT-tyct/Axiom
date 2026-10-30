package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"
	"uuid"

	"axiom/internal/payments"
	"axiom/internal/platform/adminapi"
)

// seedMerchant makes one enabled merchant account whose orders an operator can list. It needs no plugin at rest: a list
// and a decision on an offline refund never reach a channel.
func (f adminFixture) seedMerchant() uuid.UUID {
	f.t.Helper()
	id := uuid.NewV7()
	f.run(fmt.Sprintf(`INSERT INTO payments.merchants
		(id, site_id, kind, name, enabled, currency, ttl_seconds, refund_enabled, user_refund, sort, rev)
		VALUES ('%s', '%s', 'easypay', 'Alipay', true, 'USD', 1800, true, true, 1, 1)`, id, f.siteUUID()))
	return id
}

func (f adminFixture) siteUUID() string {
	f.t.Helper()
	return f.text("SELECT site_id::text FROM accounts.users WHERE email = '" + ownerEmail + "'")
}

// seedOrder writes one order of a user's account at a known moment, paid or still waiting, so the list's state and date
// filters have something deterministic to find.
func (f adminFixture) seedOrder(merchant uuid.UUID, email, stateName string, at time.Time) uuid.UUID {
	f.t.Helper()
	id, account, user := uuid.NewV7(), f.accountUUID(email), f.userUUID(email)
	paid := stateName == "paid"
	settled := "NULL, NULL, NULL, NULL"
	if paid {
		settled = fmt.Sprintf("'%s', 1000, 30, 10000000000", at.Format(time.RFC3339Nano))
	}
	f.run(fmt.Sprintf(`INSERT INTO payments.orders
		(id, site_id, account_id, user_id, merchant_id, method, state, idem_key, fingerprint, credits, site_currency,
		 pay_currency, total, fee, fee_ppm, fee_fixed, ref_num, ref_den, applied_num, applied_den, trade_no, expires_at,
		 client_ip, created_at, paid_at, paid_total, paid_fee, credited)
		VALUES ('%s', '%s', '%s', '%s', '%s', 'alipay', '%s', 'order-%s', decode(repeat('00', 32), 'hex'),
		 10000000000, 'USD', 'USD', 1000, 30, 0, 0, 1, 1, 1, 1, 'trade-%s', now() + interval '1 day', '192.0.2.1',
		 '%s', %s)`,
		id, f.siteUUID(), account, user, merchant, stateName, id, id, at.Format(time.RFC3339Nano), settled))
	return id
}

func (f adminFixture) accountUUID(email string) string {
	f.t.Helper()
	return f.text("SELECT account_id::text FROM accounts.users WHERE email = '" + email + "'")
}

func (f adminFixture) userUUID(email string) string {
	f.t.Helper()
	return f.text("SELECT id::text FROM accounts.users WHERE email = '" + email + "'")
}

// seedRefund freezes an offline refund, which needs no billing batch to list or approve: approval records the second
// person and waits for a manual conclusion, so the test never calls a channel.
func (f adminFixture) seedRefund(order uuid.UUID, requester string) uuid.UUID {
	f.t.Helper()
	id := uuid.NewV7()
	f.run(fmt.Sprintf(`INSERT INTO payments.refunds
		(id, order_id, site_id, account_id, state, via, credits, requested_credits, amount, with_fee, refund_key, reason,
		 requested_by, idem_key, created_at)
		VALUES ('%s', '%s', (SELECT site_id FROM payments.orders WHERE id = '%s'),
		 (SELECT account_id FROM payments.orders WHERE id = '%s'), 'frozen', 'offline', 5000000000, 5000000000, 500, false,
		 true, 'The credits were not used.', '%s', 'refund-%s', now())`,
		id, order, order, order, f.userUUID(requester), id))
	return id
}

func (f adminFixture) orders(t *testing.T, s session, query url.Values) adminapi.PaymentOrderList {
	t.Helper()
	query.Set("site_id", f.siteID())
	rec := f.do(http.MethodGet, "/payments/orders?"+query.Encode(), "", as(s))
	expectAdmin(t, "listPaymentOrders", rec, http.StatusOK, "")
	var list adminapi.PaymentOrderList
	decode(t, rec, &list)
	return list
}

// orderListFixture is one site with two orders by the owner and one by another user, all of one merchant, at fixed
// moments so the date filters and the cursor are independent of how fast the test runs.
type orderListFixture struct {
	f                adminFixture
	s                session
	merchant         uuid.UUID
	base             time.Time
	old, mid, newest uuid.UUID
}

func newOrderListFixture(t *testing.T) orderListFixture {
	t.Helper()
	f := newAdminFixture(t)
	f.addUser("other@example.com", "")
	merchant := f.seedMerchant()
	base := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	return orderListFixture{
		f: f, s: f.owner(), merchant: merchant, base: base,
		old:    f.seedOrder(merchant, ownerEmail, "paid", base),
		mid:    f.seedOrder(merchant, ownerEmail, "pending", base.Add(time.Hour)),
		newest: f.seedOrder(merchant, "other@example.com", "paid", base.Add(2*time.Hour)),
	}
}

func TestAdminListsPaymentOrders(t *testing.T) {
	o := newOrderListFixture(t)
	list := o.f.orders(t, o.s, url.Values{})
	if list.Data == nil || len(list.Data) != 3 {
		t.Fatalf("orders: %+v", list.Data)
	}
	assertOrderShown(t, list.Data[0], o.newest)
}

func assertOrderShown(t *testing.T, order adminapi.PaymentOrder, id uuid.UUID) {
	t.Helper()
	if valueOf(order.Id) != payments.OrderID(id).TypeID() {
		t.Errorf("the newest order is %v, want %s", order.Id, payments.OrderID(id).TypeID())
	}
	if got := valueOf(order.Credits); got.Amount != "10000000000" {
		t.Errorf("credits: %+v", order.Credits)
	}
	if got := valueOf(order.Total); got.Amount != "1000" || got.Currency != "USD" {
		t.Errorf("total: %+v", order.Total)
	}
	if valueOf(order.PaidTotal).Amount != "1000" {
		t.Errorf("paid total: %+v", order.PaidTotal)
	}
	if valueOf(order.MerchantName) != "Alipay" {
		t.Errorf("merchant: %v", order.MerchantName)
	}
	if order.Refunds == nil || len(*order.Refunds) != 0 {
		t.Errorf("an order without refunds has %v", order.Refunds)
	}
}

// valueOf reads an optional field so a test can compare it with what a page must show; nil reads as the zero value.
func valueOf[T any](p *T) (v T) {
	if p != nil {
		v = *p
	}
	return v
}

func TestAdminFiltersPaymentOrders(t *testing.T) {
	o := newOrderListFixture(t)
	at := func(offset time.Duration) string { return o.base.Add(offset).Format(time.RFC3339) }
	cases := map[string]struct {
		query url.Values
		want  []string
	}{
		"account": {
			url.Values{"account_id": {o.f.accountOf(ownerEmail)}},
			[]string{payments.OrderID(o.mid).TypeID(), payments.OrderID(o.old).TypeID()},
		},
		"user": {
			url.Values{"user_id": {o.f.userOf(ownerEmail)}},
			[]string{payments.OrderID(o.mid).TypeID(), payments.OrderID(o.old).TypeID()},
		},
		"state": {url.Values{"state": {"pending"}}, []string{payments.OrderID(o.mid).TypeID()}},
		"since": {url.Values{"since": {at(90 * time.Minute)}}, []string{payments.OrderID(o.newest).TypeID()}},
		"until": {
			url.Values{"until": {at(90 * time.Minute)}},
			[]string{payments.OrderID(o.mid).TypeID(), payments.OrderID(o.old).TypeID()},
		},
		"merchant": {
			url.Values{"merchant_id": {payments.MerchantID(o.merchant).TypeID()}},
			[]string{payments.OrderID(o.newest).TypeID(), payments.OrderID(o.mid).TypeID(), payments.OrderID(o.old).TypeID()},
		},
		"another merchant": {url.Values{"merchant_id": {payments.MerchantID(uuid.NewV7()).TypeID()}}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := orderIDs(mustData(t, o.f.orders(t, o.s, tc.query))); !slices.Equal(got, tc.want) {
				t.Fatalf("%s: orders %v, want %v", name, got, tc.want)
			}
		})
	}
}

func TestAdminPagesPaymentOrders(t *testing.T) {
	o := newOrderListFixture(t)
	firstPage := o.f.orders(t, o.s, url.Values{"limit": {"2"}})
	if firstPage.NextCursor == nil || len(firstPage.Data) != 2 {
		t.Fatalf("the first page: %+v, next %v", firstPage.Data, firstPage.NextCursor)
	}
	secondPage := o.f.orders(t, o.s, url.Values{"limit": {"2"}, "cursor": {*firstPage.NextCursor}})
	if len(secondPage.Data) != 1 || *secondPage.Data[0].Id != payments.OrderID(o.old).TypeID() || secondPage.NextCursor != nil {
		t.Fatalf("the second page: %+v, next %v", secondPage.Data, secondPage.NextCursor)
	}
}

func TestAdminRejectsInvalidOrderQueries(t *testing.T) {
	o := newOrderListFixture(t)
	for name, query := range map[string]string{
		"until before since": "since=2026-10-02T00:00:00Z&until=2026-10-01T00:00:00Z",
		"a bad cursor":       "cursor=nonsense",
		"a bad account":      "account_id=nonsense",
	} {
		t.Run(name, func(t *testing.T) {
			rec := o.f.do(http.MethodGet, "/payments/orders?site_id="+o.f.siteID()+"&"+query, "", as(o.s))
			expectAdmin(t, name, rec, http.StatusBadRequest, "")
		})
	}
}

func mustData(t *testing.T, list adminapi.PaymentOrderList) []adminapi.PaymentOrder {
	t.Helper()
	if list.Data == nil {
		t.Fatal("the list has no data")
	}
	return list.Data
}

// orderIDs is the IDs of a page in the order it lists them, for comparing a filtered page with what it must hold.
func orderIDs(orders []adminapi.PaymentOrder) []string {
	ids := make([]string, len(orders))
	for i, order := range orders {
		if order.Id != nil {
			ids[i] = *order.Id
		}
	}
	return ids
}

func TestAdminApprovesRefund(t *testing.T) {
	f, s, refund := newFrozenRefund(t)
	assertRefundQueue(t, f, s, refund)
	assertRefundApproval(t, f, s, refund)
}

// newFrozenRefund is an offline refund frozen for review, requested by someone other than the operator on duty.
func newFrozenRefund(t *testing.T) (adminFixture, session, uuid.UUID) {
	t.Helper()
	f := newAdminFixture(t)
	f.addUser("other@example.com", "")
	merchant := f.seedMerchant()
	order := f.seedOrder(merchant, ownerEmail, "paid", time.Now().UTC())
	return f, f.owner(), f.seedRefund(order, "other@example.com")
}

func assertRefundQueue(t *testing.T, f adminFixture, s session, refund uuid.UUID) {
	t.Helper()
	rec := f.do(http.MethodGet, "/payments/refunds?site_id="+f.siteID()+"&state=frozen", "", as(s))
	expectAdmin(t, "listPaymentRefunds", rec, http.StatusOK, "")
	var list adminapi.PaymentRefundList
	decode(t, rec, &list)
	if list.Data == nil || len(list.Data) != 1 {
		t.Fatalf("frozen refunds: %+v", list.Data)
	}
	assertFrozenRefundShown(t, list.Data[0], f, refund)
}

func assertFrozenRefundShown(t *testing.T, shown adminapi.PaymentRefund, f adminFixture, refund uuid.UUID) {
	t.Helper()
	if valueOf(shown.Id) != payments.RefundID(refund).TypeID() {
		t.Errorf("the refund is %v, want %s", shown.Id, payments.RefundID(refund).TypeID())
	}
	if valueOf(shown.AccountId) != f.accountOf(ownerEmail) {
		t.Errorf("the refund's account: %v", shown.AccountId)
	}
	if got := valueOf(shown.Credits); got.Amount != "5000000000" {
		t.Errorf("credits: %+v", shown.Credits)
	}
	if valueOf(shown.Amount).Amount != "500" {
		t.Errorf("amount: %+v", shown.Amount)
	}
	if valueOf(shown.Reason) != "The credits were not used." {
		t.Errorf("reason: %v", shown.Reason)
	}
}

func assertRefundApproval(t *testing.T, f adminFixture, s session, refund uuid.UUID) {
	t.Helper()
	path := "/payments/refunds/" + payments.RefundID(refund).TypeID() + "/approve"
	expectAdmin(t, "an empty reason", f.change(s, http.MethodPost, path, `{"reason":"  "}`, newKey()),
		http.StatusUnprocessableEntity, "")
	key := newKey()
	rec := f.change(s, http.MethodPost, path, `{"reason":"checked the receipt"}`, key)
	expectAdmin(t, "approvePaymentRefund", rec, http.StatusOK, "")
	if got := rec.Header().Get("Idempotency-Replayed"); got != "" {
		t.Errorf("the first approval says it was replayed: %q", got)
	}
	var approved adminapi.PaymentRefund
	decode(t, rec, &approved)
	if approved.ApprovedBy == nil || *approved.ApprovedBy != f.userOf(ownerEmail) {
		t.Errorf("approved by %v, want %s", approved.ApprovedBy, f.userOf(ownerEmail))
	}
	if approved.State == nil || *approved.State != "frozen" {
		t.Errorf("an offline refund's state: %v", approved.State)
	}
	f.expectAudit("refund.approve", map[string]string{"reason": "checked the receipt"})

	rec = f.change(s, http.MethodPost, path, `{"reason":"checked the receipt"}`, key)
	expectAdmin(t, "the replay", rec, http.StatusOK, "")
	if got := rec.Header().Get("Idempotency-Replayed"); got != "true" {
		t.Errorf("the replay header is %q, want true", got)
	}
	if n := f.auditCount("refund.approve"); n != "1" {
		t.Errorf("%s approvals were audited, want 1", n)
	}
	expectAdmin(t, "a new key on a decided refund", f.change(s, http.MethodPost, path,
		`{"reason":"looked again"}`, newKey()), http.StatusOK, "")
	if n := f.auditCount("refund.approve"); n != "1" {
		t.Errorf("a second approval was audited: %s", n)
	}
}

func TestAdminRefusesRefundPastReviewOrByRequester(t *testing.T) {
	f := newAdminFixture(t)
	f.addUser("other@example.com", "")
	s := f.owner()
	merchant := f.seedMerchant()
	order := f.seedOrder(merchant, ownerEmail, "paid", time.Now().UTC())
	refund := f.seedRefund(order, "other@example.com")
	path := "/payments/refunds/" + payments.RefundID(refund).TypeID()
	expectAdmin(t, "approvePaymentRefund", f.change(s, http.MethodPost, path+"/approve",
		`{"reason":"checked the receipt"}`, newKey()), http.StatusOK, "")
	expectAdmin(t, "rejecting an approved refund", f.change(s, http.MethodPost, path+"/reject",
		`{"reason":"too late"}`, newKey()), http.StatusConflict, codeConflict)

	ownOrder := f.seedOrder(merchant, ownerEmail, "paid", time.Now().UTC().Add(time.Second))
	ownRefund := f.seedRefund(ownOrder, ownerEmail)
	expectAdmin(t, "approving one's own refund", f.change(s, http.MethodPost,
		"/payments/refunds/"+payments.RefundID(ownRefund).TypeID()+"/approve", `{"reason":"mine"}`, newKey()),
		http.StatusForbidden, codeForbidden)
}

// One key stands for one decision: two refunds cannot both be approved because they raced past a lookup.
func TestRefundDecisionKeyCoversOneRefund(t *testing.T) {
	f := newAdminFixture(t)
	f.addUser("other@example.com", "")
	s := f.owner()
	merchant := f.seedMerchant()
	at := time.Now().UTC()
	firstOrder := f.seedOrder(merchant, "other@example.com", "paid", at)
	secondOrder := f.seedOrder(merchant, "other@example.com", "paid", at.Add(time.Second))
	first := f.seedRefund(firstOrder, "other@example.com")
	second := f.seedRefund(secondOrder, "other@example.com")

	key := newKey()
	responses := make(chan *httptest.ResponseRecorder, 2)
	for _, id := range []uuid.UUID{first, second} {
		go func() {
			path := "/payments/refunds/" + payments.RefundID(id).TypeID() + "/approve"
			responses <- f.change(s, http.MethodPost, path, `{"reason":"checked the receipt"}`, key)
		}()
	}
	a, b := <-responses, <-responses
	won, lost := a, b
	if a.Code != http.StatusOK {
		won, lost = b, a
	}
	expectAdmin(t, "the first refund under the key", won, http.StatusOK, "")
	expectAdmin(t, "the other refund under the same key", lost, http.StatusConflict, codeIdempotencyReuse)
	if n := f.text("SELECT count(*)::text FROM payments.refunds WHERE approved_by IS NOT NULL"); n != "1" {
		t.Errorf("%s refunds were approved under one key, want 1", n)
	}
	if n := f.text("SELECT count(*)::text FROM payments.refunds WHERE state = 'frozen' AND approved_by IS NULL"); n != "1" {
		t.Errorf("%s refunds were left frozen without an approver, want 1", n)
	}
	if n := f.auditCount("refund.approve"); n != "1" {
		t.Errorf("%s approvals were audited, want 1", n)
	}
}

// The local decision, its audit and its idempotency answer commit together; if the answer cannot be stored, no approval
// is left behind and the same key can be retried after the fault is gone.
func TestRefundDecisionAndItsKeyCommitTogether(t *testing.T) {
	f := newAdminFixture(t)
	f.addUser("other@example.com", "")
	s := f.owner()
	merchant := f.seedMerchant()
	order := f.seedOrder(merchant, "other@example.com", "paid", time.Now().UTC())
	refund := f.seedRefund(order, "other@example.com")
	path := "/payments/refunds/" + payments.RefundID(refund).TypeID() + "/approve"
	key := newKey()

	f.run(`CREATE FUNCTION platform.reject_idempotency_finish() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected idempotency failure'; END $$`)
	f.run(`CREATE TRIGGER reject_idempotency_finish BEFORE UPDATE ON platform.idempotency
		FOR EACH ROW WHEN (OLD.response IS NULL AND NEW.response IS NOT NULL)
		EXECUTE FUNCTION platform.reject_idempotency_finish()`)
	expectAdmin(t, "an approval whose key cannot be saved", f.change(s, http.MethodPost, path, `{"reason":"checked the receipt"}`, key),
		http.StatusInternalServerError, codeInternal)
	if got := f.text("SELECT state || ':' || (approved_by IS NULL)::text FROM payments.refunds WHERE id = '" + refund.String() + "'"); got != "frozen:true" {
		t.Errorf("a failed key write left the refund as %q, want frozen:true", got)
	}
	if n := f.auditCount("refund.approve"); n != "0" {
		t.Errorf("a failed key write audited %s approvals, want none", n)
	}

	f.run(`DROP TRIGGER reject_idempotency_finish ON platform.idempotency`)
	rec := f.change(s, http.MethodPost, path, `{"reason":"checked the receipt"}`, key)
	expectAdmin(t, "the same key after the fault", rec, http.StatusOK, "")
	var approved adminapi.PaymentRefund
	decode(t, rec, &approved)
	if approved.ApprovedBy == nil || *approved.ApprovedBy != f.userOf(ownerEmail) {
		t.Errorf("approved by %v, want %s", approved.ApprovedBy, f.userOf(ownerEmail))
	}
	if n := f.auditCount("refund.approve"); n != "1" {
		t.Errorf("%s approvals were audited after the retry, want 1", n)
	}
}
