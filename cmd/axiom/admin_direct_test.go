package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
)

// directModel is the model a fake provider lists that the catalog has on sale; the provider lists one more, which the
// catalog does not know.
const (
	directModel = "claude-direct-1"
	directSpec  = `{"items":["input","output"],"tiers":[],"context_length":128000,"max_output_tokens":4096,"streams":true,"abilities":[]}`
)

// fakeProvider is an API that speaks Anthropic's formats over TLS on loopback. Its model list, of models, lets in a key
// that starts sk-good, refuses one that starts sk-refused, and fails with a 503 for any other; it answers /v1/messages
// with stream.
type fakeProvider struct {
	server *httptest.Server
	stream string
	mu     sync.Mutex
	seen   []supplied
}

func newFakeProvider(t *testing.T, stream string, models ...string) *fakeProvider {
	t.Helper()
	p := &fakeProvider{stream: stream}
	list := `{"data":[{"id":"` + strings.Join(models, `","type":"model"},{"id":"`) + `","type":"model"}],"has_more":false}`
	p.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("the fake provider could not read a request: %v", err)
		}
		key := r.Header.Get("X-Api-Key")
		p.mu.Lock()
		p.seen = append(p.seen, supplied{r.URL.Path, key + "|" + r.Header.Get("Authorization"), string(body)})
		p.mu.Unlock()
		switch {
		case strings.HasPrefix(key, "sk-refused"):
			w.WriteHeader(http.StatusUnauthorized)
		case !strings.HasPrefix(key, "sk-good"):
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			_, _ = io.WriteString(w, list) //nolint:errcheck // a test server
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, p.stream) //nolint:errcheck // a test server
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *fakeProvider) sent() []supplied {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]supplied(nil), p.seen...)
}

// newDirectFixture is the admin on a node that reaches the fake provider, with the provider's one known model listed
// in the catalog at the usd reference price.
func newDirectFixture(t *testing.T) (catalogFixture, *fakeProvider) {
	t.Helper()
	p := newFakeProvider(t, "", directModel, "mystery-model")
	base := newAdminFixture(t)
	node := startAdminNodeVia(base.commandLine, base.clock, egress.NewForTesting(egresstest.Loopback(t, p.server.Certificate())))
	f := catalogFixture{node, node.owner()}
	f.saveAuthor("anthropic")
	m := f.send(http.MethodPost, "/models", `{"slug":"anthropic/`+directModel+`","version_basis":"dated","price_source":"manual","spec":`+directSpec+`,"reason":"a model"}`, http.StatusCreated)
	m = f.publish(f.addName(f.addName(m, "anthropic/"+directModel), directModel), usd, nil)
	expectAdmin(t, "listing the model", f.setStatus(m, "listed"), http.StatusOK, "")
	return f, p
}

// directBody is a compatible Anthropic API at the fake provider with the keys given.
func directBody(origin string, keys ...string) string {
	return `{"provider":"compatible","name":"Harbor","origin":"` + origin + `","format":"anthropic","settlement_currency":"USD",` +
		`"keys":["` + strings.Join(keys, `","`) + `"],"max_concurrency":3,"requests_per_minute":60,"reason":"a direct source"}`
}

