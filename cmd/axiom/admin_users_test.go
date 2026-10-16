package main

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
)

// users reads the pages of listUsers a request leads to, following the cursor to the last page, and returns the users
// in the order they came and the number of pages.
func (f adminFixture) users(s session, query string) ([]adminapi.UserSummary, int) {
	f.t.Helper()
	var users []adminapi.UserSummary
	cursor, pages := "", 0
	for {
		path := "/users?" + query
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		rec := f.do(http.MethodGet, path, "", as(s))
		expectAdmin(f.t, "listUsers "+path, rec, http.StatusOK, "")
		var list adminapi.UserList
		decode(f.t, rec, &list)
		users = append(users, list.Data...)
		pages++
		if list.NextCursor == nil {
			return users, pages
		}
		if pages > 20 {
			f.t.Fatal("listUsers pages without end")
		}
		cursor = *list.NextCursor
	}
}

func emailsOf(users []adminapi.UserSummary) []string {
	emails := make([]string, len(users))
	for i, u := range users {
		emails[i] = u.Email
	}
	return emails
}

// fiveUsers makes users after the owner, whose emails differ in what a prefix filter must tell apart, and returns the
// owner's session and the emails of all six, newest first.
func (f adminFixture) fiveUsers() (session, []string) {
	f.t.Helper()
	s := f.owner()
	for _, email := range []string{"alice@example.com", "a_c@example.com", "abc@example.com", "100%@example.com", "bob@example.com"} {
		f.addUser(email, "")
	}
	return s, []string{"bob@example.com", "100%@example.com", "abc@example.com", "a_c@example.com", "alice@example.com", ownerEmail}
}

// TestListUsersInPages: users newest first, in pages that follow each other without a gap or a repeat.
func TestListUsersInPages(t *testing.T) {
	f := newAdminFixture(t)
	s, newestFirst := f.fiveUsers()
	paged, pages := f.users(s, "limit=2")
	if diff := cmp.Diff(newestFirst, emailsOf(paged)); diff != "" || pages != 3 {
		t.Errorf("all the users in pages of 2: %d pages (-want +got):\n%s", pages, diff)
	}
	whole, pages := f.users(s, "")
	if diff := cmp.Diff(newestFirst, emailsOf(whole)); diff != "" || pages != 1 {
		t.Errorf("all the users in one page: %d pages (-want +got):\n%s", pages, diff)
	}
}

// TestListUsersFilters: by the start of the email, matched literally and without regard to case, and by site.
func TestListUsersFilters(t *testing.T) {
	f := newAdminFixture(t)
	s, newestFirst := f.fiveUsers()
	for query, want := range map[string][]string{
		"email_prefix=a":                          {"abc@example.com", "a_c@example.com", "alice@example.com"},
		"email_prefix=A":                          {"abc@example.com", "a_c@example.com", "alice@example.com"},
		"email_prefix=a_":                         {"a_c@example.com"},
		"email_prefix=100%25":                     {"100%@example.com"},
		"email_prefix=%25":                        nil,
		"email_prefix=owner@example.com":          {ownerEmail},
		"email_prefix=nobody":                     nil,
		"site_id=" + f.siteID():                   newestFirst,
		"site_id=site_00000000000000000000000000": nil,
	} {
		got, _ := f.users(s, query)
		if emails := emailsOf(got); !slices.Equal(want, emails) {
			t.Errorf("%s: %v, want %v", query, emails, want)
		}
	}
}

// TestListUsersFields: a user is listed with its account, site, tier, status, whether it has two-step verification and
// when it was made.
func TestListUsersFields(t *testing.T) {
	f := newAdminFixture(t)
	s, _ := f.fiveUsers()
	users, _ := f.users(s, "")
	if len(users) != 6 {
		t.Fatalf("%d users listed, want 6", len(users))
	}
	owner := users[len(users)-1]
	if owner.Status != "active" || owner.Tier.Name != "Default" || !strings.HasPrefix(owner.Tier.Id, "tier_") || !owner.MfaEnabled ||
		owner.Id != f.userOf(ownerEmail) || owner.AccountId != f.accountOf(ownerEmail) || owner.SiteId != f.siteID() ||
		time.Since(owner.CreatedAt) > time.Hour || owner.CreatedAt.After(time.Now()) {
		t.Errorf("the owner as listed: %+v", owner)
	}
	if users[0].MfaEnabled {
		t.Errorf("a user without two-step verification is listed with it: %+v", users[0])
	}
}

