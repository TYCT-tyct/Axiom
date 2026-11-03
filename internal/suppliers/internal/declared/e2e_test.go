package declared

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
	"axiom/internal/suppliers/internal/client"
)

// The recorded relay below is a small system with the declaration's shape: a login, a public status with a version,
// groups with a ratio and formats, a price list per million tokens, paged keys with a policy, and a paged charge log
// that also holds records which are not charges. It answers over HTTP, so the test exercises the adapter through the
// real client, the egress policy and the declared rate buckets, not through a fake Client.
func TestDeclaredAdapterEndToEnd(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	var (
		mu      sync.Mutex
		seen    []string
		logins  []string
		creates []string
	)
	server := httptest.NewServer(newACME(t, now, &mu, &seen, &logins, &creates).handler())
	defer server.Close()

	a := loadAcme(t)
	clk := &testClock{t: now}
	a.now = clk.now
	c, err := client.New(server.URL, egress.NewForTesting(egresstest.Loopback(t)), client.NewLimiter(a.Buckets(), clk.now), clk.now)
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.Run(t, a)
	checkDetect(t, a, c)
	m := checkOpen(t, a, c)
	adaptertest.RunScan(t, m)
	checkAccount(t, m)
	checkOffer(t, a, m)
	checkKeys(t, m)
	checkCharges(t, m, now)

	mu.Lock()
	got := append([]string(nil), seen...)
	loginBodies := append([]string(nil), logins...)
	created := append([]string(nil), creates...)
	mu.Unlock()
	checkRecorded(t, got, loginBodies, created)
}

// acmeRelay is the recorded relay. Each path is the shape the declaration names, and every management path but the
// login requires the token the login answered with.
type acmeRelay struct {
	t       *testing.T
	now     time.Time
	mu      *sync.Mutex
	seen    *[]string
	logins  *[]string
	creates *[]string
}

func newACME(t *testing.T, now time.Time, mu *sync.Mutex, seen, logins, creates *[]string) *acmeRelay {
	return &acmeRelay{t: t, now: now, mu: mu, seen: seen, logins: logins, creates: creates}
}

func (s *acmeRelay) handler() http.HandlerFunc {
	routes := map[string]http.HandlerFunc{
		"/api/status": s.status,
		"/api/login":  s.login,
		"/api/me":     s.account,
		"/api/groups": s.groups,
		"/api/groups/g1/models": func(w http.ResponseWriter, r *http.Request) {
			s.models(w, r, `{"data":[{"name":"acme-chat","formats":["openai"]},{"name":"acme-reason","formats":["responses"]}]}`)
		},
		"/api/groups/g2/models": func(w http.ResponseWriter, r *http.Request) { s.models(w, r, `{"data":[{"name":"acme-claude"}]}`) },
		"/api/prices":           s.prices,
		"/api/keys":             s.keys,
		"/api/keys/k1/secret":   s.secret,
		"/api/charges":          s.charges,
	}
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		*s.seen = append(*s.seen, r.Method+" "+r.URL.RequestURI())
		s.mu.Unlock()
		route, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		route(w, r)
	}
}

func (*acmeRelay) status(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Acme", "1")
	writeBody(w, `{"data":{"version":"1.2.3","captcha":false}}`)
}

func (s *acmeRelay) login(w http.ResponseWriter, r *http.Request) {
	raw := readBody(s.t, r)
	s.mu.Lock()
	*s.logins = append(*s.logins, raw)
	s.mu.Unlock()
	writeBody(w, `{"data":{"token":"tok"}}`)
}

func (*acmeRelay) account(w http.ResponseWriter, r *http.Request) {
	if !authorized(w, r) {
		return
	}
	writeBody(w, `{"data":{"balance":"12.345","unit":"1 unit = $0.001","source":"wallet"}}`)
}

func (*acmeRelay) groups(w http.ResponseWriter, r *http.Request) {
	if !authorized(w, r) {
		return
	}
	writeBody(w, `{"data":[
		{"id":"g1","name":"default","ratio":"1","formats":["openai","responses"]},
		{"id":"g2","name":"claude","ratio":"2","formats":["anthropic"]},
		{"id":"g3","name":"auto","ratio":"auto"}
	]}`)
}

func (*acmeRelay) models(w http.ResponseWriter, r *http.Request, body string) {
	if !authorized(w, r) {
		return
	}
	writeBody(w, body)
}

func (*acmeRelay) prices(w http.ResponseWriter, r *http.Request) {
	if !authorized(w, r) {
		return
	}
	writeBody(w, `{"data":{"rates":[
		{"model":"acme-chat","input":2,"output":"4","cache_read":"0.5"},
		{"model":"acme-reason","input":"3","output":"6"},
		{"model":"acme-claude","input":"5","output":"10"}
	]}}`)
}

func (s *acmeRelay) keys(w http.ResponseWriter, r *http.Request) {
	handleKeys(s.t, w, r, s.now, s.mu, s.creates)
}