// TestCreateDirectSupplier: each key is checked once and only the one the provider let in is stored, sealed, with the
// limits given and the models it listed; the line has an endpoint for the catalog's model, costed at its official
// price, and the name the catalog does not know waits for a person. No key text is in the answer, the audit log or a
// table. The same request again is answered from its claim without asking the provider again.
func TestCreateDirectSupplier(t *testing.T) {
	t.Parallel()
	f, p := newDirectFixture(t)
	body := directBody(p.server.URL, "sk-good-key-0001", "sk-refused-0002", "sk-flaky-key-0003", "sk-good-key-0001")
	key := newKey()
	rec := f.change(f.s, http.MethodPost, "/suppliers/direct", body, key)
	expectAdmin(t, "createDirectSupplier", rec, http.StatusCreated, "")
	var made adminapi.SupplierConnected
	decode(t, rec, &made)
	if made.Supplier == nil {
		t.Fatal("no supplier was connected")
	}
	s := made.Supplier
	if got := []any{s.Kind, *s.Mode, *s.ApiFormat, s.Status, s.NativeUnit, s.SettlementCurrency, s.BalanceNative, s.UnitValue != nil && *s.UnitValue == "1"}; !cmp.Equal(got,
		[]any{"compatible", adminapi.SupplierMode("direct"), adminapi.ApiFormat("anthropic"), adminapi.SupplierStatus("active"), adminapi.SupplierNativeUnit("usd"), "USD", (*adminapi.Decimal)(nil), true}) {
		t.Errorf("the supplier is %v", got)
	}
	if diff := cmp.Diff([]string{
		"0 0001 passed  true", "1 0002 failed credential_rejected false", "2 0003 inconclusive upstream_error false", "3 0001 duplicate  false",
	}, outcomesOf(made.Checks)); diff != "" {
		t.Errorf("the checks (-want +got):\n%s", diff)
	}
	if n := len(p.sent()); n != 3 {
		t.Errorf("the provider was asked %d times, want once for each key that is not a duplicate", n)
	}
	f.checkDirectRows(s.Id)
	f.expectAudit("supplier.direct_connect", map[string]string{"reason": "a direct source", "target_id": s.Id, "detail->>'models'": "2", "detail->>'priced_endpoints'": "1"})
	if leaked := f.text(`SELECT count(*)::text FROM platform.audit_log WHERE detail::text LIKE '%sk-good%' OR detail::text LIKE '%sk-refused%'`); leaked != "0" || strings.Contains(rec.Body.String(), "sk-") {
		t.Error("a key's text is in the audit log or the answer")
	}
	f.checkDirectRetries(p, body, key, made)
}

// checkDirectRetries: the same request again is answered from its claim, and a stored key pasted for another supplier
// is a duplicate; neither asks the provider anything.
func (f catalogFixture) checkDirectRetries(p *fakeProvider, body, key string, made adminapi.SupplierConnected) {
	f.t.Helper()
	asked := len(p.sent())
	again := f.change(f.s, http.MethodPost, "/suppliers/direct", body, key)
	expectAdmin(f.t, "createDirectSupplier again", again, http.StatusCreated, "")
	var replay adminapi.SupplierConnected
	decode(f.t, again, &replay)
	if diff := cmp.Diff(made, replay); diff != "" || again.Header().Get("Idempotency-Replayed") != "true" {
		f.t.Errorf("the replay (-first +replay), replayed %q:\n%s", again.Header().Get("Idempotency-Replayed"), diff)
	}
	rec := f.change(f.s, http.MethodPost, "/suppliers/direct", strings.Replace(directBody(p.server.URL, "sk-good-key-0001"), "Harbor", "Harbor 2", 1), newKey())
	expectAdmin(f.t, "a stored key", rec, http.StatusOK, "")
	var stored adminapi.SupplierConnected
	decode(f.t, rec, &stored)
	if stored.Supplier != nil || len(stored.Checks) != 1 || stored.Checks[0].Outcome != "duplicate" {
		f.t.Errorf("a stored key: %+v", stored)
	}
	if n := len(p.sent()) - asked; n != 0 {
		f.t.Errorf("the provider was asked %d more times", n)
	}
}

// outcomesOf is each check as "index last4 outcome cause stored".
func outcomesOf(checks []adminapi.CredentialCheck) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		cause := ""
		if c.Cause != nil {
			cause = string(*c.Cause)
		}
		out[i] = strconv.Itoa(*c.Index) + " " + *c.Last4 + " " + string(c.Outcome) + " " + cause + " " + strconv.FormatBool(c.CredentialId != nil)
	}
	return out
}

// checkDirectRows: the one key that passed is stored with the request's limits and the models it listed, the catalog's
// model has an endpoint at its official price, and the name the catalog does not know waits for a person.
func (f catalogFixture) checkDirectRows(id string) {
	f.t.Helper()
	if got := f.text(`SELECT count(*)::text || ' ' || max(max_concurrency)::text || ' ' || max(requests_per_minute)::text || ' ' ||
		max(hint) || ' ' || max(array_to_string(allowed_models, ',')) || ' ' || max(status) FROM suppliers.credentials WHERE supplier_id = '` + id + `'`); got != "1 3 60 0001 "+directModel+",mystery-model active" {
		f.t.Errorf("the credentials are %q", got)
	}
	if got := f.text(`SELECT string_agg(e.upstream_name || ' ' || e.cost_state || ' ' || (c.schedule->'rates'->>'output') || ' ' || c.basis, ',')
		FROM suppliers.endpoints e JOIN suppliers.cost_schedules c ON c.id = e.cost_schedule_id WHERE e.supplier_id = '` + id + `'`); got != directModel+" known 0.000002 published" {
		f.t.Errorf("the endpoints are %q, want the listed model's at its official price", got)
	}
	if got := f.text(`SELECT string_agg(upstream_name || ' ' || (model_id IS NULL)::text, ',' ORDER BY upstream_name) FROM suppliers.supplier_models WHERE supplier_id = '` + id + `'`); got != directModel+" false,mystery-model true" {
		f.t.Errorf("the line's names are %q, want the unknown one waiting for a person", got)
	}
}

