package main

import (
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/observability"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// account is the signed-in user's account and the site's ID.
func (f consoleFixture) account(s session) (account, site uuid.UUID) {
	f.t.Helper()
	var info consoleapi.Session
	decode(f.t, f.do(http.MethodGet, "/session", "", as(s)), &info)
	id, err := accounts.ParseAccountID(info.Account)
	if err != nil {
		f.t.Fatal(err)
	}
	return uuid.UUID(id), uuid.UUID(f.siteID())
}

func (f consoleFixture) siteID() accounts.SiteID {
	f.t.Helper()
	site, _, err := f.svc.SiteForHost(consoleHost)
	if err != nil {
		f.t.Fatal(err)
	}
	return site.ID
}

// run executes statements as the database's superuser, who is bound by no row-level security.
func (f consoleFixture) run(statements ...string) {
	f.t.Helper()
	if err := pgtest.Run(f.t.Context(), f.database.Superuser, statements...); err != nil {
		f.t.Fatal(err)
	}
}

func (f consoleFixture) balance(s session) consoleapi.Balance {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/balance", "", as(s))
	expect(f.t, "getBalance", rec, http.StatusOK, "")
	var balance consoleapi.Balance
	decode(f.t, rec, &balance)
	return balance
}

// TestBalance: a new account has nothing, a gift shows as balance and available, and the month's spending counts what
// the account's workspaces were charged since the first of the month, once each, not the keys' copies of it and not
// last month's.
func TestBalance(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	zero := f.balance(s)
	if zero.Balance.Amount != "0" || zero.Reserved.Amount != "0" || zero.Floor.Amount != "0" || zero.Available.Amount != "0" || zero.SpentThisMonth.Amount != "0" {
		t.Errorf("a new account: %+v", zero)
	}
	account, site := f.account(s)
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return billing.AddCredit(t.Context(), tx, billing.Credit{
			ID: uuid.NewV7(), Site: site, Account: account, Amount: 5_000_000_000, Actor: audit.System("test"), Reason: "a gift",
		})
	}); err != nil {
		t.Fatal(err)
	}
	key := f.createKey(s, `{"name":"Spender"}`).Key
	const workspace = "(SELECT id FROM accounts.workspaces WHERE account_id = '%ACCOUNT%')"
	spend := func(subject, window string, spent, reserved int) string {
		return `INSERT INTO billing.spend (subject, window_start, site_id, account_id, spent, reserved) SELECT ` + subject +
			`, date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '` + window + `', '` + site.String() + `', '` +
			account.String() + `', ` + strconv.Itoa(spent) + `, ` + strconv.Itoa(reserved)
	}
	keyID, err := accounts.ParseKeyID(key.Id)
	if err != nil {
		t.Fatal(err)
	}
	f.run(
		spend(strings.ReplaceAll(workspace, "%ACCOUNT%", account.String()), "1 day", 123, 0),
		spend(strings.ReplaceAll(workspace, "%ACCOUNT%", account.String()), "-1 day", 1000, 0), // last month
		spend("'"+uuid.UUID(keyID).String()+"'", "1 day", 123, 0),                              // the same charge on the key
	)
	got := f.balance(s)
	if got.Balance.Amount != "5000000000" || got.Available.Amount != "5000000000" || got.SpentThisMonth.Amount != "123" || got.Balance.Currency != "USD" {
		t.Errorf("after a gift and a charge: %+v", got)
	}
}

