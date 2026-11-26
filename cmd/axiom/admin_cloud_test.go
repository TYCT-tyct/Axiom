package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pgtest"
)

// newCloudFixture is the admin on a node that reaches the fake cloud by the platforms' names, with the model the fake
// Bedrock lists in the catalog at the usd reference price.
func newCloudFixture(t *testing.T) (catalogFixture, *fakeCloud) {
	t.Helper()
	c := newFakeCloud(t, "", "")
	base := newAdminFixture(t)
	node := startAdminNodeVia(base.commandLine, base.clock, c.egress(t))
	f := catalogFixture{node, node.owner()}
	f.saveAuthor("anthropic")
	m := f.send(http.MethodPost, "/models", `{"slug":"anthropic/`+directModel+`","version_basis":"dated","price_source":"manual","spec":`+directSpec+`,"reason":"a model"}`, http.StatusCreated)
	m = f.publish(f.addName(f.addName(m, "anthropic/"+directModel), directModel), usd, nil)
	expectAdmin(t, "listing the model", f.setStatus(m, "listed"), http.StatusOK, "")
	return f, c
}

// bedrockBody is Bedrock with the fake's access key, in the regions.
func bedrockBody(regions ...string) string {
	return `{"provider":"bedrock","name":"Harbor Bedrock","credential":{"access_key_id":"` + cloudKeyID + `","secret_access_key":"fake/secret+key"},` +
		`"regions":["` + strings.Join(regions, `","`) + `"],"max_concurrency":3,"requests_per_minute":60,"reason":"a cloud source"}`
}

// regionsOf is each check as "region last4 outcome cause stored".
func regionsOf(checks []adminapi.CredentialCheck) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		cause, region := "", ""
		if c.Cause != nil {
			cause = string(*c.Cause)
		}
		if c.Region != nil {
			region = *c.Region
		}
		out[i] = strings.Join([]string{region, valueOf(c.Last4), string(c.Outcome), cause, boolText(c.CredentialId != nil)}, " ")
	}
	return out
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestCreateCloudSupplier: each region is checked once and only the one that passed is stored, sealed, as its own
// credential with the limits given and the models the region lists; the line has an endpoint at the official price.
// What is not secret about the credential is the supplier's, and no credential text is in the answer, the audit log
// or a table. The same request again is answered from its claim without asking the platform again, and a region whose
// credential another supplier holds is a duplicate.
func TestCreateCloudSupplier(t *testing.T) {
	t.Parallel()
	f, c := newCloudFixture(t)
	body := bedrockBody("us-east-1", "us-west-2", "us-east-1")
	key := newKey()
	rec := f.change(f.s, http.MethodPost, "/suppliers/cloud", body, key)
	expectAdmin(t, "createCloudSupplier", rec, http.StatusCreated, "")
	var made adminapi.SupplierConnected
	decode(t, rec, &made)
	if made.Supplier == nil {
		t.Fatal("no supplier was connected")
	}
	s := made.Supplier
	if got := []any{s.Kind, *s.Mode, s.Status, s.NativeUnit, s.SettlementCurrency, s.BalanceNative, valueOf(s.CloudAccount.AccessKeyLast4)}; !cmp.Equal(got,
		[]any{"bedrock", adminapi.SupplierMode("cloud"), adminapi.SupplierStatus("active"), adminapi.SupplierNativeUnit("usd"), "USD", (*adminapi.Decimal)(nil), "0001"}) {
		t.Errorf("the supplier is %v", got)
	}
	if diff := cmp.Diff([]string{
		"us-east-1 0001 passed  true", "us-west-2 0001 inconclusive not_permitted false", "us-east-1 0001 duplicate  false",
	}, regionsOf(made.Checks)); diff != "" {
		t.Errorf("the checks (-want +got):\n%s", diff)
	}
	f.checkCloudRows(s.Id, rec.Body.String())
	f.checkCloudRetries(c, body, key, made)
	f.checkCloudTest(c, s.Id)
}

