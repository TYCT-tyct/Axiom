package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"axiom/internal/accounts"
)

// TestContractRules: the rules the program enforces are the ones the contract declares: all 23 operations, with the
// level and permission each names, and whether it changes data.
func TestContractRules(t *testing.T) {
	t.Parallel()
	c := loadedContract(t)
	if len(c.byPattern) != 23 {
		t.Errorf("%d operations, want 23", len(c.byPattern))
	}
	manage, view := mustPermission(t, "api_keys.manage"), mustPermission(t, "api_keys.view")
	cases := []struct {
		pattern, level string
		permission     *accounts.Permission
		session        bool
		changes        bool
	}{
		{"GET /api/console/v1/version", "", nil, false, false},
		{"GET /api/console/v1/site", "", nil, false, false},
		{"POST /api/console/v1/auth/sign-in", "", nil, false, true},
		{"POST /api/console/v1/auth/register", "", nil, false, true},
		{"POST /api/console/v1/auth/mfa", "pending_mfa", nil, true, true},
		{"GET /api/console/v1/session", "any", nil, true, false},
		{"POST /api/console/v1/auth/sign-out", "any", nil, true, true},
		{"POST /api/console/v1/auth/recovery-codes", "mfa", nil, true, true},
		{"GET /api/console/v1/keys", "signed_in", &view, true, false},
		{"POST /api/console/v1/keys", "signed_in", &manage, true, true},
		{"DELETE /api/console/v1/keys/{key_id}", "signed_in", &manage, true, true},
		{"GET /api/console/v1/balance", "signed_in", mustPermissionPtr(t, "balance.view"), true, false},
		{"GET /api/console/v1/calls", "signed_in", mustPermissionPtr(t, "calls.view"), true, false},
	}
	for _, want := range cases {
		got, ok := c.byPattern[want.pattern]
		if !ok {
			t.Errorf("%s: not in the contract", want.pattern)
			continue
		}
		if got.level != want.level || got.session != want.session || got.changes != want.changes || (got.permission == nil) != (want.permission == nil) ||
			got.permission != nil && *got.permission != *want.permission {
			t.Errorf("%s: %+v, want %+v", want.pattern, got, want)
		}
	}
}

func mustPermission(t testing.TB, name string) accounts.Permission {
	t.Helper()
	p, ok := accounts.PermissionNamed(name)
	if !ok {
		t.Fatalf("%s is not declared", name)
	}
	return p
}

func mustPermissionPtr(t testing.TB, name string) *accounts.Permission {
	p := mustPermission(t, name)
	return &p
}

// TestContractThePrograminCannotEnforceIsRefused: a contract with an operation the program cannot enforce stops the
// start, whatever the mistake: it does not serve it unprotected.
func TestContractTheProgramCannotEnforceIsRefused(t *testing.T) {
	t.Parallel()
	file, err := os.ReadFile("../../api/console.yaml") // the contract's source; the program has it only generated into Go
	if err != nil {
		t.Fatal(err)
	}
	spec := string(file)
	cases := []struct{ name, from, to, want string }{
		{"a permission this program does not declare", "x-axiom-permission: balance.view", "x-axiom-permission: balance.wipe", "not declared"},
		{"a level that is none", "x-axiom-level: mfa", "x-axiom-level: root", "x-axiom-level"},
		{
			"no security declaration", "      security: []\n      responses:\n        \"200\":\n          description: The running version",
			"      responses:\n        \"200\":\n          description: The running version", "no security declaration",
		},
		{
			"a session operation that changes data without the CSRF scheme",
			"cannot be restored, and its calls stay in the call list.\n      security:\n        - session: []\n          csrf: []",
			"cannot be restored, and its calls stay in the call list.\n      security:\n        - session: []", "csrf",
		},
	}
	for _, c := range cases {
		if !strings.Contains(spec, c.from) {
			t.Fatalf("%s: the contract no longer contains the text this test changes", c.name)
		}
		doc, err := openapi3.NewLoader().LoadFromData([]byte(strings.Replace(spec, c.from, c.to, 1)))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		_, err = loadContract(doc)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error about %q", c.name, err, c.want)
		}
	}
}

