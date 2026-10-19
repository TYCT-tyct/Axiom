package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/consoleapi"
)

// pointersOf are the parts of the request a 400 names, sorted.
func pointersOf(t testing.TB, body []byte) []string {
	t.Helper()
	var parsed consoleapi.Error
	if err := decodeBytes(body, &parsed); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	var pointers []string
	if parsed.Error.Fields != nil {
		for _, f := range *parsed.Error.Fields {
			pointers = append(pointers, f.Pointer)
		}
	}
	slices.Sort(pointers)
	return pointers
}

// TestKeyInputIsChecked: what the contract's schemas cannot say, a rule across members or one of the table, is a 400 that
// names the members, before anything is written.
func TestKeyInputIsChecked(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	cases := []struct {
		name, body string
		pointers   []string
	}{
		{"a reset without a limit", `{"name":"k","limit_reset":"daily"}`, []string{"/limit_reset"}},
		{"a negative limit", `{"name":"k","spend_limit":{"amount":"-1","currency":"USD"}}`, []string{"/spend_limit/amount"}},
		{"a limit beyond 63 bits", `{"name":"k","spend_limit":{"amount":"9223372036854775808","currency":"USD"}}`, []string{"/spend_limit/amount"}},
		{"another currency", `{"name":"k","spend_limit":{"amount":"1","currency":"EUR"}}`, []string{"/spend_limit/currency"}},
		{"an empty model name", `{"name":"k","models":["a",""]}`, []string{"/models/1"}},
		{"addresses that are none", `{"name":"k","ip_allow":["10.0.0.1","999.1.1.1","fe80::1%eth0","10.0.0.0/33"]}`, []string{"/ip_allow/1", "/ip_allow/2", "/ip_allow/3"}},
		{"an expiry in the past", `{"name":"k","expires_at":"` + past + `"}`, []string{"/expires_at"}},
		{"several at once", `{"name":"k","limit_reset":"daily","models":[""],"expires_at":"` + past + `"}`, []string{"/expires_at", "/limit_reset", "/models/0"}},
	}
	for _, c := range cases {
		rec := f.do(http.MethodPost, "/keys", c.body, as(s))
		expect(t, c.name, rec, http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
		if got := pointersOf(t, rec.Body.Bytes()); !slices.Equal(got, c.pointers) {
			t.Errorf("%s: names %v, want %v", c.name, got, c.pointers)
		}
	}
	if got := f.text("SELECT count(*)::text FROM accounts.api_keys"); got != "0" {
		t.Errorf("%s keys after the refusals", got)
	}
	update := f.createKey(s, `{"name":"k"}`).Key.Id
	rec := f.do(http.MethodPut, "/keys/"+update, `{"name":"k","disabled":false,"limit_reset":"daily"}`, as(s))
	expect(t, "an update with a reset and no limit", rec, http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
}

// TestRefusalsAreTheContractsErrorBody: a validation failure through the whole stack is the contract's error body, and
// repeats nothing the client sent.
func TestRefusalsAreTheContractsErrorBody(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	const planted = "planted-secret-value"
	rec := f.do(http.MethodPost, "/auth/sign-in", `{"email":"ab","password":"`+planted+`"}`)
	expect(t, "validation", rec, http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	if rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" ||
		strings.Contains(rec.Body.String(), planted) || strings.Contains(f.logs.String(), planted) {
		t.Errorf("the refusal: %v %s (log %s)", rec.Header(), rec.Body, f.logs)
	}
	big := `{"name":"` + strings.Repeat("a", maxBody) + `"}`
	expect(t, "a body past the limit", f.do(http.MethodPost, "/auth/sign-in", big), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	expect(t, "a malformed body", f.do(http.MethodPost, "/auth/sign-in", `{"email":`), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
}

// TestBodiesFollowTheJSONPolicy: a body the project's JSON policy refuses is a 400 before any parser that would read it
// differently: a member name twice, bytes that are not UTF-8, nesting past the limit; nothing is made.
func TestBodiesFollowTheJSONPolicy(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	deep := strings.Repeat("[", 300) + strings.Repeat("]", 300)
	for name, body := range map[string]string{
		"a member name twice":      `{"name":"a","name":"b"}`,
		"bytes that are not UTF-8": "{\"name\":\"\xff\"}",
		"nesting too deep":         `{"name":"a","models":` + deep + `}`,
	} {
		expect(t, name, f.do(http.MethodPost, "/keys", body, as(s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	}
	if got := f.text("SELECT count(*)::text FROM accounts.api_keys"); got != "0" {
		t.Errorf("%s keys made from refused bodies", got)
	}
}

// TestMemberNamesMatchExactly: the schema check and the handler read a body's members by the same rule, the project's:
// names match exactly, case included. A member that differs from a declared one only in case is an unknown member,
// ignored, so it can neither carry a value past the schema's bounds nor overwrite one that passed them.
func TestMemberNamesMatchExactly(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	models := `["` + strings.Repeat(`a","`, 256) + `a"]` // 257 names, one past the bound
	for name, body := range map[string]string{
		"257 models under another case": `{"name":"k","MODELS":` + models + `}`,
		"a name overwritten by another": `{"name":"k","NAME":""}`,
		"mixed case in a nested object": `{"name":"k","spend_limit":{"amount":"1","currency":"USD","Currency":"x"}}`,
	} {
		created := f.createKey(s, body)
		if created.Key.Name != "k" || created.Key.Models != nil && len(*created.Key.Models) > 0 {
			t.Errorf("%s: made %+v", name, created.Key)
		}
	}
	if got := f.text("SELECT count(*)::text FROM accounts.api_keys WHERE name <> 'k' OR cardinality(models) > 0"); got != "0" {
		t.Errorf("%s keys made from the other cases' values", got)
	}
}