// checkCloudRows: the region that passed is stored with the request's limits and the models it lists, the line has an
// endpoint at the official price, and no credential text is in the audit log or the answer.
func (f catalogFixture) checkCloudRows(id, answer string) {
	f.t.Helper()
	t := f.t
	if got := f.text(`SELECT count(*)::text || ' ' || max(region) || ' ' || max(max_concurrency)::text || ' ' || max(requests_per_minute)::text || ' ' ||
		max(hint) || ' ' || max(array_to_string(allowed_models, ',')) || ' ' || max(status) || ' ' || max(source) FROM suppliers.credentials WHERE supplier_id = '` + id + `'`); got !=
		"1 us-east-1 3 60 0001 anthropic.claude-direct-1-v1:0,anthropic.claude-opus-5-5-v1:0 active pasted" {
		t.Errorf("the credentials are %q", got)
	}
	if got := f.text(`SELECT string_agg(e.upstream_name || ' ' || e.cost_state || ' ' || (c.schedule->'rates'->>'output') || ' ' || c.basis, ',')
		FROM suppliers.endpoints e JOIN suppliers.cost_schedules c ON c.id = e.cost_schedule_id WHERE e.supplier_id = '` + id + `'`); got != "anthropic.claude-direct-1-v1:0 known 0.000002 published" {
		t.Errorf("the endpoints are %q, want the listed model's at its official price", got)
	}
	f.expectAudit("supplier.cloud_connect", map[string]string{"reason": "a cloud source", "target_id": id, "detail->>'priced_endpoints'": "1"})
	if leaked := f.text(`SELECT count(*)::text FROM platform.audit_log WHERE detail::text LIKE '%` + cloudKeyID + `%' OR detail::text LIKE '%secret+key%'`); leaked != "0" ||
		strings.Contains(answer, cloudKeyID) || strings.Contains(answer, "secret") {
		t.Error("the credential's text is in the audit log or the answer")
	}
}

// checkCloudRetries: the same request again is answered from its claim, and a region stored for another supplier is
// a duplicate; neither asks the platform anything.
func (f catalogFixture) checkCloudRetries(c *fakeCloud, body, key string, made adminapi.SupplierConnected) {
	f.t.Helper()
	t := f.t
	asked := len(c.sent())
	again := f.change(f.s, http.MethodPost, "/suppliers/cloud", body, key)
	var replay adminapi.SupplierConnected
	decode(t, again, &replay)
	if diff := cmp.Diff(made, replay); diff != "" || again.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("the replay (-first +replay), replayed %q:\n%s", again.Header().Get("Idempotency-Replayed"), diff)
	}
	stored := f.change(f.s, http.MethodPost, "/suppliers/cloud", strings.Replace(bedrockBody("us-east-1"), "Harbor Bedrock", "Harbor 2", 1), newKey())
	expectAdmin(t, "a stored region", stored, http.StatusOK, "")
	var dup adminapi.SupplierConnected
	decode(t, stored, &dup)
	if dup.Supplier != nil || len(dup.Checks) != 1 || dup.Checks[0].Outcome != "duplicate" || len(c.sent()) != asked {
		t.Errorf("a stored region: %+v, the platform asked %d more times", dup, len(c.sent())-asked)
	}
}

// checkCloudTest: a test of the region's credential asks the region, and once the platform refuses the key it is
// rejected.
func (f catalogFixture) checkCloudTest(c *fakeCloud, supplier string) {
	f.t.Helper()
	credential := f.text(`SELECT id::text FROM suppliers.credentials WHERE supplier_id = '` + supplier + `'`)
	path := "/suppliers/" + supplier + "/credentials/" + credential + "/tests"
	var check adminapi.CredentialCheck
	rec := f.change(f.s, http.MethodPost, path, `{"reason":"a check"}`, newKey())
	expectAdmin(f.t, "testSupplierCredential", rec, http.StatusCreated, "")
	decode(f.t, rec, &check)
	if check.Outcome != "passed" || valueOf(check.Region) != "us-east-1" || !strings.HasPrefix(c.sent()[len(c.sent())-1].path, "bedrock.us-east-1.amazonaws.com/") {
		f.t.Errorf("the test is %+v, sent to %s", check, c.sent()[len(c.sent())-1].path)
	}
	c.refuse.Store(true)
	decode(f.t, f.change(f.s, http.MethodPost, path, `{"reason":"a check"}`, newKey()), &check)
	if status := f.text(`SELECT status FROM suppliers.credentials WHERE id = '` + credential + `'`); check.Outcome != "failed" || status != "rejected" {
		f.t.Errorf("a refused key: %+v, the credential %s", check, status)
	}
}

