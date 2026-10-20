package main

import (
	"net/http"
	"testing"

	"axiom/internal/platform/adminapi"
)

// TestSetUserStatus: disabling a user's login answers with the user disabled, ends every session the user has and is
// audited with the reason; the same state again writes nothing, the same request under the same key is replayed, and
// enabling the user answers with it active.
func TestSetUserStatus(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	f.addUser("alice@example.com", "")
	id, path := f.userOf("alice@example.com"), "/users/"+f.userOf("alice@example.com")+"/status"
	alice := f.signIn("alice@example.com")
	key := newKey()
	rec := f.change(s, http.MethodPut, path, `{"status":"disabled","reason":"a stolen laptop"}`, key)
	expectAdmin(t, "setUserStatus", rec, http.StatusOK, "")
	var user adminapi.UserSummary
	decode(t, rec, &user)
	if user.Id != id || user.Status != "disabled" || user.Email != "alice@example.com" {
		t.Errorf("the user after the disabling: %+v", user)
	}
	f.expectAudit("user.disable", map[string]string{
		"actor_kind || ':' || actor_id": "user:" + f.userOf(ownerEmail), "target_kind": "user", "target_id": id,
		"detail->>'from'": "active", "detail->>'to'": "disabled", "detail->>'sessions'": "1", "reason": "a stolen laptop",
	})
	expectAdmin(t, "alice's session after the disabling", f.do(http.MethodGet, "/session", "", as(alice)), http.StatusUnauthorized, codeUnauthenticated)
	again := f.change(s, http.MethodPut, path, `{"status":"disabled","reason":"a stolen laptop"}`, key)
	expectAdmin(t, "setUserStatus under the same key", again, http.StatusOK, "")
	if again.Header().Get("Idempotency-Replayed") == "" || rec.Header().Get("Idempotency-Replayed") != "" {
		t.Errorf("the replay header: first %q, again %q", rec.Header().Get("Idempotency-Replayed"), again.Header().Get("Idempotency-Replayed"))
	}
	if n := f.auditCount("user.disable"); n != "1" {
		t.Errorf("%s audit entries of the disabling, want 1", n)
	}
	rec = f.change(s, http.MethodPut, path, `{"status":"active","reason":"back at work"}`, newKey())
	expectAdmin(t, "setUserStatus back", rec, http.StatusOK, "")
	decode(t, rec, &user)
	if user.Status != "active" {
		t.Errorf("the user after enabling it: %+v", user)
	}
	f.expectAudit("user.enable", map[string]string{"detail->>'from'": "disabled", "detail->>'to'": "active"})
	for _, id := range []string{
		"user_00000000000000000000000000", "user_zzzzzzzzzzzzzzzzzzzzzzzzzz", f.accountOf("alice@example.com"), "nonsense",
	} {
		expectAdmin(t, "setUserStatus "+id, f.change(s, http.MethodPut, "/users/"+id+"/status", `{"status":"disabled","reason":"r"}`, newKey()),
			http.StatusNotFound, codeNotFound)
	}
	for _, body := range []string{`{"status":"paused","reason":"r"}`, `{"status":"disabled"}`, `{"reason":"r"}`} {
		expectAdmin(t, "setUserStatus "+body, f.change(s, http.MethodPut, path, body, newKey()), http.StatusBadRequest, codeInvalidRequest)
	}
}

// TestSetAccountStatus: suspending an account answers with the account suspended and its money, and is audited with the
// reason; the same state again writes nothing, and resuming the account answers with it active.
func TestSetAccountStatus(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	f.addUser("alice@example.com", "")
	account := f.accountOf("alice@example.com")
	path := "/accounts/" + account + "/status"
	rec := f.change(s, http.MethodPut, path, `{"status":"disabled","reason":"unpaid"}`, newKey())
	expectAdmin(t, "setAccountStatus", rec, http.StatusOK, "")
	var detail adminapi.AccountDetail
	decode(t, rec, &detail)
	zero := adminapi.Money{Amount: "0", Currency: "USD"}
	want := adminapi.Balance{Balance: zero, Reserved: zero, Floor: zero, Available: zero, SpentThisMonth: zero}
	if detail.Id != account || detail.Status != "disabled" || detail.SiteId != f.siteID() || detail.Balance != want ||
		detail.Owner.Email != "alice@example.com" {
		t.Errorf("the account after the suspending: %+v", detail)
	}
	f.expectAudit("account.suspend", map[string]string{
		"actor_kind || ':' || actor_id": "user:" + f.userOf(ownerEmail), "target_kind": "account", "target_id": account,
		"detail->>'from'": "active", "detail->>'to'": "disabled", "reason": "unpaid",
	})
	if rec := f.change(s, http.MethodPut, path, `{"status":"disabled","reason":"unpaid"}`, newKey()); rec.Code != http.StatusOK {
		t.Errorf("suspending the account again: %d %s", rec.Code, rec.Body)
	}
	if n := f.auditCount("account.suspend"); n != "1" {
		t.Errorf("%s audit entries of the suspending, want 1", n)
	}
	rec = f.change(s, http.MethodPut, path, `{"status":"active","reason":"paid"}`, newKey())
	expectAdmin(t, "setAccountStatus back", rec, http.StatusOK, "")
	decode(t, rec, &detail)
	if detail.Status != "active" {
		t.Errorf("the account after resuming it: %+v", detail)
	}
	f.expectAudit("account.resume", map[string]string{"detail->>'from'": "disabled", "detail->>'to'": "active", "reason": "paid"})
	for _, id := range []string{"acct_00000000000000000000000000", "acct_zzzzzzzzzzzzzzzzzzzzzzzzzz", f.userOf("alice@example.com"), "nonsense"} {
		expectAdmin(t, "setAccountStatus "+id, f.change(s, http.MethodPut, "/accounts/"+id+"/status", `{"status":"disabled","reason":"r"}`, newKey()),
			http.StatusNotFound, codeNotFound)
	}
	for _, body := range []string{`{"status":"paused","reason":"r"}`, `{"status":"active"}`, `{"reason":"r"}`} {
		expectAdmin(t, "setAccountStatus "+body, f.change(s, http.MethodPut, path, body, newKey()), http.StatusBadRequest, codeInvalidRequest)
	}
}
