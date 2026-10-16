package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// TestAdminContractValidation: what the admin's contract says of a request is enforced by name, header parameters
// included, and a failure names the part of the request and never the value.
func TestAdminContractValidation(t *testing.T) {
	t.Parallel()
	c := loadedAdminContract(t)
	const key = "0198c0a8-0000-7000-8000-000000000009"
	grant := `{"amount":{"amount":"1","currency":"USD"},"reason":"r"}`
	cases := []struct {
		name, pattern, target, body string
		headers                     map[string]string
		pointers                    []string
	}{
		{"no key", "POST /api/admin/v1/accounts/{account_id}/credit-grants", "/x", grant, nil, []string{"Idempotency-Key"}},
		{"a key that is no UUID", "POST /api/admin/v1/accounts/{account_id}/credit-grants", "/x", grant, map[string]string{"Idempotency-Key": "planted-secret-value"}, []string{"Idempotency-Key"}},
		{"a key in upper case", "POST /api/admin/v1/accounts/{account_id}/credit-grants", "/x", grant, map[string]string{"Idempotency-Key": strings.ToUpper(key)}, []string{"Idempotency-Key"}},
		{"a key and no reason", "POST /api/admin/v1/accounts/{account_id}/credit-grants", "/x", `{"amount":{"amount":"1","currency":"USD"}}`, map[string]string{"Idempotency-Key": key}, []string{"/reason"}},
		{"a limit of 201", "GET /api/admin/v1/users", "/x?limit=201", "", nil, []string{"limit"}},
	}
	for _, tc := range cases {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.target, strings.NewReader(tc.body))
		method, _, _ := strings.Cut(tc.pattern, " ")
		r.Method = method
		r.Header.Set("Content-Type", "application/json")
		for name, value := range tc.headers {
			r.Header.Set(name, value)
		}
		var pointers []string
		for _, f := range c.byPattern[tc.pattern].validate(t.Context(), r) {
			pointers = append(pointers, f.Pointer)
			if f.Message == "" || strings.Contains(f.Message, "planted-secret-value") {
				t.Errorf("%s: the message %q is empty or repeats a value", tc.name, f.Message)
			}
		}
		slices.Sort(pointers)
		if !slices.Equal(pointers, tc.pointers) {
			t.Errorf("%s: pointers %v, want %v", tc.name, pointers, tc.pointers)
		}
	}
}