// TestKeyLimitRemaining: what a key may still spend is its limit less what it has spent and holds reserved in the
// window of its own reset; a key without a limit shows none.
func TestKeyLimitRemaining(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	account, site := f.account(s)
	key := f.createKey(s, `{"name":"Limited","spend_limit":{"amount":"1000","currency":"USD"},"limit_reset":"daily"}`).Key
	id, err := accounts.ParseKeyID(key.Id)
	if err != nil {
		t.Fatal(err)
	}
	f.run(`INSERT INTO billing.spend (subject, window_start, site_id, account_id, spent, reserved) VALUES
		('` + uuid.UUID(id).String() + `', date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', '` + site.String() + `', '` + account.String() + `', 300, 100),
		('` + uuid.UUID(id).String() + `', date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 day', '` + site.String() + `', '` + account.String() + `', 900, 0)`)
	var read consoleapi.Key
	decode(t, f.do(http.MethodGet, "/keys/"+key.Id, "", as(s)), &read)
	if read.LimitRemaining == nil || read.LimitRemaining.Amount != "600" {
		t.Errorf("remaining in today's window: %+v", read.LimitRemaining)
	}
	var list consoleapi.KeyList
	decode(t, f.do(http.MethodGet, "/keys", "", as(s)), &list)
	if len(list.Data) != 1 || list.Data[0].LimitRemaining == nil || list.Data[0].LimitRemaining.Amount != "600" {
		t.Errorf("remaining in the list: %+v", list.Data)
	}
	f.run(`UPDATE billing.spend SET spent = 5000 WHERE spent = 300`)
	decode(t, f.do(http.MethodGet, "/keys/"+key.Id, "", as(s)), &read)
	if read.LimitRemaining == nil || read.LimitRemaining.Amount != "0" {
		t.Errorf("remaining for a key past its limit: %+v", read.LimitRemaining)
	}
}

// newCall is a complete call record of account at at.
func newCall(account uuid.UUID, at time.Time, model, app string) observability.CallRecord {
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		panic(err)
	}
	return observability.CallRecord{
		ID: uuid.NewV7(), At: at, Site: uuid.NewV7(), Account: account, Workspace: uuid.NewV7(), Key: uuid.NewV7(),
		IP: netip.MustParseAddr("203.0.113.7"), ClientApp: app, Class: "chat", Model: model, Route: []byte(`{}`),
		Usage: map[catalog.Item]int64{catalog.ItemInput: 120, catalog.ItemOutput: 30}, ServiceTier: catalog.ServiceTierStandard,
		PriceNano: 5_000_000, CostNano: 3_000_000, Currency: currency, Latency: 1200 * time.Millisecond, FirstToken: 300 * time.Millisecond,
		FinishReason: "stop", ConsentVersion: "2026-10",
	}
}

