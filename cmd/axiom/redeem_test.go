package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/payments"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

// TestAdminRedeemCodes: a batch answers its codes once, the list and later reads carry only a hint, a disable sticks,
// and a replay of the key answers the same batch instead of making another.
//
//nolint:cyclop,gocognit // the generation, the replay and the disable are one admin journey.
func TestAdminRedeemCodes(t *testing.T) {
	f := newAdminFixture(t)
	s := f.owner()
	site := f.siteID()
	expires := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"site_id":"` + site + `","amount":{"amount":"2500000000","currency":"USD"},"count":2,` +
		`"note":"spring","expires_at":"` + expires + `","reason":"a spring promotion"}`
	key := newKey()
	rec := f.change(s, http.MethodPost, "/payments/redeem-codes", body, key)
	expectAdmin(t, "create redeem codes", rec, http.StatusCreated, "")
	var made adminapi.RedeemCodeList
	decode(t, rec, &made)
	if len(made.Data) != 2 {
		t.Fatalf("%d codes made, want 2: %s", len(made.Data), rec.Body)
	}
	for _, c := range made.Data {
		if c.Code == nil || c.State != adminapi.RedeemCodeStateEnabled ||
			c.Amount.Amount != "2500000000" || c.Amount.Currency != "USD" || c.Note != "spring" || c.ExpiresAt == nil {
			t.Fatalf("a made code: %+v", c)
		}
		if len([]rune(*c.Code)) != 23 || !strings.HasSuffix(*c.Code, c.Hint) {
			t.Fatalf("the code %q and its hint %q", *c.Code, c.Hint)
		}
	}
	replay := f.change(s, http.MethodPost, "/payments/redeem-codes", body, key)
	expectAdmin(t, "replay the batch", replay, http.StatusCreated, "")
	if replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("the replay header: %v", replay.Header())
	}
	var again adminapi.RedeemCodeList
	decode(t, replay, &again)
	if len(again.Data) != 2 || again.Data[0].Id != made.Data[0].Id || again.Data[0].Hint != made.Data[0].Hint {
		t.Fatalf("the replay is not the first batch: %s", replay.Body)
	}
	for _, c := range again.Data {
		if c.Code != nil {
			t.Fatalf("the replay shows a code that was already shown: %+v", c)
		}
	}
	// The claim keeps the answer for a replay, so it must not keep the codes either.
	if n := f.text("SELECT count(*) FROM platform.idempotency WHERE response::text LIKE '%" + *made.Data[0].Code + "%'"); n != "0" {
		t.Fatalf("%s idempotency responses hold the code", n)
	}

	rec = f.do(http.MethodGet, "/payments/redeem-codes?site_id="+site, "", as(s))
	expectAdmin(t, "list redeem codes", rec, http.StatusOK, "")
	var list adminapi.RedeemCodeList
	decode(t, rec, &list)
	if len(list.Data) != 2 || list.NextCursor != nil {
		t.Fatalf("the list: %s", rec.Body)
	}
	for _, c := range list.Data {
		if c.Code != nil {
			t.Fatalf("the list shows a code: %+v", c)
		}
	}
	first := list.Data[0].Id
	rec = f.change(s, http.MethodPost, "/payments/redeem-codes/"+first+"/disable", `{"reason":"a mistake"}`, newKey())
	expectAdmin(t, "disable a code", rec, http.StatusOK, "")
	var disabled adminapi.RedeemCode
	decode(t, rec, &disabled)
	if disabled.State != adminapi.RedeemCodeStateDisabled {
		t.Fatalf("the disabled code: %+v", disabled)
	}
	rec = f.do(http.MethodGet, "/payments/redeem-codes?site_id="+site+"&state=disabled", "", as(s))
	expectAdmin(t, "list disabled codes", rec, http.StatusOK, "")
	decode(t, rec, &list)
	if len(list.Data) != 1 || list.Data[0].Id != first {
		t.Fatalf("the disabled codes: %s", rec.Body)
	}
	if n := f.auditCount("redeem_code.issue"); n != "1" {
		t.Errorf("%s issue audit entries, want 1", n)
	}
	if n := f.auditCount("redeem_code.disable"); n != "1" {
		t.Errorf("%s disable audit entries, want 1", n)
	}
}

// TestConsoleRedeemCode: a code credits its amount once, the history shows the credit without the code, a replay with
// the key answers the same credit, another key on the code is a conflict, and the account's limit answers 429.
//
//nolint:cyclop,gocognit // the credit, its replay, the read-back, the refusals and the limit are one journey.
func TestConsoleRedeemCode(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	owner := f.text("SELECT id::text FROM accounts.users WHERE email = '" + ownerEmail + "'")
	createdBy, err := uuid.Parse(owner)
	if err != nil {
		t.Fatal(err)
	}
	var codes []payments.RedeemCode
	if err := f.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		var err error
		codes, err = payments.IssueCodes(f.t.Context(), tx, payments.Issue{
			Site: uuid.UUID(f.siteID()), Amount: 2_500_000_000, Count: 4, Note: "seed", Actor: audit.System("test"),
			CreatedBy: createdBy, Reason: "seed a code",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"code":"` + codes[0].Code + `"}`

	rec := f.do(http.MethodPost, "/redeem-codes", body, as(s), idempotent("first"))
	expect(t, "redeem a code", rec, http.StatusCreated, "")
	var got consoleapi.RedeemCodeRedemption
	decode(t, rec, &got)
	if got.Id == "" || got.Amount.Amount != "2500000000" || got.Amount.Currency != "USD" || got.CodeHint != codes[0].Hint {
		t.Fatalf("the redemption: %s", rec.Body)
	}
	replay := f.do(http.MethodPost, "/redeem-codes", body, as(s), idempotent("first"))
	expect(t, "replay the redemption", replay, http.StatusOK, "")
	var again consoleapi.RedeemCodeRedemption
	decode(t, replay, &again)
	if again.Id != got.Id || again.RedeemedAt != got.RedeemedAt {
		t.Fatalf("the replay: %s", replay.Body)
	}
	rec = f.do(http.MethodPost, "/redeem-codes", body, as(s), idempotent("second"))
	expect(t, "the code with another key", rec, http.StatusConflict, consoleapi.ErrorCodeRedeemCodeUsed)

	rec = f.do(http.MethodGet, "/redeem-codes", "", as(s))
	expect(t, "the history", rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), strings.ReplaceAll(codes[0].Code, "-", "")) {
		t.Fatalf("the history holds the code: %s", rec.Body)
	}
	var history consoleapi.RedeemCodeHistory
	decode(t, rec, &history)
	if len(history.Data) != 1 || history.Data[0].Id != got.Id || history.NextCursor != nil {
		t.Fatalf("the history: %s", rec.Body)
	}

	// The credit reads back through the balance, the statement and its export: a source or kind the reader does not know
	// would refuse the whole page, not just hide the row.
	balance := f.balance(s)
	if balance.Batches == nil || len(*balance.Batches) != 1 || (*balance.Batches)[0].Source != consoleapi.CreditBatchRedeem {
		t.Fatalf("the balance after the redemption: %+v", balance)
	}
	statement := f.statementRows(s, "?limit=50")
	if len(statement.Data) != 1 || statement.Data[0].Kind != consoleapi.Redeem || statement.Data[0].Amount.Amount != "2500000000" {
		t.Fatalf("the statement after the redemption: %+v", statement)
	}
	export := f.do(http.MethodGet, "/statement.csv", "", as(s))
	if export.Code != http.StatusOK || !strings.Contains(export.Body.String(), ",redeem,") {
		t.Fatalf("the statement export after the redemption: %d %s", export.Code, export.Body)
	}

	// Expired, disabled, invalid and already used are different refusals, so the page can say what to do.
	f.run("UPDATE payments.redeem_codes SET expires_at = now() - interval '1 minute' WHERE id = '" + uuid.UUID(codes[1].ID).String() + "'")
	rec = f.do(http.MethodPost, "/redeem-codes", `{"code":"`+codes[1].Code+`"}`, as(s), idempotent("expired"))
	expect(t, "an expired code", rec, http.StatusUnprocessableEntity, consoleapi.ErrorCodeRedeemCodeExpired)
	f.run("UPDATE payments.redeem_codes SET state = 'disabled', disabled_at = now(), disabled_by = '" + createdBy.String() + "' WHERE id = '" + uuid.UUID(codes[2].ID).String() + "'")
	rec = f.do(http.MethodPost, "/redeem-codes", `{"code":"`+codes[2].Code+`"}`, as(s), idempotent("disabled"))
	expect(t, "a disabled code", rec, http.StatusUnprocessableEntity, consoleapi.ErrorCodeRedeemCodeDisabled)
	rec = f.do(http.MethodPost, "/redeem-codes", `{"code":"AAAAA-BBBBB-CCCCC-DDDDD"}`, as(s), idempotent("invalid"))
	expect(t, "an unknown code", rec, http.StatusUnprocessableEntity, consoleapi.ErrorCodeRedeemCodeInvalid)

	account, _ := f.account(s)
	f.run("UPDATE payments.redeem_attempts SET attempts = 30 WHERE kind = 'account' AND subject = '" + account.String() + "'")
	rec = f.do(http.MethodPost, "/redeem-codes", `{"code":"AAAAA-BBBBB-CCCCC-DDDDD"}`, as(s), idempotent(newKey()))
	expect(t, "the account's limit", rec, http.StatusTooManyRequests, consoleapi.ErrorCodeRateLimited)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a rate-limited redemption names no wait")
	}
}