func (*acmeRelay) secret(w http.ResponseWriter, r *http.Request) {
	if !authorized(w, r) {
		return
	}
	writeBody(w, `{"data":{"key":"sk-k1"}}`)
}

func (s *acmeRelay) charges(w http.ResponseWriter, r *http.Request) {
	handleCharges(s.t, w, r, s.now)
}

func checkDetect(t *testing.T, a *Adapter, c adapter.Client) {
	t.Helper()
	detected, err := a.Detect(t.Context(), c)
	if err != nil || detected.Version != "1.2.3" || detected.Captcha {
		t.Fatalf("Detect: %+v, %v", detected, err)
	}
}

func checkOpen(t *testing.T, a *Adapter, c adapter.Client) adapter.Manager {
	t.Helper()
	login, err := a.NewLogin(adapter.Credentials{Email: "ops@example.test", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.Open(t.Context(), c, &loginStore{raw: login.Plaintext, version: 1})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func checkAccount(t *testing.T, m adapter.Manager) {
	t.Helper()
	account, err := m.Account(t.Context())
	if err != nil || account.Balance != "12.345" || account.UnitHint != "1 unit = $0.001" || account.Funding != adapter.FundingWallet {
		t.Fatalf("Account: %+v, %v", account, err)
	}
}

func checkOffer(t *testing.T, a *Adapter, m adapter.Manager) {
	t.Helper()
	offer, err := m.Offer(t.Context(), nil)
	if err != nil || !offer.Done || len(offer.Items) != 2 {
		t.Fatalf("Offer: %+v, %v", offer.Items, err)
	}
	groups := map[string]adapter.Group{}
	for _, g := range offer.Items {
		groups[g.Ref] = g
	}
	if _, ok := groups["g3"]; ok {
		t.Error("the auto-ratio group was offered")
	}
	checkFormats(t, a, groups)
	checkCosts(t, groups)
}

func checkFormats(t *testing.T, a *Adapter, groups map[string]adapter.Group) {
	t.Helper()
	if got := a.Calls().Formats(groups["g1"].Facts); len(got) != 2 || got[0] != protocol.Chat || got[1] != protocol.Responses {
		t.Errorf("g1 formats %v", got)
	}
	if got := a.Calls().Formats(groups["g2"].Facts); len(got) != 1 || got[0] != protocol.Messages {
		t.Errorf("g2 formats %v", got)
	}
}

func checkCosts(t *testing.T, groups map[string]adapter.Group) {
	t.Helper()
	for _, c := range []struct {
		group string
		item  catalog.Item
		want  string
	}{
		{"g1", catalog.ItemInput, "0.000002"},
		{"g1", catalog.ItemOutput, "0.000004"},
		{"g1", catalog.ItemCacheRead, "0.0000005"},
		{"g2", catalog.ItemInput, "0.00001"},
	} {
		if got := rateOf(t, groups[c.group].Models[0].Cost, c.item); got != c.want {
			t.Errorf("%s %s %q, want %q", c.group, c.item, got, c.want)
		}
	}
}

func checkKeys(t *testing.T, m adapter.Manager) {
	t.Helper()
	keys, err := m.Keys(t.Context(), nil)
	if err != nil || !keys.Done || len(keys.Items) != 3 {
		t.Fatalf("Keys: %+v, %v", keys.Items, err)
	}
	checkKeyRows(t, keys.Items)
	secretText, err := m.KeySecret(t.Context(), "k1")
	if err != nil || secretText.Reveal() != "sk-k1" {
		t.Fatalf("KeySecret: %q, %v", secretText.Reveal(), err)
	}
	made, err := m.(adapter.KeyMaker).CreateKey(t.Context(), "g1", "axiom-g1")
	if err != nil || made != "k9" {
		t.Fatalf("CreateKey: %q, %v", made, err)
	}
}

func checkKeyRows(t *testing.T, items []adapter.KeyState) {
	t.Helper()
	if items[0].Name != "axiom-g1" || items[0].Status != adapter.KeyActive {
		t.Errorf("first key %+v", items[0])
	}
	if items[1].Status != adapter.KeyDisabled || items[1].Policy == nil || len(items[1].Policy.Models) != 0 {
		t.Errorf("second key %+v", items[1])
	}
	if items[2].Status != adapter.KeyQuotaExhausted || items[2].ResetAt.IsZero() {
		t.Errorf("third key %+v", items[2])
	}
	if items[2].Policy == nil || len(items[2].Policy.Models) != 1 || items[2].Policy.Models[0] != "acme-claude" {
		t.Errorf("third key policy %+v", items[2].Policy)
	}
}

func checkCharges(t *testing.T, m adapter.Manager, now time.Time) {
	t.Helper()
	scan, err := (&scanState{
		V: 1, Watermark: now.Add(-time.Hour).Unix(), Span: 3600,
		LastLate: now.Unix(), LateEnd: now.Unix(), Connected: now.Add(-2 * time.Hour).Unix(),
	}).encode()
	if err != nil {
		t.Fatal(err)
	}
	first, next, err := m.Charges(t.Context(), scan)
	if err != nil || first.Done || len(first.Charges) != 1 {
		t.Fatalf("first charge page: %+v, %v", first, err)
	}
	checkCharge(t, first.Charges[0], now)
	last, final, err := m.Charges(t.Context(), next)
	if err != nil || !last.Done || len(last.Charges) != 0 || !last.Through.Equal(now.Add(-30*time.Second)) || len(final) == 0 {
		t.Fatalf("second charge page: %+v, %v", last, err)
	}
}

func checkCharge(t *testing.T, charge adapter.Charge, now time.Time) {
	t.Helper()
	if charge.Ref != "r1" || charge.Key != "k1" || charge.Amount != "0.5" || charge.Model != "acme-chat" ||
		!charge.OccurredAt.Equal(now.Add(-2*time.Minute)) || !charge.Reported.Has(catalog.ItemInput) ||
		!charge.Reported.Has(catalog.ItemOutput) || charge.Usage.Count(catalog.ItemInput) != 100 ||
		charge.Usage.Count(catalog.ItemOutput) != 50 || charge.Funding != adapter.FundingWallet {
		t.Errorf("charge %+v", charge)
	}
}

func checkRecorded(t *testing.T, seen, logins, creates []string) {
	t.Helper()
	if len(logins) != 1 || !strings.Contains(logins[0], `"email":"ops@example.test"`) || !strings.Contains(logins[0], `"password":"pw"`) {
		t.Errorf("login bodies %q", logins)
	}
	if len(creates) != 1 || !strings.Contains(creates[0], `"name":"axiom-g1"`) || !strings.Contains(creates[0], `"group_id":"g1"`) {
		t.Errorf("create bodies %q", creates)
	}
	if !slicesContain(seen, "GET /api/charges?start=") {
		t.Errorf("the charge requests %q", seen)
	}
}

func handleKeys(t *testing.T, w http.ResponseWriter, r *http.Request, now time.Time, mu *sync.Mutex, creates *[]string) {
	t.Helper()
	if !authorized(w, r) {
		return
	}
	if r.Method == http.MethodPost {
		raw := readBody(t, r)
		mu.Lock()
		*creates = append(*creates, raw)
		mu.Unlock()
		writeBody(w, `{"data":{"id":"k9"}}`)
		return
	}
	page := r.URL.Query().Get("page")
	if page == "1" {
		writeBody(w, `{"data":{"total":3,"items":[
			{"id":"k1","group_id":"g1","name":"axiom-g1","status":"enabled","models":["acme-chat"]},
			{"id":"k2","group_id":"g2","name":"axiom-g2","status":"disabled","models":[]}
		]}}`)
		return
	}
	writeBody(w, fmt.Sprintf(`{"data":{"total":3,"items":[
		{"id":"k3","group_id":"g2","name":"axiom-g2","status":"exhausted","reset_at":%q,"models":["acme-claude"],"expires_at":"2027-01-01T00:00:00Z"}
	]}}`, now.Add(2*time.Hour).Format(time.RFC3339)))
}

func handleCharges(t *testing.T, w http.ResponseWriter, r *http.Request, now time.Time) {
	t.Helper()
	if !authorized(w, r) {
		return
	}
	for _, key := range []string{"start", "end", "page", "size"} {
		if r.URL.Query().Get(key) == "" {
			t.Errorf("the charge request has no %s: %s", key, r.URL.RawQuery)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	if r.URL.Query().Get("page") == "2" {
		writeBody(w, `{"data":{"items":[]}}`)
		return
	}
	writeBody(w, fmt.Sprintf(`{"data":{"items":[
		{"type":"topup","amount":"100"},
		{"type":"consume","request_id":"r1","key_id":"k1","created_at":%d,"amount":"0.5","model":"acme-chat","source":"wallet","usage":{"input":100,"output":50}}
	]}}`, now.Add(-2*time.Minute).Unix()))
}

func authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") == "Bearer tok" {
		return true
	}
	w.WriteHeader(http.StatusUnauthorized)
	return false
}

func writeBody(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write([]byte(body)); err != nil {
		panic(err)
	}
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
		return ""
	}
	return string(raw)
}

func rateOf(t *testing.T, s *catalog.Schedule, item catalog.Item) string {
	t.Helper()
	if s == nil {
		t.Fatal("no cost")
	}
	for _, r := range s.View().Rates {
		if r.Item == item && r.Base {
			return r.Price.String()
		}
	}
	t.Fatalf("no %s rate", item)
	return ""
}

func slicesContain(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

type loginStore struct {
	raw     []byte
	version int64
}

func (s *loginStore) Load(context.Context) ([]byte, int64, error) { return s.raw, s.version, nil }

func (s *loginStore) Swap(_ context.Context, version int64, raw []byte) error {
	s.raw, s.version = raw, version+1
	return nil
}