// TestListUsersRefusals: a cursor this API did not give out, a page size or a filter outside the contract is a 400.
func TestListUsersRefusals(t *testing.T) {
	f := newAdminFixture(t)
	s := f.owner()
	for _, query := range []string{"cursor=nonsense", "cursor=acct_00000000000000000000000000", "cursor=user_zzzzzzzzzzzzzzzzzzzzzzzzzz", "limit=0", "limit=201", "email_prefix=", "site_id=nonsense"} {
		expectAdmin(t, "listUsers?"+query, f.do(http.MethodGet, "/users?"+query, "", as(s)), http.StatusBadRequest, codeInvalidRequest)
	}
}

// TestGetAccount: an account with its tier, its owner and its balance in the site's currency; an ID that is not an
// account's, or no account's, is a 404.
func TestGetAccount(t *testing.T) {
	f := newAdminFixture(t)
	s := f.owner()
	f.addUser("alice@example.com", "")
	account := f.accountOf("alice@example.com")
	rec := f.do(http.MethodGet, "/accounts/"+account, "", as(s))
	expectAdmin(t, "getAccount", rec, http.StatusOK, "")
	var got adminapi.AccountDetail
	decode(t, rec, &got)
	zero := adminapi.Money{Amount: "0", Currency: "USD"}
	want := adminapi.AccountDetail{
		Id: account, SiteId: f.siteID(), Name: got.Name, Kind: "personal", Status: "active", Tier: got.Tier, CreatedAt: got.CreatedAt,
		Balance: adminapi.Balance{Balance: zero, Reserved: zero, Floor: zero, Available: zero, SpentThisMonth: zero},
	}
	want.Owner.Id, want.Owner.Email = f.userOf("alice@example.com"), "alice@example.com"
	if diff := cmp.Diff(want, got); diff != "" || got.Tier.Name != "Default" || time.Since(got.CreatedAt) > time.Hour {
		t.Errorf("the account (-want +got):\n%s", diff)
	}
	for _, id := range []string{"acct_00000000000000000000000000", "acct_zzzzzzzzzzzzzzzzzzzzzzzzzz", f.userOf("alice@example.com"), "nonsense"} {
		expectAdmin(t, "getAccount "+id, f.do(http.MethodGet, "/accounts/"+id, "", as(s)), http.StatusNotFound, codeNotFound)
	}
}

// grantBody is a request to give amount billionths of a USD.
func grantBody(amount, extra string) string {
	return `{"amount":{"amount":"` + amount + `","currency":"USD"},` + extra + `"reason":"goodwill"}`
}

// grantee is a fixture with an operator's session and an account to give credit to.
type grantee struct {
	adminFixture
	s       session
	account string // as the API writes it
	path    string
}

func newGrantee(t *testing.T) grantee {
	t.Helper()
	f := newAdminFixture(t)
	s := f.owner()
	f.addUser("alice@example.com", "")
	account := f.accountOf("alice@example.com")
	return grantee{f, s, account, "/accounts/" + account + "/credit-grants"}
}

// balanceOf is the balance of alice's account, read in the database.
func (g grantee) balanceOf() string {
	g.t.Helper()
	return g.text("SELECT balance::text FROM billing.balances WHERE account_id = (SELECT account_id FROM accounts.users WHERE email = 'alice@example.com')")
}

// TestGrantCredit: a grant is a batch, a ledger entry that moves the gift to the balance, and an audit entry with the
// reason, all of one transaction, answered with the balance after it.
func TestGrantCredit(t *testing.T) {
	g := newGrantee(t)
	expires := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	rec := g.change(g.s, http.MethodPost, g.path, grantBody("5000000000", `"expires_at":"`+expires.Format(time.RFC3339)+`",`), newKey())
	expectAdmin(t, "grantCredit", rec, http.StatusCreated, "")
	var grant adminapi.CreditGrant
	decode(t, rec, &grant)
	five := adminapi.Money{Amount: "5000000000", Currency: "USD"}
	zero := adminapi.Money{Amount: "0", Currency: "USD"}
	wantAfter := adminapi.Balance{Balance: five, Reserved: zero, Floor: zero, Available: five, SpentThisMonth: zero}
	if _, err := uuid.Parse(grant.Id); err != nil || grant.AccountId != g.account || grant.Amount != five || grant.ExpiresAt == nil ||
		!grant.ExpiresAt.Equal(expires) || time.Since(grant.CreatedAt) > time.Minute || rec.Header().Get("Idempotency-Replayed") != "" {
		t.Errorf("the grant: %+v replayed=%q", grant, rec.Header().Get("Idempotency-Replayed"))
	}
	if diff := cmp.Diff(wantAfter, grant.BalanceAfter); diff != "" {
		t.Errorf("the balance after (-want +got):\n%s", diff)
	}
	var seen adminapi.AccountDetail
	decode(t, g.do(http.MethodGet, "/accounts/"+g.account, "", as(g.s)), &seen)
	if diff := cmp.Diff(wantAfter, seen.Balance); diff != "" {
		t.Errorf("getAccount after the grant (-want +got):\n%s", diff)
	}
	g.expectGranted(grant.Id)
}