// request is a request to the pattern with body, as JSON.
func request(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// TestContractValidation: what the contract's schemas say is enforced for every operation, at once and by name:
// lengths, patterns, enums, ranges, required members, types; each failure names the part of the request and the rule,
// never the value.
func TestContractValidation(t *testing.T) {
	t.Parallel()
	c := loadedContract(t)
	const secretValue = "planted-secret-value"
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name, pattern, target, body string
		pointers                    []string
	}{
		{"an email too short", "POST /api/console/v1/auth/sign-in", "/api/console/v1/auth/sign-in", `{"email":"ab","password":"` + secretValue + `"}`, []string{"/email"}},
		{"an empty password", "POST /api/console/v1/auth/sign-in", "/api/console/v1/auth/sign-in", `{"email":"abc","password":""}`, []string{"/password"}},
		{"a password too long", "POST /api/console/v1/auth/sign-in", "/api/console/v1/auth/sign-in", `{"email":"abc","password":"` + long(257) + `"}`, []string{"/password"}},
		{"both at once", "POST /api/console/v1/auth/sign-in", "/api/console/v1/auth/sign-in", `{"email":"ab","password":""}`, []string{"/email", "/password"}},
		{"a member missing", "POST /api/console/v1/auth/sign-in", "/api/console/v1/auth/sign-in", `{"email":"abc"}`, []string{"/password"}},
		{"a member of the wrong type", "POST /api/console/v1/auth/sign-in", "/api/console/v1/auth/sign-in", `{"email":"abc","password":12345678}`, []string{"/password"}},
		{"a short password at registration", "POST /api/console/v1/auth/register", "/api/console/v1/auth/register", `{"invite_code":"x","email":"abc","password":"` + secretValue[:11] + `"}`, []string{"/password"}},
		{"a code too short", "POST /api/console/v1/auth/mfa", "/api/console/v1/auth/mfa", `{"code":"12345"}`, []string{"/code"}},
		{"a code that is no digits", "POST /api/console/v1/auth/recovery-codes", "/api/console/v1/auth/recovery-codes", `{"code":"12345a"}`, []string{"/code"}},
		{"a secret too long", "POST /api/console/v1/auth/totp/confirm", "/api/console/v1/auth/totp/confirm", `{"secret":"` + long(129) + `","password":"p","code":"123456"}`, []string{"/secret"}},
		{"a key without a name", "POST /api/console/v1/keys", "/api/console/v1/keys", `{"name":""}`, []string{"/name"}},
		{"a key name of 101 characters", "POST /api/console/v1/keys", "/api/console/v1/keys", `{"name":"` + long(101) + `"}`, []string{"/name"}},
		{"a limit that is no number", "POST /api/console/v1/keys", "/api/console/v1/keys", `{"name":"k","spend_limit":{"amount":"12x","currency":"USD"}}`, []string{"/spend_limit/amount"}},
		{"a currency in lower case", "POST /api/console/v1/keys", "/api/console/v1/keys", `{"name":"k","spend_limit":{"amount":"1","currency":"usd"}}`, []string{"/spend_limit/currency"}},
		{"a reset that is none", "POST /api/console/v1/keys", "/api/console/v1/keys", `{"name":"k","limit_reset":"yearly"}`, []string{"/limit_reset"}},
		{"257 models", "POST /api/console/v1/keys", "/api/console/v1/keys", `{"name":"k","models":["` + strings.Repeat(`a","`, 256) + `a"]}`, []string{"/models"}},
		{"a model name of 201 characters", "POST /api/console/v1/keys", "/api/console/v1/keys", `{"name":"k","models":["` + long(201) + `"]}`, []string{"/models/0"}},
		{"an address of 65 characters", "POST /api/console/v1/keys", "/api/console/v1/keys", `{"name":"k","ip_allow":["` + long(65) + `"]}`, []string{"/ip_allow/0"}},
		{"settings without a disabled flag", "PUT /api/console/v1/keys/{key_id}", "/api/console/v1/keys/key_00000000000000000000000000", `{"name":"k"}`, []string{"/disabled"}},
		{"a body that is not JSON", "POST /api/console/v1/keys", "/api/console/v1/keys", `{"name":`, []string{""}},
		{"a body that is an array", "POST /api/console/v1/keys", "/api/console/v1/keys", `[]`, []string{""}},
		{"no body", "POST /api/console/v1/keys", "/api/console/v1/keys", ``, []string{""}},
		{"a limit of 0", "GET /api/console/v1/keys", "/api/console/v1/keys?limit=0", ``, []string{"limit"}},
		{"a limit of 201", "GET /api/console/v1/keys", "/api/console/v1/keys?limit=201", ``, []string{"limit"}},
		{"a cursor of 513 characters", "GET /api/console/v1/keys", "/api/console/v1/keys?cursor=" + long(513), ``, []string{"cursor"}},
		{"a time that is none", "GET /api/console/v1/calls", "/api/console/v1/calls?since=yesterday", ``, []string{"since"}},
		{"a model name of 201 characters in the filter", "GET /api/console/v1/calls", "/api/console/v1/calls?model=" + long(201), ``, []string{"model"}},
	}
	for _, tc := range cases {
		rl := c.byPattern[tc.pattern]
		method, _, _ := strings.Cut(tc.pattern, " ")
		fields := rl.validate(t.Context(), request(t, method, tc.target, tc.body))
		var pointers []string
		for _, f := range fields {
			pointers = append(pointers, f.Pointer)
			if f.Message == "" || strings.Contains(f.Message, secretValue) {
				t.Errorf("%s: the message %q is empty or repeats a value", tc.name, f.Message)
			}
		}
		slices.Sort(pointers)
		if !slices.Equal(pointers, tc.pointers) {
			t.Errorf("%s: pointers %v, want %v", tc.name, pointers, tc.pointers)
		}
	}
}