// TestCreateCloudSupplierNothingPassed: when no region passes, nothing is stored and the answer is 200 with the checks.
func TestCreateCloudSupplierNothingPassed(t *testing.T) {
	t.Parallel()
	f, c := newCloudFixture(t)
	c.refuse.Store(true)
	rec := f.change(f.s, http.MethodPost, "/suppliers/cloud", bedrockBody("us-east-1"), newKey())
	expectAdmin(t, "createCloudSupplier", rec, http.StatusOK, "")
	var got adminapi.SupplierConnected
	decode(t, rec, &got)
	if got.Supplier != nil || len(got.Checks) != 1 || got.Checks[0].Outcome != "failed" || valueOf(got.Checks[0].Cause) != "credential_rejected" {
		t.Errorf("the answer is %+v", got)
	}
	if n := f.text("SELECT count(*)::text FROM suppliers.suppliers WHERE mode = 'cloud'") + " " + f.auditCount("supplier.cloud_connect"); n != "0 0" {
		t.Errorf("suppliers and audit entries: %s, want none", n)
	}
}

// TestCreateCloudSupplierRefusals: a platform with an address of its own takes none and Azure OpenAI needs one,
// Vertex AI and Bedrock need regions and Azure OpenAI takes none, each platform takes its own members of the
// credential, and a service account's key must be one; a role without suppliers.manage is forbidden. None of them asks
// the platform anything.
func TestCreateCloudSupplierRefusals(t *testing.T) {
	t.Parallel()
	f, c := newCloudFixture(t)
	azure := `{"provider":"azure","name":"A","origin":"https://harbor.openai.azure.com","credential":{"api_key":"` + cloudResourceKey + `"},"max_concurrency":1,"reason":"r"}`
	for _, tc := range []struct {
		name, body, pointer string
	}{
		{"Bedrock with an origin", strings.Replace(bedrockBody("us-east-1"), `"name"`, `"origin":"https://bedrock.example.com","name"`, 1), "/origin"},
		{"Azure without an origin", strings.Replace(azure, `"origin":"https://harbor.openai.azure.com",`, "", 1), "/origin"},
		{"Azure with regions", strings.Replace(azure, `"max_concurrency"`, `"regions":["eastus"],"max_concurrency"`, 1), "/regions"},
		{"Bedrock without regions", strings.Replace(bedrockBody("x"), `"regions":["x"],`, "", 1), "/regions"},
		{"Bedrock with a key of Azure's", strings.Replace(bedrockBody("us-east-1"), `"secret_access_key"`, `"api_key":"x","secret_access_key"`, 1), "/credential/access_key_id"},
		{"Vertex AI with a file that is not a key", `{"provider":"vertex","name":"V","credential":{"service_account_json":"{}"},"regions":["us-east5"],"max_concurrency":1,"reason":"r"}`, "/credential/service_account_json"},
	} {
		rec := f.change(f.s, http.MethodPost, "/suppliers/cloud", tc.body, newKey())
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", tc.name, rec.Code, rec.Body)
			continue
		}
		if fields := errorDetailOf(t, rec).Fields; fields == nil || len(*fields) != 1 || (*fields)[0].Pointer != tc.pointer {
			t.Errorf("%s: %s, want a field at %s", tc.name, rec.Body, tc.pointer)
		}
	}
	f.addUser("viewer@example.com", "viewer")
	expectAdmin(t, "a viewer", f.change(f.verified("viewer@example.com"), http.MethodPost, "/suppliers/cloud", azure, newKey()), http.StatusForbidden, codeForbidden)
	if n := len(c.sent()); n != 0 {
		t.Errorf("the platform was asked %d times by requests that were refused", n)
	}
}

