package adminapi_test

import (
	"testing"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
)

// TestSupplierCreateKeepsTheLogin: the generated request type carries the sign-in and its refresh token through
// decoding, so an edit of the contract that drops `login` (or a member of it) breaks the build or this test.
func TestSupplierCreateKeepsTheLogin(t *testing.T) {
	const body = `{"name":"n","kind":"sub2api","origin":"https://a.example","upstream_account":"u","settlement_currency":"USD",
		"login":{"access_token":"at","refresh_token":"rt","access_token_expires_at":"2026-10-16T08:00:00Z"},"reason":"r"}`
	var req adminapi.SupplierCreate
	if err := jsonx.UnmarshalKnown([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	l := req.Login
	if l.AccessToken == nil || *l.AccessToken != "at" || l.RefreshToken == nil || *l.RefreshToken != "rt" ||
		l.AccessTokenExpiresAt == nil || req.SettlementCurrency != "USD" {
		t.Errorf("decoded %+v", req)
	}
}