// TestContractAcceptsWhatItAllows: requests at the edges of the schemas pass, and members the contract does not name are
// ignored, not refused: a newer page may send more than this program knows.
func TestContractAcceptsWhatItAllows(t *testing.T) {
	t.Parallel()
	c := loadedContract(t)
	long := func(n int) string { return strings.Repeat("a", n) }
	for _, tc := range []struct{ pattern, target, body string }{
		{"POST /api/console/v1/auth/sign-in", "/x", `{"email":"abc","password":"` + long(256) + `","extra":true}`},
		{"POST /api/console/v1/keys", "/x", `{"name":"` + long(100) + `","models":["` + long(200) + `"],"limit_reset":"monthly","spend_limit":{"amount":"-5","currency":"USD"}}`},
		{"PUT /api/console/v1/keys/{key_id}", "/x", `{"name":"k","disabled":true,"ip_allow":["10.0.0.0/8"]}`},
		{"GET /api/console/v1/keys", "/x?limit=200&cursor=" + long(512), ``},
		{"GET /api/console/v1/calls", "/x?since=2026-10-01T00:00:00Z&until=2026-10-02T00:00:00%2B08:00&client_app=a", ``},
		{"GET /api/console/v1/keys/{key_id}", "/api/console/v1/keys/not-an-id", ``}, // a path ID is the handler's 404, not a 400
	} {
		method, _, _ := strings.Cut(tc.pattern, " ")
		if fields := c.byPattern[tc.pattern].validate(t.Context(), request(t, method, tc.target, tc.body)); len(fields) > 0 {
			t.Errorf("%s %s: %+v", tc.pattern, tc.target, fields)
		}
	}
}