// expectGranted checks what a grant of 5 units left in the ledger and the audit log.
func (g grantee) expectGranted(batch string) {
	g.t.Helper()
	for query, want := range map[string]string{
		"SELECT count(*)::text FROM billing.credit_batches WHERE id = '" + batch + "' AND amount = 5000000000 AND source = 'gift' AND expires_at IS NOT NULL": "1",
		"SELECT coalesce(sum(amount), 0)::text FROM billing.entries WHERE book = 'gift_expense'":                                                              "5000000000",
		"SELECT coalesce(sum(amount), 0)::text FROM billing.entries":                                                                                          "0",
	} {
		if got := g.text(query); got != want {
			g.t.Errorf("%s = %s, want %s", query, got, want)
		}
	}
	g.expectAudit("credit.add", map[string]string{
		"actor_kind || ':' || actor_id": "user:" + g.text("SELECT id::text FROM accounts.users WHERE email = '"+ownerEmail+"'"),
		"reason":                        "goodwill",
		"target_id":                     g.text("SELECT account_id::text FROM accounts.users WHERE email = 'alice@example.com'"),
		"detail->>'amount'":             "5000000000",
	})
}

// TestGrantCreditReplay: the same request again, after a lost response, changes nothing and answers the same; a new
// key is a new grant, and a grant that does not expire has no expiry.
func TestGrantCreditReplay(t *testing.T) {
	g := newGrantee(t)
	body, key := grantBody("5000000000", ""), newKey()
	var first, again adminapi.CreditGrant
	decode(t, g.change(g.s, http.MethodPost, g.path, body, key), &first)
	rec := g.change(g.s, http.MethodPost, g.path, body, key)
	expectAdmin(t, "the same request again", rec, http.StatusCreated, "")
	decode(t, rec, &again)
	if diff := cmp.Diff(first, again); diff != "" || rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("the replay (-want +got) replayed=%q:\n%s", rec.Header().Get("Idempotency-Replayed"), diff)
	}
	if g.text("SELECT count(*)::text FROM billing.credit_batches") != "1" || g.auditCount("credit.add") != "1" || g.balanceOf() != "5000000000" {
		t.Error("the replay granted again")
	}
	rec = g.change(g.s, http.MethodPost, g.path, grantBody("1", ""), newKey())
	expectAdmin(t, "a second grant", rec, http.StatusCreated, "")
	var second adminapi.CreditGrant
	decode(t, rec, &second)
	if second.ExpiresAt != nil || second.BalanceAfter.Balance.Amount != "5000000001" || g.text("SELECT coalesce(sum(amount), 0)::text FROM billing.entries") != "0" {
		t.Errorf("the second grant: %+v", second)
	}
}

// TestGrantCreditKeyIsTheFirstRequests: a key is bound to the request, the account and the operator it was first used
// with; another amount, another account or another operator's is refused and changes nothing.
func TestGrantCreditKeyIsTheFirstRequests(t *testing.T) {
	g := newGrantee(t)
	body, key := grantBody("5000000000", ""), newKey()
	expectAdmin(t, "the first request", g.change(g.s, http.MethodPost, g.path, body, key), http.StatusCreated, "")
	expectAdmin(t, "the key with another amount", g.change(g.s, http.MethodPost, g.path, grantBody("6000000000", ""), key), http.StatusConflict, codeIdempotencyReuse)
	expectAdmin(t, "the key for another account", g.change(g.s, http.MethodPost, "/accounts/"+g.accountOf(ownerEmail)+"/credit-grants", body, key), http.StatusConflict, codeIdempotencyReuse)
	g.addUser("second@example.com", "admin")
	expectAdmin(t, "the key of another operator", g.change(g.verified("second@example.com"), http.MethodPost, g.path, body, key), http.StatusConflict, codeIdempotencyReuse)
	if g.text("SELECT count(*)::text FROM billing.credit_batches") != "1" || g.balanceOf() != "5000000000" {
		t.Error("a refused request granted")
	}
}

