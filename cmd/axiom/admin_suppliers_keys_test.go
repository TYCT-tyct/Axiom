package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// expectSame fails the test when got is not want.
func expectSame(t *testing.T, what string, got, want any) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("%s (-want +got):\n%s", what, diff)
	}
}

// fakeRelay is a new-api the tests run: its tokens by ID, which its management API makes, lists masked, searches by
// name and reveals, and its call API accepts while they are active. It keeps every management request it was sent.
type fakeRelay struct {
	mu      sync.Mutex
	tokens  map[int64]*relayToken
	next    int64
	creates int
	refuse  bool // every key is refused at the call API
	asked   []string
}

type relayToken struct {
	Name, Group, Key string
	Status           int
	Limits           string // the models the token may call, comma separated; empty for any
}

func newFakeRelay(t *testing.T) (*fakeRelay, string) {
	t.Helper()
	r := &fakeRelay{tokens: map[int64]*relayToken{}, next: 1}
	srv := httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(srv.Close)
	return r, srv.URL
}

// add is a token the relay lists, as a person made it there.
func (r *fakeRelay) add(group, key string) {
	r.addToken(relayToken{Group: group, Key: key, Status: 1})
}

// addToken is a token with its status and model limits as a person set them there.
func (r *fakeRelay) addToken(t relayToken) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Name = "by hand"
	r.tokens[r.next] = &t
	r.next++
}

// expectUnsent fails the test when a management request carried any of texts, or the part of it a listing shows:
// new-api logs every request's path and query.
func (r *fakeRelay) expectUnsent(t *testing.T, texts ...string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, text := range texts {
		plain := strings.TrimPrefix(text, "sk-")
		for _, asked := range r.asked {
			if strings.Contains(asked, plain) || strings.Contains(asked, maskedToken(plain)) {
				t.Errorf("the relay was sent %q", asked)
			}
		}
	}
}

// setRefuse makes the call API refuse every key, or accept the active ones again.
func (r *fakeRelay) setRefuse(refuse bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refuse = refuse
}

// made is how many keys the management API made, and the name of the last.
func (r *fakeRelay) made() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creates, r.tokens[r.next-1].Name
}

