package main

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/observability"
	"axiom/internal/platform/consoleapi"
)

func TestCallSessionsHTTP(t *testing.T) {
	t.Parallel()
	c := newCallsFixture(t)
	a := newCall(c.old.Account, c.now.Add(-30*time.Minute), "model-a", "codex")
	a.SessionID = "named"
	a.PriceNano = 10
	b, d := a, a
	b.ID, b.At, b.Model, b.PriceNano = uuid.NewV7(), a.At.Add(time.Second), "model-b", 20
	d.ID, d.At, d.PriceNano = uuid.NewV7(), a.At.Add(2*time.Second), 30
	other := a
	other.ID, other.SessionID = uuid.NewV7(), "second"
	c.f.record(a, b, d, other)
	query := url.Values{"since": {c.now.Add(-time.Hour).Format(time.RFC3339Nano)}, "until": {c.now.Format(time.RFC3339Nano)}, "limit": {"1"}, "workspace_id": {accounts.WorkspaceID(a.Workspace).TypeID()}}
	read := func(q url.Values) consoleapi.CallSessionList {
		t.Helper()
		rec := c.f.do(http.MethodGet, "/calls/sessions?"+q.Encode(), "", as(c.s))
		expect(t, "sessions", rec, http.StatusOK, "")
		var body consoleapi.CallSessionList
		decode(t, rec, &body)
		return body
	}
	first := read(query)
	if first.Data == nil || len(*first.Data) != 1 || first.NextCursor == nil {
		t.Fatalf("first page: %+v", first)
	}
	r := (*first.Data)[0]
	want := consoleapi.CallSession{
		SessionId: new("named"), ClientApp: new("codex"), Calls: new("3"),
		Price: &consoleapi.CallAggregateMoney{Amount: new("60"), Currency: new("USD")}, MainModel: new("model-a"), FirstAt: &a.At, LastAt: &d.At,
	}
	if diff := cmp.Diff(want, r); diff != "" {
		t.Fatalf("aggregate (-want +got):\n%s", diff)
	}
	query.Set("cursor", *first.NextCursor)
	second := read(query)
	if second.Data == nil || len(*second.Data) != 1 || *(*second.Data)[0].SessionId != "second" || second.NextCursor != nil {
		t.Fatalf("second page: %+v", second)
	}
	query.Set("cursor", (*first.NextCursor)[:74]+"!")
	expect(t, "tampered cursor", c.f.do(http.MethodGet, "/calls/sessions?"+query.Encode(), "", as(c.s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	query.Set("cursor", *first.NextCursor)
	query.Set("model", "model-b")
	expect(t, "changed filters", c.f.do(http.MethodGet, "/calls/sessions?"+query.Encode(), "", as(c.s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	for _, bad := range []string{"since=2026-09-01T00:00:00Z&until=2026-11-01T00:00:00Z", "cursor=broken", "workspace_id=bad"} {
		expect(t, bad, c.f.do(http.MethodGet, "/calls/sessions?"+bad, "", as(c.s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	}
	expect(t, "unauthenticated", c.f.do(http.MethodGet, "/calls/sessions", ""), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	d.ID, d.At = uuid.NewV7(), d.At.Add(time.Second)
	c.f.record(d)
	query.Del("cursor")
	query.Del("model")
	fresh := read(query)
	want.Calls, want.Price.Amount, want.LastAt = new("4"), new("90"), &d.At
	if diff := cmp.Diff(want, (*fresh.Data)[0]); diff != "" {
		t.Fatalf("refresh (-want +got):\n%s", diff)
	}
}

func TestSessionCursorIntegrity(t *testing.T) {
	t.Parallel()
	api := consoleAPI{sessionCursorKey: []byte("shared-identity-key")}
	q := observability.OwnQuery{Account: uuid.NewV7(), Since: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}
	pos := observability.Cursor{At: q.Since, ID: uuid.NewV7()}
	raw, err := api.sessionCursor(pos, q)
	if err != nil {
		t.Fatal(err)
	}
	got, err := api.parseSessionCursor(raw, q)
	if err != nil || *got != pos {
		t.Fatalf("round trip: %v %v", got, err)
	}
	for _, bad := range []string{"", raw[:74], strings.Repeat("!", 75), base64.RawURLEncoding.EncodeToString(make([]byte, 24)) + raw[32:], raw[:32] + strings.Repeat("!", 43), raw[:32] + strings.Repeat("A", 43)} {
		if _, err := api.parseSessionCursor(bad, q); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}

	zone := time.FixedZone("offset", 8*60*60)
	same := q
	same.Since, same.Until, same.PageSize = q.Since.In(zone), q.Until.In(zone), 200
	if _, err := api.parseSessionCursor(raw, same); err != nil {
		t.Fatalf("equivalent window or page size invalidated cursor: %v", err)
	}
	bad := q
	bad.Model = string([]byte{0xff})
	if _, err := api.parseSessionCursor(raw, bad); err == nil {
		t.Fatal("accepted invalid text in cursor filters")
	}
	if _, err := api.sessionCursor(pos, bad); err == nil {
		t.Fatal("signed invalid text")
	}
	q.Account = uuid.NewV7()
	if _, err := api.parseSessionCursor(raw, q); err == nil {
		t.Fatal("accepted other account")
	}
}