// TestCreateDirectSupplierNothingPassed:when no key passes, nothing is stored and the answer is 200 with the checks.
func TestCreateDirectSupplierNothingPassed(t *testing.T) {
	t.Parallel()
	f, p := newDirectFixture(t)
	rec := f.change(f.s, http.MethodPost, "/suppliers/direct", directBody(p.server.URL, "sk-refused-0009"), newKey())
	expectAdmin(t, "createDirectSupplier", rec, http.StatusOK, "")
	var got adminapi.SupplierConnected
	decode(t, rec, &got)
	if got.Supplier != nil || len(got.Checks) != 1 || got.Checks[0].Outcome != "failed" || *got.Checks[0].UpstreamStatus != 401 {
		t.Errorf("the answer is %+v", got)
	}
	if n := f.text("SELECT count(*)::text FROM suppliers.suppliers WHERE mode = 'direct'") + " " + f.auditCount("supplier.direct_connect"); n != "0 0" {
		t.Errorf("suppliers and audit entries: %s, want none", n)
	}
}

// TestCreateDirectSupplierRefusals: a built-in provider's address and format are its own, any other API needs both
// and a currency, Gemini's own format is not served, and a key with a space is no key; a role without suppliers.manage
// is forbidden. None of them asks the provider anything.
func TestCreateDirectSupplierRefusals(t *testing.T) {
	t.Parallel()
	f, p := newDirectFixture(t)
	compatible := directBody(p.server.URL, "sk-good-key-0001")
	for _, tc := range []struct {
		name, body string
		status     int
		pointer    string
	}{
		{"a built-in provider with an origin", `{"provider":"anthropic","name":"A","origin":"https://api.example.com","keys":["sk-good-key-0001"],"max_concurrency":1,"reason":"r"}`, 400, "/origin"},
		{"a built-in provider with a format", `{"provider":"openai","name":"A","format":"openai","keys":["sk-good-key-0001"],"max_concurrency":1,"reason":"r"}`, 400, "/format"},
		{"compatible without an origin", strings.Replace(compatible, `"origin":"`+p.server.URL+`",`, "", 1), 400, "/origin"},
		{"compatible without a format", strings.Replace(compatible, `"format":"anthropic",`, "", 1), 400, "/format"},
		{"compatible without a currency", strings.Replace(compatible, `"settlement_currency":"USD",`, "", 1), 400, "/settlement_currency"},
		{"Gemini's own format", strings.Replace(compatible, `"format":"anthropic"`, `"format":"gemini"`, 1), 422, "/format"},
		{"a key with a space", directBody(p.server.URL, "sk-good key-0001"), 400, "/keys/0"},
		{"a currency without rates", strings.Replace(compatible, `"USD"`, `"XAF"`, 1), 422, "/settlement_currency"},
	} {
		rec := f.change(f.s, http.MethodPost, "/suppliers/direct", tc.body, newKey())
		if rec.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body, tc.status)
			continue
		}
		if fields := errorDetailOf(t, rec).Fields; fields == nil || len(*fields) != 1 || (*fields)[0].Pointer != tc.pointer {
			t.Errorf("%s: %s, want a field at %s", tc.name, rec.Body, tc.pointer)
		}
	}
	f.addUser("viewer@example.com", "viewer")
	expectAdmin(t, "a viewer", f.change(f.verified("viewer@example.com"), http.MethodPost, "/suppliers/direct", compatible, newKey()), http.StatusForbidden, codeForbidden)
	if n := len(p.sent()); n != 0 {
		t.Errorf("the provider was asked %d times by requests that were refused", n)
	}
}