// record writes calls through the recorder and waits for them to be stored.
func (f consoleFixture) record(calls ...observability.CallRecord) {
	f.t.Helper()
	r := observability.NewRecorder(f.t.Context(), f.db, pg.PlatformScope(), callRecords, slog.New(slog.DiscardHandler))
	for _, c := range calls {
		if err := r.Enqueue(c); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := r.Close(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
}

// callsFixture is an owner with three calls in the log, oldest first, and another account's call.
type callsFixture struct {
	f                          consoleFixture
	s                          session
	old, mid, recent, stranger observability.CallRecord
	now                        time.Time
}

func newCallsFixture(t *testing.T) callsFixture {
	t.Helper()
	f := newConsoleFixture(t)
	s := f.owner()
	account, _ := f.account(s)
	now := time.Now().UTC().Truncate(time.Millisecond)
	c := callsFixture{
		f: f, s: s, now: now,
		old:      newCall(account, now.Add(-3*time.Hour), "gpt-5", "codex"),
		mid:      newCall(account, now.Add(-2*time.Hour), "claude-opus", "claude-code"),
		recent:   newCall(account, now.Add(-time.Hour), "gpt-5", "claude-code"),
		stranger: newCall(uuid.NewV7(), now, "gpt-5", "codex"),
	}
	f.record(c.old, c.mid, c.recent, c.stranger)
	return c
}

func callIDs(list consoleapi.CallList) []string {
	var ids []string
	for _, c := range list.Data {
		ids = append(ids, c.Id)
	}
	return ids
}

// TestCallsPage: the list is the account's calls, newest first, in pages by cursor, each call as the contract gives it.
func TestCallsPage(t *testing.T) {
	c := newCallsFixture(t)
	var page consoleapi.CallList
	decode(t, c.f.do(http.MethodGet, "/calls?limit=2", "", as(c.s)), &page)
	if diff := cmp.Diff([]string{c.recent.ID.String(), c.mid.ID.String()}, callIDs(page)); diff != "" || page.NextCursor == nil {
		t.Fatalf("the first page (-want +got):\n%s\ncursor %v", diff, page.NextCursor)
	}
	want := consoleapi.Call{
		Id: c.recent.ID.String(), At: c.recent.At, Model: "gpt-5", ClientApp: "claude-code", RequestClass: "chat", Ip: "203.0.113.7",
		Usage: map[string]int64{"input": 120, "output": 30}, Price: consoleapi.Money{Amount: "5000000", Currency: "USD"},
		LatencyMs: 1200, FirstTokenMs: 300, FinishReason: "stop", ServiceTier: "standard", KeyId: page.Data[0].KeyId, WorkspaceId: page.Data[0].WorkspaceId,
	}
	if diff := cmp.Diff(want, page.Data[0], cmpopts.EquateApproxTime(time.Millisecond)); diff != "" {
		t.Errorf("a call (-want +got):\n%s", diff)
	}
	if !strings.HasPrefix(page.Data[0].KeyId, "key_") || !strings.HasPrefix(page.Data[0].WorkspaceId, "ws_") {
		t.Errorf("IDs %q and %q", page.Data[0].KeyId, page.Data[0].WorkspaceId)
	}
	var next consoleapi.CallList
	decode(t, c.f.do(http.MethodGet, "/calls?limit=2&cursor="+*page.NextCursor, "", as(c.s)), &next)
	if diff := cmp.Diff([]string{c.old.ID.String()}, callIDs(next)); diff != "" || next.NextCursor != nil {
		t.Errorf("the second page (-want +got):\n%s\ncursor %v", diff, next.NextCursor)
	}
	expect(t, "a cursor that is none", c.f.do(http.MethodGet, "/calls?cursor=nonsense", "", as(c.s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
}

// TestCallsFilters: the list can be cut by model, client application and time, and a rare filter finds what it matches.
func TestCallsFilters(t *testing.T) {
	c := newCallsFixture(t)
	old, mid, recent := c.old.ID.String(), c.mid.ID.String(), c.recent.ID.String()
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"model=gpt-5", []string{recent, old}},
		{"client_app=claude-code", []string{recent, mid}},
		{"model=gpt-5&client_app=claude-code", []string{recent}},
		{"since=" + c.now.Add(-150*time.Minute).Format(time.RFC3339), []string{recent, mid}},
		{"until=" + c.now.Add(-150*time.Minute).Format(time.RFC3339), []string{old}},
		{"model=nothing", nil},
	} {
		var page consoleapi.CallList
		decode(t, c.f.do(http.MethodGet, "/calls?"+tc.query, "", as(c.s)), &page)
		if diff := cmp.Diff(tc.want, callIDs(page)); diff != "" {
			t.Errorf("calls?%s (-want +got):\n%s", tc.query, diff)
		}
	}
}

// TestGetCall: one call is read by its ID; another account's call, an unknown ID and a malformed one are the same 404.
func TestGetCall(t *testing.T) {
	c := newCallsFixture(t)
	var one consoleapi.Call
	rec := c.f.do(http.MethodGet, "/calls/"+c.mid.ID.String(), "", as(c.s))
	expect(t, "getCall", rec, http.StatusOK, "")
	decode(t, rec, &one)
	if one.Id != c.mid.ID.String() || one.Model != "claude-opus" {
		t.Errorf("getCall: %s", rec.Body)
	}
	for _, path := range []string{
		"/calls/" + c.stranger.ID.String(), "/calls/" + uuid.NewV7().String(), "/calls/not-an-id", "/calls/" + strings.ToUpper(c.mid.ID.String()),
	} {
		expect(t, "GET "+path, c.f.do(http.MethodGet, path, "", as(c.s)), http.StatusNotFound, consoleapi.ErrorCodeNotFound)
	}
}