func (r *fakeRelay) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if req.URL.Path != "/v1/models" {
		r.asked = append(r.asked, req.Method+" "+req.URL.String())
	}
	switch {
	case req.URL.Path == "/v1/models":
		r.models(w, strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
	case req.Header.Get("Authorization") != "Bearer pat-secret":
		w.WriteHeader(http.StatusUnauthorized)
	case req.Method == http.MethodPost && req.URL.Path == "/api/token/":
		r.create(w, req)
	case req.Method == http.MethodGet && req.URL.Path == "/api/token/":
		r.list(w, req.URL.Query().Get("p"), req.URL.Query().Get("page_size"))
	case req.URL.Path == "/api/token/search":
		r.search(w, req.URL.Query().Get("keyword"), req.URL.Query().Get("token"))
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/key"):
		r.reveal(w, strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/api/token/"), "/key"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// models is the call API's list of models: 200 for an active key, 401 for any other.
func (r *fakeRelay) models(w http.ResponseWriter, key string) {
	for _, t := range r.tokens {
		if t.Key == key && t.Status == 1 && !r.refuse {
			_, _ = io.WriteString(w, `{"object":"list","data":[]}`) //nolint:errcheck // a test server
			return
		}
	}
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"error":{"message":"invalid token","type":"new_api_error"}}`) //nolint:errcheck // a test server
}

func (r *fakeRelay) create(w http.ResponseWriter, req *http.Request) {
	var made struct {
		Name  string `json:"name"`
		Group string `json:"group"`
	}
	body, _ := io.ReadAll(req.Body) //nolint:errcheck // a body that fails to read is refused below
	if jsonx.Unmarshal(body, &made) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.creates++
	r.tokens[r.next] = &relayToken{Name: made.Name, Group: made.Group, Key: "sk-made" + strconv.FormatInt(r.next, 10) + "abcdefgh", Status: 1}
	r.next++
	_, _ = io.WriteString(w, `{"success":true}`) //nolint:errcheck // a test server
}

func (r *fakeRelay) search(w http.ResponseWriter, name, key string) {
	var items []string
	for id, t := range r.tokens {
		if (name != "" && t.Name == name) || (key != "" && strings.TrimPrefix(t.Key, "sk-") == key) {
			items = append(items, fmt.Sprintf(`{"id":%d,"name":%q,"group":%q,"status":%d}`, id, t.Name, t.Group, t.Status))
		}
	}
	_, _ = fmt.Fprintf(w, `{"success":true,"data":{"items":[%s]}}`, strings.Join(items, ",")) //nolint:errcheck // a test server
}

// list is a page of the tokens by ID, each key masked as new-api masks it, with its model limits.
func (r *fakeRelay) list(w http.ResponseWriter, page, size string) {
	p, _ := strconv.Atoi(page) //nolint:errcheck // no page is the first
	n, _ := strconv.Atoi(size) //nolint:errcheck // no size is none
	p = max(p, 1)
	var items []string
	for id := int64(1); id < r.next; id++ {
		t, ok := r.tokens[id]
		if !ok || int(id) <= (p-1)*n || int(id) > p*n {
			continue
		}
		items = append(items, fmt.Sprintf(`{"id":%d,"name":%q,"group":%q,"status":%d,"key":%q,"expired_time":-1,"model_limits_enabled":%t,"model_limits":%q}`,
			id, t.Name, t.Group, t.Status, maskedToken(strings.TrimPrefix(t.Key, "sk-")), t.Limits != "", t.Limits))
	}
	_, _ = fmt.Fprintf(w, `{"success":true,"data":{"page":%d,"page_size":%d,"total":%d,"items":[%s]}}`, p, n, len(r.tokens), strings.Join(items, ",")) //nolint:errcheck // a test server
}

// maskedToken is a key as new-api's listing shows it (model/token.go MaskTokenKey), for a key longer than eight.
func maskedToken(key string) string {
	return key[:4] + "**********" + key[len(key)-4:]
}

func (r *fakeRelay) reveal(w http.ResponseWriter, ref string) {
	id, _ := strconv.ParseInt(ref, 10, 64) //nolint:errcheck // a ref that is no number is no token
	t, ok := r.tokens[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = fmt.Fprintf(w, `{"success":true,"data":{"key":%q}}`, strings.TrimPrefix(t.Key, "sk-")) //nolint:errcheck // a test server
}

// relayFixture is a connected new-api supplier at a fake relay, in use, with two groups.
type relayFixture struct {
	supplierFixture
	relay                  *fakeRelay
	supplier, def, premium string
}

func newRelayFixture(t *testing.T) relayFixture {
	t.Helper()
	relay, origin := newFakeRelay(t)
	base := newAdminFixture(t)
	f := supplierFixture{startAdminNodeVia(base.commandLine, base.clock, egress.NewForTesting(egresstest.Loopback(t))), base.owner()}
	supplier := f.connect().Id
	f.run("ALTER TABLE suppliers.suppliers DROP CONSTRAINT suppliers_origin_check")
	f.run(fmt.Sprintf(`UPDATE suppliers.suppliers SET origin = '%s', status = 'active', balance_native = 100 WHERE id = '%s'`, origin, supplier))
	return relayFixture{f, relay, supplier, f.group(supplier, "default"), f.group(supplier, "premium")}
}

func (f relayFixture) path(rest string) string { return "/suppliers/" + f.supplier + rest }

// connectGroup connects a group with the body and reads the answer.
func (f relayFixture) connectGroup(group, body string) adminapi.GroupConnected {
	f.t.Helper()
	var connected adminapi.GroupConnected
	f.send(http.MethodPost, f.path("/groups/"+group+"/connection"), body, http.StatusCreated, &connected)
	return connected
}

// TestConnectSupplierGroupRefusals: a key with make, paste without one, a key of another group, a key the supplier does
// not list and a group the supplier lacks are refused before anything is stored.
func TestConnectSupplierGroupRefusals(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.relay.add("default", "sk-pasted-default-0099")
	f.relay.addToken(relayToken{Group: "premium", Key: "sk-pasted-disabled-0098", Status: 2})
	for _, c := range []struct {
		name, group, body string
		status            int
		code              errorCode
	}{
		{"make with a key", f.def, `{"key_source":"make","key":"sk-0123456789","reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		{"paste without a key", f.def, `{"key_source":"paste","reason":"r"}`, http.StatusBadRequest, codeInvalidRequest},
		{"a key of another group", f.premium, `{"key_source":"paste","key":"sk-pasted-default-0099","reason":"r"}`, http.StatusUnprocessableEntity, codeUnprocessable},
		{"a key the supplier does not list", f.premium, `{"key_source":"paste","key":"sk-nobody-knows-it","reason":"r"}`, http.StatusUnprocessableEntity, codeUnprocessable},
		{"a key the supplier disabled", f.premium, `{"key_source":"paste","key":"sk-pasted-disabled-0098","reason":"r"}`, http.StatusUnprocessableEntity, codeUnprocessable},
		{"no such group", uuid.NewV7().String(), `{"key_source":"make","reason":"r"}`, http.StatusNotFound, codeNotFound},
	} {
		expectAdmin(t, c.name, f.change(f.s, http.MethodPost, f.path("/groups/"+c.group+"/connection"), c.body, newKey()), c.status, c.code)
	}
	expectSame(t, "credentials stored", f.text("SELECT count(*)::text FROM suppliers.credentials"), "0")
	f.relay.expectUnsent(t, "sk-pasted-default-0099", "sk-nobody-knows-it", "sk-pasted-disabled-0098")
}

// TestConnectSupplierGroup: a group gets a key made for it at the supplier under the sync's name for it, or a pasted one
// the supplier lists under it; either way it is selected and its credential is active, and a pasted key can call only
// what the supplier lets it. A replay answers the first result without asking the supplier again, a group that has a
// key already is a conflict before a key is made at the supplier, and the pasted key is nowhere but sealed.
func TestConnectSupplierGroup(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	pasted := "sk-pasted-premium-0042"
	f.relay.addToken(relayToken{Group: "premium", Key: pasted, Status: 1, Limits: "gpt-a,gpt-b"})
	key := newKey()
	body := `{"key_source":"make","reason":"first line"}`
	rec := f.change(f.s, http.MethodPost, f.path("/groups/"+f.def+"/connection"), body, key)
	expectAdmin(t, "make", rec, http.StatusCreated, "")
	var made adminapi.GroupConnected
	decode(t, rec, &made)
	expectSame(t, "made", []any{made.Group.Selected, made.Group.Codename != nil, made.Credential.Status, valueOf(made.Credential.KeySource), made.Credential.MaxConcurrency},
		[]any{true, true, adminapi.CredentialStatus("active"), adminapi.SupplierCredentialKeySource("made"), 4})
	replay := f.change(f.s, http.MethodPost, f.path("/groups/"+f.def+"/connection"), body, key)
	creates, name := f.relay.made()
	expectSame(t, "replayed", []any{replay.Code, replay.Header().Get("Idempotency-Replayed"), creates, strings.HasPrefix(name, "axiom-")},
		[]any{http.StatusCreated, "true", 1, true})
	expectAdmin(t, "a group with a key", f.change(f.s, http.MethodPost, f.path("/groups/"+f.def+"/connection"), `{"key_source":"make","reason":"r"}`, newKey()), http.StatusConflict, codeConflict)
	creates, _ = f.relay.made()
	expectSame(t, "keys made at the supplier", creates, 1)

	connected := f.connectGroup(f.premium, `{"key_source":"paste","key":"`+pasted+`","reason":"r"}`)
	expectSame(t, "pasted", []any{valueOf(connected.Credential.Last4), valueOf(connected.Credential.KeySource), connected.Credential.UpstreamKeyRef},
		[]any{"0042", adminapi.SupplierCredentialKeySource("pasted"), "1"})
	expectSame(t, "what each key may call", f.text(`SELECT string_agg(source || ':' || coalesce(allowed_models::text, 'any'), ' ' ORDER BY source) FROM suppliers.credentials`),
		"made:any pasted:{gpt-a,gpt-b}")
	f.relay.expectUnsent(t, pasted)
	expectSame(t, "where the pasted text is", f.text(`SELECT
		(SELECT count(*) FROM platform.audit_log WHERE detail::text LIKE '%pasted-premium%') ||
		'/' || (SELECT count(*) FROM platform.idempotency WHERE response::text LIKE '%pasted-premium%') ||
		'/' || (SELECT count(*) FROM platform.audit_log WHERE action = 'supplier_group.connect')`), "0/0/2")
}

// TestIdentifySupplierKey: the supplier says which group a pasted key is under; a key it does not list has no group;
// ten a minute per session.
func TestIdentifySupplierKey(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.relay.add("premium", "sk-identify-me-7777")
	var id adminapi.KeyIdentification
	f.sendSafe(f.path("/key-identifications"), `{"key":"sk-identify-me-7777"}`, http.StatusOK, &id)
	expectSame(t, "identified", []any{valueOf(id.GroupId), id.Last4, valueOf(id.UpstreamKeyRef), valueOf(id.Status), id.CredentialId},
		[]any{f.premium, "7777", "1", adminapi.KeyIdentificationStatus("active"), (*string)(nil)})
	var unknown adminapi.KeyIdentification
	f.sendSafe(f.path("/key-identifications"), `{"key":"sk-nobody-has-this"}`, http.StatusOK, &unknown)
	expectSame(t, "unknown", []any{unknown.GroupId, unknown.Last4}, []any{(*string)(nil), "this"})
	expectAdmin(t, "a key with a space", f.do(http.MethodPost, f.path("/key-identifications"), `{"key":"sk-has a space"}`, as(f.s)), http.StatusBadRequest, codeInvalidRequest)
	for range detectionsPerMinute - 3 {
		f.do(http.MethodPost, f.path("/key-identifications"), `{"key":"sk-nobody-has-this"}`, as(f.s))
	}
	expectAdmin(t, "the eleventh", f.do(http.MethodPost, f.path("/key-identifications"), `{"key":"sk-nobody-has-this"}`, as(f.s)), http.StatusTooManyRequests, codeRateLimited)
	f.relay.expectUnsent(t, "sk-identify-me-7777", "sk-nobody-has-this")
}

// sendSafe sends a request that changes nothing and reads the answer.
func (f supplierFixture) sendSafe(path, body string, status int, v any) {
	f.t.Helper()
	rec := f.do(http.MethodPost, path, body, as(f.s))
	expectAdmin(f.t, "POST "+path, rec, status, "")
	decode(f.t, rec, v)
}

// outcomes are the checks of a list as outcome/cause at their index.
func outcomes(list adminapi.CredentialCheckList) []string {
	out := make([]string, len(list.Data))
	for i, c := range list.Data {
		out[i] = fmt.Sprintf("%d %s", valueOf(c.Index), c.Outcome)
		if c.Cause != nil {
			out[i] += "/" + string(*c.Cause)
		}
	}
	return out
}

// TestAddSupplierKeys: each pasted key is checked and only the ones that pass are stored, each its own credential with
// the limits given; a repeat, a key of another group and a key of nobody's are reported, and nothing of a key but its
// last four characters comes back. A request that stores nothing answers 200.
func TestAddSupplierKeys(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.relay.addToken(relayToken{Group: "default", Key: "sk-good-default-1111", Status: 1, Limits: "gpt-a"})
	f.relay.add("premium", "sk-good-premium-2222")
	keys := `["sk-good-default-1111","sk-good-default-1111","sk-good-premium-2222","sk-unknown-key-3333"]`
	body := `{"group_id":"` + f.def + `","keys":` + keys + `,"max_concurrency":2,"requests_per_minute":60,"reason":"more keys"}`
	key := newKey()
	rec := f.change(f.s, http.MethodPost, f.path("/keys"), body, key)
	expectAdmin(t, "add", rec, http.StatusCreated, "")
	var checks adminapi.CredentialCheckList
	decode(t, rec, &checks)
	expectSame(t, "the checks", outcomes(checks), []string{"0 passed", "1 duplicate", "2 failed/wrong_group", "3 failed/credential_rejected"})
	expectSame(t, "the key's text in the answer", strings.Contains(rec.Body.String(), "good-default"), false)
	expectSame(t, "the stored key", f.text(`SELECT max_concurrency || '/' || requests_per_minute || '/' || source || '/' || hint FROM suppliers.credentials WHERE id = '`+valueOf(checks.Data[0].CredentialId)+`'`), "2/60/pasted/1111")
	expectSame(t, "what the stored key may call", f.text(`SELECT allowed_models::text FROM suppliers.credentials`), "{gpt-a}")
	f.relay.expectUnsent(t, "sk-good-default-1111", "sk-good-premium-2222", "sk-unknown-key-3333")
	replay := f.change(f.s, http.MethodPost, f.path("/keys"), body, key)
	expectSame(t, "replay", []any{replay.Code, replay.Header().Get("Idempotency-Replayed"), f.text("SELECT count(*)::text FROM suppliers.credentials")},
		[]any{http.StatusCreated, "true", "1"})
	again := `{"group_id":"` + f.def + `","keys":["sk-good-default-1111"],"max_concurrency":2,"reason":"again"}`
	var stored adminapi.CredentialCheckList
	f.send(http.MethodPost, f.path("/keys"), again, http.StatusOK, &stored)
	expectSame(t, "a key stored already", outcomes(stored), []string{"0 duplicate"})
	for name, bad := range map[string]string{
		"no group":     `{"keys":["sk-0123456789"],"max_concurrency":2,"reason":"r"}`,
		"no keys":      `{"group_id":"` + f.def + `","keys":[],"max_concurrency":2,"reason":"r"}`,
		"a key spaced": `{"group_id":"` + f.def + `","keys":["sk-0123 456789"],"max_concurrency":2,"reason":"r"}`,
	} {
		expectAdmin(t, name, f.change(f.s, http.MethodPost, f.path("/keys"), bad, newKey()), http.StatusBadRequest, codeInvalidRequest)
	}
}

// routedCredential is a pasted key of the default group with an endpoint this node routes through it, and a function
// that says whether a call can be admitted now, without reloading.
func (f relayFixture) routedCredential() (string, func() error) {
	f.t.Helper()
	f.relay.add("default", "sk-routed-key-5555")
	connected := f.connectGroup(f.def, `{"key_source":"paste","key":"sk-routed-key-5555","reason":"r"}`)
	endpoint := uuid.MustParse(f.routedEndpoint(f.supplier, f.def))
	return f.path("/credentials/" + connected.Credential.Id), func() error {
		a, err := f.upstreams.Acquire(endpoint)
		if err == nil {
			a.Done()
		}
		return err
	}
}

// routedEndpoint makes the group's endpoint the sync would make and has this node load it.
func (f supplierFixture) routedEndpoint(supplier, group string) string {
	f.t.Helper()
	statements := endpointStatements(uuid.MustParse(supplier), uuid.MustParse(group), uuid.NewV7(), "gpt", "chat")
	if err := f.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		for _, s := range statements {
			if _, err := tx.Exec(f.t.Context(), s.sql, s.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	f.run(fmt.Sprintf(`INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ('%s', 1, 'test', 'seed')`, supplier))
	if err := f.upstreams.Reload(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
	return strings.TrimSpace(f.text("SELECT id::text FROM suppliers.endpoints WHERE supplier_group_id = '" + group + "'"))
}

// routed is whether acquire admitted a call; false is the endpoint having left routing.
func routed(t *testing.T, acquire func() error) bool {
	t.Helper()
	err := acquire()
	if err != nil && !errors.Is(err, suppliers.ErrNoEndpoint) {
		t.Fatal(err)
	}
	return err == nil
}

// TestCredentialChecks: a check the supplier refuses makes the credential rejected and this node stops routing through
// it at once; one it accepts again brings it back; a disabled one is not checked, and a check of every key skips it.
func TestCredentialChecks(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	credential, acquire := f.routedCredential()
	expectSame(t, "routed at first", routed(t, acquire), true)
	f.relay.setRefuse(true)
	var check adminapi.CredentialCheck
	f.send(http.MethodPost, credential+"/tests", `{"reason":"is it still good"}`, http.StatusCreated, &check)
	expectSame(t, "refused", []any{check.Outcome, valueOf(check.Cause), valueOf(check.UpstreamStatus), routed(t, acquire)},
		[]any{adminapi.CredentialCheckOutcome("failed"), adminapi.CredentialCheckCause("credential_rejected"), 401, false})
	f.relay.setRefuse(false)
	f.send(http.MethodPost, credential+"/tests", `{"reason":"fixed at the supplier"}`, http.StatusCreated, &check)
	expectSame(t, "accepted", []any{check.Outcome, routed(t, acquire)}, []any{adminapi.CredentialCheckOutcome("passed"), true})

	var c adminapi.SupplierCredential
	f.send(http.MethodPut, credential+"/enabled", `{"enabled":false,"reason":"pause it"}`, http.StatusOK, &c)
	expectAdmin(t, "check a disabled key", f.change(f.s, http.MethodPost, credential+"/tests", `{"reason":"r"}`, newKey()), http.StatusConflict, codeConflict)
	var all adminapi.CredentialCheckList
	f.send(http.MethodPost, "/supplier-credentials/tests", `{"supplier_id":"`+f.supplier+`","reason":"all"}`, http.StatusCreated, &all)
	expectSame(t, "every key", []any{len(all.Data), valueOf(all.Skipped)}, []any{0, 1})
	f.send(http.MethodPut, credential+"/enabled", `{"enabled":true,"reason":"resume"}`, http.StatusOK, &c)
	f.send(http.MethodPost, "/supplier-credentials/tests", `{"reason":"all"}`, http.StatusCreated, &all)
	expectSame(t, "every key again", outcomes(all), []string{"0 passed"})
	expectSame(t, "the checks recorded", f.auditCount("credential.test"), "3")
}

// TestCredentialSwitchAndErasure: the switch takes a credential out of this node's routing at once and puts it back; a
// switch already as asked records nothing; erasing clears the key for good, and erasing again answers it as it is.
func TestCredentialSwitchAndErasure(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	credential, acquire := f.routedCredential()
	var c adminapi.SupplierCredential
	f.send(http.MethodPut, credential+"/enabled", `{"enabled":false,"reason":"pause it"}`, http.StatusOK, &c)
	expectSame(t, "disabled", []any{c.Status, routed(t, acquire)}, []any{adminapi.CredentialStatus("disabled"), false})
	key := newKey()
	expectAdmin(t, "switched again", f.change(f.s, http.MethodPut, credential+"/enabled", `{"enabled":false,"reason":"pause it"}`, key), http.StatusOK, "")
	expectAdmin(t, "replayed switch", f.change(f.s, http.MethodPut, credential+"/enabled", `{"enabled":false,"reason":"pause it"}`, key), http.StatusOK, "")
	expectSame(t, "switches recorded", f.auditCount("credential.disable"), "1")
	f.send(http.MethodPut, credential+"/enabled", `{"enabled":true,"reason":"resume"}`, http.StatusOK, &c)
	expectSame(t, "resumed", []any{c.Status, routed(t, acquire)}, []any{adminapi.CredentialStatus("active"), true})

	f.send(http.MethodPost, credential+"/removal", `{"reason":"leaked"}`, http.StatusOK, &c)
	expectSame(t, "erased", []any{c.Status, routed(t, acquire)}, []any{adminapi.CredentialStatus("erased"), false})
	f.send(http.MethodPost, credential+"/removal", `{"reason":"again"}`, http.StatusOK, &c)
	expectAdmin(t, "switch an erased key", f.change(f.s, http.MethodPut, credential+"/enabled", `{"enabled":true,"reason":"r"}`, newKey()), http.StatusConflict, codeConflict)
	expectSame(t, "the erasure", f.auditCount("credential.remove")+"/"+f.text("SELECT count(*)::text FROM suppliers.credentials WHERE ciphertext IS NOT NULL"), "1/0")
}

// TestRemoveInvalidSupplierCredentials: only the named credentials that are still refused are erased; one a check put
// back, and an ID that names none, are kept.
func TestRemoveInvalidSupplierCredentials(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.relay.add("default", "sk-first-refused-0001")
	f.relay.add("default", "sk-second-refus-0002")
	var checks adminapi.CredentialCheckList
	f.send(http.MethodPost, f.path("/keys"), `{"group_id":"`+f.def+`","keys":["sk-first-refused-0001","sk-second-refus-0002"],"max_concurrency":1,"reason":"r"}`, http.StatusCreated, &checks)
	first, second := valueOf(checks.Data[0].CredentialId), valueOf(checks.Data[1].CredentialId)
	f.run("UPDATE suppliers.credentials SET status = 'rejected' WHERE id = '" + first + "'")
	unknown := uuid.NewV7().String()
	body := `{"credential_ids":["` + first + `","` + second + `","` + unknown + `","` + first + `"],"reason":"they were refused"}`
	var result adminapi.InvalidCredentialsRemoved
	key := newKey()
	rec := f.change(f.s, http.MethodPost, "/supplier-credentials/invalid-removals", body, key)
	expectAdmin(t, "remove", rec, http.StatusOK, "")
	decode(t, rec, &result)
	expectSame(t, "removed and kept", []any{result.Removed, len(result.Kept)}, []any{[]string{first}, 2})
	expectSame(t, "the statuses", f.text("SELECT string_agg(status, ',' ORDER BY id) FROM suppliers.credentials"), "erased,active")
	expectAdmin(t, "replay", f.change(f.s, http.MethodPost, "/supplier-credentials/invalid-removals", body, key), http.StatusOK, "")
	expectSame(t, "removals recorded", f.auditCount("credential.remove_invalid"), "1")
}

// credentialIDs are the IDs of a page of credentials.
func credentialIDs(page adminapi.SupplierCredentialList) []string {
	out := make([]string, len(page.Data))
	for i, c := range page.Data {
		out[i] = c.Id
	}
	return out
}

// TestListSupplierCredentials: every platform supplier's credentials, newest first, a page at a time, filtered exactly,
// with this node's load on each.
func TestListSupplierCredentials(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	for i := range 3 {
		f.relay.add("default", fmt.Sprintf("sk-listed-key-000%d", i))
	}
	var checks adminapi.CredentialCheckList
	f.send(http.MethodPost, f.path("/keys"), `{"group_id":"`+f.def+`","keys":["sk-listed-key-0000","sk-listed-key-0001","sk-listed-key-0002"],"max_concurrency":1,"reason":"r"}`, http.StatusCreated, &checks)
	ids := []string{valueOf(checks.Data[0].CredentialId), valueOf(checks.Data[1].CredentialId), valueOf(checks.Data[2].CredentialId)}
	f.run("UPDATE suppliers.credentials SET status = 'rejected' WHERE id = '" + ids[0] + "'")
	var page, rest, refused, direct adminapi.SupplierCredentialList
	decode(t, f.do(http.MethodGet, "/supplier-credentials?limit=2", "", as(f.s)), &page)
	expectSame(t, "the first page", credentialIDs(page), []string{ids[2], ids[1]})
	expectSame(t, "its load and supplier", []any{valueOf(page.Data[0].InFlight), valueOf(page.Data[0].RequestsLastMinute), valueOf(page.Data[0].SupplierId)}, []any{0, 0, f.supplier})
	decode(t, f.do(http.MethodGet, "/supplier-credentials?limit=2&cursor="+valueOf(page.NextCursor), "", as(f.s)), &rest)
	expectSame(t, "the last page", []any{credentialIDs(rest), rest.NextCursor}, []any{[]string{ids[0]}, (*string)(nil)})
	decode(t, f.do(http.MethodGet, "/supplier-credentials?status=rejected&supplier_id="+f.supplier+"&mode=relay", "", as(f.s)), &refused)
	expectSame(t, "refused", credentialIDs(refused), []string{ids[0]})
	decode(t, f.do(http.MethodGet, "/supplier-credentials?mode=direct", "", as(f.s)), &direct)
	expectSame(t, "direct", credentialIDs(direct), []string{})
	// The filter is the supplier's own mode: the same keys are a direct supplier's once the supplier is one.
	f.run("UPDATE suppliers.suppliers SET mode = 'direct' WHERE id = '" + f.supplier + "'")
	decode(t, f.do(http.MethodGet, "/supplier-credentials?mode=direct", "", as(f.s)), &direct)
	decode(t, f.do(http.MethodGet, "/supplier-credentials?mode=relay&supplier_id="+f.supplier, "", as(f.s)), &refused)
	expectSame(t, "by the supplier's mode", []any{credentialIDs(direct), credentialIDs(refused)}, []any{[]string{ids[2], ids[1], ids[0]}, []string{}})
	expectAdmin(t, "a bad cursor", f.do(http.MethodGet, "/supplier-credentials?cursor=nope", "", as(f.s)), http.StatusBadRequest, codeInvalidRequest)
}

// TestAddDirectSupplierKeys: a direct API takes more keys with no group, each checked at the provider as a new direct
// API's are; only the ones that pass are stored, on its line, with the limits given and the models they list. A repeat
// and a key stored already are duplicates the provider is not asked about, a group is refused, a replay answers from
// its claim, and a cloud supplier's keys are not pasted one by one.
func TestAddDirectSupplierKeys(t *testing.T) {
	t.Parallel()
	f, p := newDirectFixture(t)
	var made adminapi.SupplierConnected
	decode(t, f.change(f.s, http.MethodPost, "/suppliers/direct", directBody(p.server.URL, "sk-good-key-0001"), newKey()), &made)
	path := "/suppliers/" + made.Supplier.Id + "/keys"
	asked := len(p.sent())
	body := `{"keys":["sk-good-key-0002","sk-refused-0003","sk-good-key-0001","sk-good-key-0002"],"max_concurrency":2,"requests_per_minute":30,"reason":"more keys"}`
	key := newKey()
	rec := f.change(f.s, http.MethodPost, path, body, key)
	expectAdmin(t, "add", rec, http.StatusCreated, "")
	var checks adminapi.CredentialCheckList
	decode(t, rec, &checks)
	expectSame(t, "the checks", outcomes(checks), []string{"0 passed", "1 failed/credential_rejected", "2 duplicate", "3 duplicate"})
	expectSame(t, "the provider asked", len(p.sent())-asked, 2)
	expectSame(t, "the stored key", f.text(`SELECT max_concurrency || '/' || requests_per_minute || '/' || hint || '/' || array_to_string(allowed_models, ',') || '/' || supplier_group_id::text
		FROM suppliers.credentials WHERE id = '`+valueOf(checks.Data[0].CredentialId)+`'`),
		"2/30/0002/"+directModel+",mystery-model/"+f.text(`SELECT id::text FROM suppliers.supplier_groups WHERE supplier_id = '`+made.Supplier.Id+`'`))
	expectSame(t, "the key's text in the answer", strings.Contains(rec.Body.String(), "good-key"), false)
	replay := f.change(f.s, http.MethodPost, path, body, key)
	expectSame(t, "replay", []any{replay.Code, replay.Header().Get("Idempotency-Replayed"), f.text("SELECT count(*)::text FROM suppliers.credentials"), f.auditCount("credential.paste")},
		[]any{http.StatusCreated, "true", "2", "1"})
	expectSame(t, "the line's endpoints", f.text(`SELECT count(*)::text FROM suppliers.endpoints WHERE supplier_id = '`+made.Supplier.Id+`'`), "1")
	expectAdmin(t, "a group", f.change(f.s, http.MethodPost, path, `{"group_id":"`+uuid.NewV7().String()+`","keys":["sk-good-key-0009"],"max_concurrency":1,"reason":"r"}`, newKey()),
		http.StatusBadRequest, codeInvalidRequest)
	f.run("UPDATE suppliers.suppliers SET mode = 'cloud', api_format = NULL WHERE id = '" + made.Supplier.Id + "'")
	expectAdmin(t, "a cloud supplier", f.change(f.s, http.MethodPost, path, `{"keys":["sk-good-key-0009"],"max_concurrency":1,"reason":"r"}`, newKey()),
		http.StatusConflict, codeConflict)
}