// TestGrantCreditRefusals: a grant that is not an amount of the account's currency is a 400, one that breaks a rule
// of a grant a 422; none changes anything, and the key of a refused request is free for the corrected one.
func TestGrantCreditRefusals(t *testing.T) {
	g := newGrantee(t)
	key := newKey()
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	for _, c := range []struct {
		name, body string
		status     int
		code       errorCode
		pointer    string
	}{
		{"another currency", `{"amount":{"amount":"1","currency":"CNY"},"reason":"r"}`, http.StatusBadRequest, codeInvalidRequest, "/amount/currency"},
		{"an amount past 64 bits", grantBody("9999999999999999999", ""), http.StatusBadRequest, codeInvalidRequest, "/amount/amount"},
		{"an amount that is no number", grantBody("1.5", ""), http.StatusBadRequest, codeInvalidRequest, "/amount/amount"},
		{"no reason", `{"amount":{"amount":"1","currency":"USD"}}`, http.StatusBadRequest, codeInvalidRequest, "/reason"},
		{"a reason of 1001 characters", `{"amount":{"amount":"1","currency":"USD"},"reason":"` + strings.Repeat("r", 1001) + `"}`, http.StatusBadRequest, codeInvalidRequest, "/reason"},
		{"a time that is none", grantBody("1", `"expires_at":"tomorrow",`), http.StatusBadRequest, codeInvalidRequest, "/expires_at"},
		{"nothing", grantBody("0", ""), http.StatusUnprocessableEntity, codeUnprocessable, ""},
		{"a negative amount", grantBody("-1", ""), http.StatusUnprocessableEntity, codeUnprocessable, ""},
		{"more than a grant may give", grantBody("10000000000001", ""), http.StatusUnprocessableEntity, codeUnprocessable, ""},
		{"an expiry that has passed", grantBody("1", `"expires_at":"`+past+`",`), http.StatusUnprocessableEntity, codeUnprocessable, ""},
	} {
		rec := g.change(g.s, http.MethodPost, g.path, c.body, key)
		expectAdmin(t, c.name, rec, c.status, c.code)
		if fields := errorDetailOf(t, rec).Fields; c.pointer != "" && (fields == nil || len(*fields) == 0 || (*fields)[0].Pointer != c.pointer) {
			t.Errorf("%s: fields %v, want %s", c.name, fields, c.pointer)
		}
	}
	if g.text("SELECT count(*)::text FROM billing.credit_batches")+g.auditCount("credit.add") != "00" {
		t.Error("a refused grant changed something")
	}
	expectAdmin(t, "the corrected request with the same key", g.change(g.s, http.MethodPost, g.path, grantBody("10000000000000", ""), key), http.StatusCreated, "")
}

// TestGrantCreditToNoAccount: an ID that is not an account's, or no account's, is a 404 and grants nothing.
func TestGrantCreditToNoAccount(t *testing.T) {
	g := newGrantee(t)
	for _, id := range []string{"acct_00000000000000000000000000", "acct_zzzzzzzzzzzzzzzzzzzzzzzzzz", g.userOf("alice@example.com")} {
		expectAdmin(t, "grantCredit to "+id, g.change(g.s, http.MethodPost, "/accounts/"+id+"/credit-grants", grantBody("1", ""), newKey()), http.StatusNotFound, codeNotFound)
	}
	if g.text("SELECT count(*)::text FROM billing.credit_batches") != "0" {
		t.Error("a grant to no account was made")
	}
}

// TestConcurrentGrantsWithOneKeyGrantOnce: requests with one key that arrive together wait on the claim; one grants and
// the others get its answer.
func TestConcurrentGrantsWithOneKeyGrantOnce(t *testing.T) {
	g := newGrantee(t)
	key := newKey()
	const requests = 6
	ids := make(chan string, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			rec := g.change(g.s, http.MethodPost, g.path, grantBody("7", ""), key)
			var grant adminapi.CreditGrant
			if err := decodeBytes(rec.Body.Bytes(), &grant); err != nil || rec.Code != http.StatusCreated {
				t.Errorf("a request: %d %s (%v)", rec.Code, rec.Body, err)
			}
			ids <- grant.Id
		})
	}
	wg.Wait()
	close(ids)
	distinct := map[string]bool{}
	for id := range ids {
		distinct[id] = true
	}
	if batches := g.text("SELECT count(*)::text FROM billing.credit_batches"); len(distinct) != 1 || batches != "1" || g.balanceOf() != "7" {
		t.Errorf("%d answers, %s batches, balance %s", len(distinct), batches, g.balanceOf())
	}
}