// TestReplaceSupplierCloudCredential: a new key is checked in the regions given; a region it passed in keeps its row
// and limits, one left out is erased, one that failed is not stored, and one added back takes the limits given. The
// regions absent are the ones served now. Only a cloud supplier's credential is replaced (409), of a supplier there is
// (404), and the same request again is answered from its claim.
func TestReplaceSupplierCloudCredential(t *testing.T) {
	t.Parallel()
	f, _ := newCloudFixture(t)
	var made adminapi.SupplierConnected
	decode(t, f.change(f.s, http.MethodPost, "/suppliers/cloud", bedrockBody("us-east-1", "us-east-2"), newKey()), &made)
	id := made.Supplier.Id
	path := "/suppliers/" + id + "/cloud-credential"
	replace := `{"credential":{"access_key_id":"AKIAFAKEHARBOR0002","secret_access_key":"fake/second"},"regions":["us-east-2","us-west-2"],"max_concurrency":7,"reason":"a new key"}`
	key := newKey()
	rec := f.change(f.s, http.MethodPut, path, replace, key)
	expectAdmin(t, "replaceSupplierCloudCredential", rec, http.StatusOK, "")
	var changed adminapi.SupplierConnected
	decode(t, rec, &changed)
	if diff := cmp.Diff([]string{"us-east-2 0002 passed  true", "us-west-2 0002 inconclusive not_permitted false"}, regionsOf(changed.Checks)); diff != "" || changed.Supplier == nil ||
		valueOf(changed.Supplier.CloudAccount.AccessKeyLast4) != "0002" {
		t.Errorf("the checks (-want +got):\n%s\nthe supplier %+v", diff, changed.Supplier)
	}
	rows := `SELECT string_agg(region || ' ' || status || ' ' || max_concurrency::text || ' ' || hint, ',' ORDER BY region, hint) FROM suppliers.credentials WHERE supplier_id = '` + id + `'`
	if got := f.text(rows); got != "us-east-1 erased 3 0001,us-east-2 erased 3 0001,us-east-2 active 3 0002" {
		t.Errorf("the credentials are %q, want the old key's erased and the region it passed in at its limits", got)
	}
	f.expectAudit("supplier.cloud_credential_replace", map[string]string{"detail->'erased_regions'->>0": "us-east-1", "reason": "a new key"})
	again := f.change(f.s, http.MethodPut, path, replace, key)
	if again.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("the replay is %d %s", again.Code, again.Body)
	}
	back := `{"credential":{"access_key_id":"AKIAFAKEHARBOR0002","secret_access_key":"fake/second"},"regions":["us-east-1","us-east-2"],"max_concurrency":7,"reason":"back"}`
	expectAdmin(t, "a region added back", f.change(f.s, http.MethodPut, path, back, newKey()), http.StatusOK, "")
	serving := `SELECT string_agg(id::text || ' ' || region || ' ' || max_concurrency::text || ' ' || hint, ',' ORDER BY region) FROM suppliers.credentials WHERE supplier_id = '` + id + `' AND status = 'active'`
	before := f.text(serving)
	if got := f.text(rows); !strings.HasSuffix(got, "us-east-1 active 7 0002,us-east-2 erased 3 0001,us-east-2 active 3 0002") {
		t.Errorf("after a region is added back, the credentials are %q", got)
	}
	// The same key left out of a region and given it again is sealed again in its own row, at the limits given.
	expectAdmin(t, "a region left out", f.change(f.s, http.MethodPut, path, strings.Replace(back, `"us-east-1",`, "", 1), newKey()), http.StatusOK, "")
	expectAdmin(t, "the region again", f.change(f.s, http.MethodPut, path, strings.Replace(back, `"max_concurrency":7`, `"max_concurrency":5`, 1), newKey()), http.StatusOK, "")
	if after := f.text(serving); after != strings.Replace(before, "us-east-1 7", "us-east-1 5", 1) {
		t.Errorf("the rows serving are %q, were %q", after, before)
	}
	same := `{"credential":{"access_key_id":"AKIAFAKEHARBOR0002","secret_access_key":"fake/second"},"reason":"the same regions"}`
	decode(t, f.change(f.s, http.MethodPut, path, same, newKey()), &changed)
	if diff := cmp.Diff([]string{"us-east-1 0002 passed  true", "us-east-2 0002 passed  true"}, regionsOf(changed.Checks)); diff != "" {
		t.Errorf("the regions served now (-want +got):\n%s", diff)
	}
	var azure adminapi.SupplierConnected
	decode(t, f.change(f.s, http.MethodPost, "/suppliers/cloud", `{"provider":"azure","name":"A","origin":"https://harbor.openai.azure.com","credential":{"api_key":"`+cloudResourceKey+`"},"max_concurrency":1,"reason":"r"}`, newKey()), &azure)
	azurePath := "/suppliers/" + azure.Supplier.Id + "/cloud-credential"
	azureKey := `{"credential":{"api_key":"` + cloudResourceKey + `"},"reason":"r"}`
	expectAdmin(t, "Azure with regions", f.change(f.s, http.MethodPut, azurePath, strings.Replace(azureKey, `"reason"`, `"regions":["eastus"],"reason"`, 1), newKey()), http.StatusBadRequest, codeInvalidRequest)
	if err := pgtest.Run(t.Context(), f.database.Superuser, "UPDATE suppliers.suppliers SET mode = 'direct', cloud_account = NULL WHERE id = '"+azure.Supplier.Id+"'"); err != nil {
		t.Fatal(err)
	}
	expectAdmin(t, "a direct API", f.change(f.s, http.MethodPut, azurePath, azureKey, newKey()), http.StatusConflict, codeConflict)
	expectAdmin(t, "no supplier", f.change(f.s, http.MethodPut, "/suppliers/01a2163e-0000-7000-8000-000000000000/cloud-credential", same, newKey()), http.StatusNotFound, codeNotFound)
}
