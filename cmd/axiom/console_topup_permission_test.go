package main

import (
	"net/http"
	"testing"
)

func TestTopUpRequiresDedicatedPermission(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	order := f.order(s, "before-demotion", http.StatusCreated)
	f.run(`UPDATE accounts.members SET role_id=(SELECT id FROM accounts.roles WHERE name='viewer')`)
	for _, r := range []struct{ method, path, body string }{
		{http.MethodPost, "/orders", topUpBody},
		{http.MethodPost, "/orders/" + order.Id + "/cancel", ""},
		{http.MethodPost, "/orders/" + order.Id + "/verify", ""},
		{http.MethodGet, "/payment-methods?credits=" + tenCredits, ""},
	} {
		expect(t, r.path, f.do(r.method, r.path, r.body, as(s), idempotent(newKey())), http.StatusForbidden, "forbidden")
	}
	for _, path := range []string{"/balance", "/orders/" + order.Id} {
		expect(t, path, f.do(http.MethodGet, path, "", as(s)), http.StatusOK, "")
	}
	if creates, queries := f.gateway.asked(); creates != 1 || queries != 0 {
		t.Fatalf("unauthorized channel work: %d %d", creates, queries)
	}
	for _, role := range []string{"owner", "admin", "finance"} {
		f.run(`UPDATE accounts.members SET role_id=(SELECT id FROM accounts.roles WHERE name='` + role + `')`)
		expect(t, role+" quote", f.do(http.MethodGet, "/payment-methods?credits="+tenCredits, "", as(s)), http.StatusOK, "")
	}
}
