package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const (
	someSite     = "/sites/site_00000000000000000000000000/settings"
	someAccount  = "/accounts/acct_00000000000000000000000000"
	someSupplier = "/suppliers/0198c0a8-0000-7000-8000-000000000001"
	someModel    = "/models/0198c0a8-0000-7000-8000-000000000002"
	someRule     = "/pricing/rules/0198c0a8-0000-7000-8000-000000000003"
	someMerchant = "/payments/merchants/mch_00000000000000000000000000"
)

// adminOperation is an operation of the admin's API as the tests state it, by hand and apart from the contract, which
// TestAdminOperationsAreTheContracts holds it to: what it needs and what it changes.
type adminOperation struct {
	name, method, path, body string
	// permission is what the session needs on the platform; empty for the Session tag's, which need none.
	permission string
	// idempotent operations take an Idempotency-Key.
	idempotent bool
}

func (o adminOperation) changes() bool { return o.method != http.MethodGet }

// adminOperations are the operations that need a session and a permission on the platform: all but the Session tag's.
var adminOperations = []adminOperation{
	{"listAdminCalls", http.MethodGet, "/calls", "", "calls.search", false},
	{"getAdminCallTrace", http.MethodGet, "/calls/0198c0a8-0000-7000-8000-000000000001/trace", "", "calls.inspect", false},
	{"listAuditEntries", http.MethodGet, "/audit", "", "audit.view", false},
	{"getAuditEntry", http.MethodGet, "/audit/0198c0a8-0000-7000-8000-000000000001", "", "audit.view", false},
	{"getSystemSettings", http.MethodGet, strings.Replace(someSite, "/settings", "/system-settings", 1), "", "sites.manage", false},
	{"setSystemSettings", http.MethodPut, strings.Replace(someSite, "/settings", "/system-settings", 1), `{"settings":{"name":"Test","registration":"closed"},"reason":"r"}`, "sites.manage", true},
	{"getPaymentSettings", http.MethodGet, strings.Replace(someSite, "/settings", "/system-settings/payments", 1), "", "sites.manage", false},
	{"setPaymentSettings", http.MethodPut, strings.Replace(someSite, "/settings", "/system-settings/payments", 1), `{"settings":{"open":false},"reason":"r"}`, "sites.manage", true},
	{"getMailSettings", http.MethodGet, "/settings/mail", "", "sites.manage", false},
	{"setMailSettings", http.MethodPut, "/settings/mail", `{"settings":{"enabled":false},"reason":"r"}`, "sites.manage", true},
	{"testMailSettings", http.MethodPost, "/settings/mail/tests", `{"to":"test@example.com","reason":"r"}`, "sites.manage", true},
	{"listMerchants", http.MethodGet, "/payments/merchants?site_id=site_00000000000000000000000000", "", "sites.manage", false},
	{"getMerchant", http.MethodGet, someMerchant, "", "sites.manage", false},
	{"createMerchant", http.MethodPost, "/payments/merchants", `{"site_id":"site_00000000000000000000000000","merchant":{},"reason":"r"}`, "sites.manage", true},
	{"updateMerchant", http.MethodPut, someMerchant, `{"merchant":{},"reason":"r"}`, "sites.manage", true},
	{"disableMerchant", http.MethodPost, someMerchant + "/disable", `{"reason":"r"}`, "sites.manage", true},
	{"testMerchantSettings", http.MethodPost, someMerchant + "/tests", `{"reason":"r"}`, "sites.manage", true},
	{"getRateLimit", http.MethodGet, "/rate-limits/account/acct_00000000000000000000000000", "", "sites.manage", false},
	{"setRateLimit", http.MethodPut, "/rate-limits/account/acct_00000000000000000000000000", `{"reason":"r"}`, "sites.manage", true},
	{"publishAnnouncement", http.MethodPost, "/announcements", `{"site_id":"site_00000000000000000000000000","title":"Maintenance","body":"Notice","starts_at":"2026-10-29T00:00:00Z","ends_at":"2026-10-30T00:00:00Z","reason":"test"}`, "sites.manage", true},
	{"withdrawAnnouncement", http.MethodPost, "/announcements/00000000-0000-0000-0000-000000000000/withdraw", `{"reason":"test"}`, "sites.manage", true},
	{"listPaymentDiscrepancies", http.MethodGet, "/payments/discrepancies", "", "sites.manage", false},
	{"getPaymentDiscrepancy", http.MethodGet, "/payments/discrepancies/0198c0a8-0000-7000-8000-000000000001", "", "sites.manage", false},
	{"investigatePaymentDiscrepancy", http.MethodPost, "/payments/discrepancies/0198c0a8-0000-7000-8000-000000000001/investigate", `{}`, "sites.manage", true},
	{"getAlertConfiguration", http.MethodGet, "/alerts/configuration", "", "sites.manage", false},
	{"setAlertConfiguration", http.MethodPut, "/alerts/configuration", `{}`, "sites.manage", true},
	{"listAlertEvents", http.MethodGet, "/alerts/events", "", "sites.manage", false},
	{"sendAlertTest", http.MethodPost, "/alerts/tests", `{"channel":"ops","reason":"r"}`, "sites.manage", true},
	{"rotateAlertWebhookSecret", http.MethodPut, "/alerts/channels/hook/secret", `{"rev":1,"secret":"receiver test signing value 32 bytes","reason":"r"}`, "sites.manage", true},
	{"resolveAlertEvent", http.MethodPost, "/alerts/events/0198c0a8-0000-7000-8000-000000000001/resolve", `{"reason":"r"}`, "sites.manage", true},
	{"listExchangeRates", http.MethodGet, "/pricing/exchange-rates", "", "pricing.view", false},
	{"setExchangeRate", http.MethodPost, "/pricing/exchange-rates", `{}`, "pricing.manage", true},
	{"exchangeRateHistory", http.MethodGet, "/pricing/exchange-rates/USD/CNY", "", "pricing.view", false},
	{"getSiteSettings", http.MethodGet, someSite, "", "sites.manage", false},
	{"updateSiteSettings", http.MethodPut, someSite, `{"settings":{},"reason":"r"}`, "sites.manage", true},
	{"getSiteLogoAdmin", http.MethodGet, strings.Replace(someSite, "/settings", "/logo", 1), "", "sites.manage", false},
	{"updateSiteLogo", http.MethodPut, strings.Replace(someSite, "/settings", "/logo", 1), `{"reason":"r"}`, "sites.manage", true},
	{"getSiteKeyLimits", http.MethodGet, strings.Replace(someSite, "/settings", "/key-limits", 1), "", "sites.manage", false},
	{"updateSiteKeyLimits", http.MethodPut, strings.Replace(someSite, "/settings", "/key-limits", 1), `{"limits":{},"reason":"r"}`, "sites.manage", true},
	{"getSiteSignIn", http.MethodGet, strings.Replace(someSite, "/settings", "/sign-in", 1), "", "sites.manage", false},
	{"updateSiteSignIn", http.MethodPut, strings.Replace(someSite, "/settings", "/sign-in", 1), `{"login_domain":"main.example","passkeys":false,"reason":"r"}`, "sites.manage", true},
	{"setSiteOAuthProvider", http.MethodPut, strings.Replace(someSite, "/settings", "/sign-in/providers/github", 1), `{"client_id":"id","client_secret":"secret","reason":"r"}`, "sites.manage", true},
	{"deleteSiteOAuthProvider", http.MethodPost, strings.Replace(someSite, "/settings", "/sign-in/providers/github/delete", 1), `{"reason":"r"}`, "sites.manage", true},
	{"getSiteLoginLimits", http.MethodGet, strings.Replace(someSite, "/settings", "/login-limits", 1), "", "sites.manage", false},
	{"updateSiteLoginLimits", http.MethodPut, strings.Replace(someSite, "/settings", "/login-limits", 1), `{"limits":{},"reason":"r"}`, "sites.manage", true},
	{"getSiteRegistration", http.MethodGet, strings.Replace(someSite, "/settings", "/registration", 1), "", "sites.manage", false},
	{"updateSiteRegistration", http.MethodPut, strings.Replace(someSite, "/settings", "/registration", 1), `{"policy":{"mode":"invite","human_check":true,"human_check_bits":18,"domain_burst":5,"domain_per_hour":30,"domain_allow":[],"domain_deny":[]},"reason":"r"}`, "sites.manage", true},
	{"getSiteBrand", http.MethodGet, strings.Replace(someSite, "/settings", "/brand", 1), "", "sites.manage", false},
	{"updateSiteBrand", http.MethodPut, strings.Replace(someSite, "/settings", "/brand", 1), `{"brand_color":"#335ddf","reason":"r"}`, "sites.manage", true},
	{"listLegalDocuments", http.MethodGet, strings.Replace(someSite, "/settings", "/legal-documents", 1), "", "sites.manage", false},
	{"publishLegalDocument", http.MethodPost, strings.Replace(someSite, "/settings", "/legal-documents", 1), `{"kind":"terms","body":"Text.","reason":"r"}`, "sites.manage", true},
	{"setAccountSuspension", http.MethodPut, someAccount + "/suspension", `{"suspended":true,"reason":"r"}`, "users.manage", true},
	{"setAccountTier", http.MethodPut, someAccount + "/tier", `{"tier_id":"tier_00000000000000000000000000","reason":"r"}`, "users.manage", true},
	{"setUserSignIn", http.MethodPut, "/users/user_00000000000000000000000000/sign-in", `{"disabled":true,"reason":"r"}`, "users.manage", true},
	{"setPlatformGrant", http.MethodPut, "/users/user_00000000000000000000000000/platform-grants/admin", `{"revoked":true,"reason":"r"}`, "platform_grants.manage", true},
	{"listUserPlatformGrants", http.MethodGet, "/users/user_00000000000000000000000000/platform-grants", "", "platform_grants.manage", false},
	{"listTiers", http.MethodGet, "/tiers", "", "users.view", false},
	{"listUsers", http.MethodGet, "/users", "", "users.view", false},
	{"getAccount", http.MethodGet, someAccount, "", "users.view", false},
	{"updateAccountKeyLimits", http.MethodPut, someAccount + "/key-limits", `{"limits":{},"reason":"r"}`, "sites.manage", true},
	{"grantCredit", http.MethodPost, someAccount + "/credit-grants", `{"amount":{"amount":"1","currency":"USD"},"reason":"r"}`, "credits.grant", true},
	{"listSuppliers", http.MethodGet, "/suppliers", "", "suppliers.view", false},
	{"createSupplier", http.MethodPost, "/suppliers", `{}`, "suppliers.manage", true},
	{"detectSupplier", http.MethodPost, "/suppliers/detections", `{"origin":"https://api.example.com"}`, "suppliers.manage", false},
	{"testSupplierEndpoint", http.MethodPost, "/suppliers/endpoint-tests", `{"endpoint_id":"0198c0a8-0000-7000-8000-000000000001","format":"chat","reason":"r"}`, "suppliers.manage", true},
	{"getSupplier", http.MethodGet, someSupplier, "", "suppliers.view", false},
	{"setSupplierStatus", http.MethodPut, someSupplier + "/status", `{"status":"paused","reason":"r"}`, "suppliers.manage", true},
	{"removeSupplier", http.MethodPost, someSupplier + "/removal", `{"reason":"r"}`, "suppliers.manage", true},
	{"replaceSupplierLogin", http.MethodPut, someSupplier + "/login", `{}`, "suppliers.manage", true},
	{"confirmSupplierUnitValue", http.MethodPut, someSupplier + "/unit-value", `{"value":"1","reason":"r"}`, "suppliers.manage", true},
	{"selectSupplierGroup", http.MethodPut, someSupplier + "/groups/0198c0a8-0000-7000-8000-000000000004", `{"selected":true,"reason":"r"}`, "suppliers.manage", true},
	{"bindSupplierKey", http.MethodPost, someSupplier + "/credentials", `{}`, "suppliers.manage", true},
	{"setSupplierCredentialLimits", http.MethodPut, someSupplier + "/credentials/0198c0a8-0000-7000-8000-000000000005/limits", `{"max_concurrency":8,"requests_per_minute":null,"reason":"r"}`, "suppliers.manage", true},
	{"listPendingMappings", http.MethodGet, "/pending-mappings", "", "suppliers.view", false},
	{"mapSupplierModel", http.MethodPost, someSupplier + "/model-mappings", `{}`, "suppliers.manage", true},
	{"listAuthors", http.MethodGet, "/authors", "", "catalog.view", false},
	{"saveAuthor", http.MethodPut, "/authors/acme", `{"name":"Acme","reason":"r"}`, "catalog.manage", true},
	{"listModels", http.MethodGet, "/models", "", "catalog.view", false},
	{"createModel", http.MethodPost, "/models", `{}`, "catalog.manage", true},
	{"getModel", http.MethodGet, someModel, "", "catalog.view", false},
	{"updateModelSpec", http.MethodPut, someModel + "/spec", `{}`, "catalog.manage", true},
	{"addModelName", http.MethodPost, someModel + "/names", `{}`, "catalog.manage", true},
	{"setModelStatus", http.MethodPut, someModel + "/status", `{}`, "catalog.manage", true},
	{"publishReferencePrice", http.MethodPost, someModel + "/prices", `{}`, "catalog.manage", true},
	{"selectPriceSource", http.MethodPut, someModel + "/price-source", `{}`, "catalog.manage", true},
	{"listPricingRules", http.MethodGet, "/pricing/rules", "", "pricing.view", false},
	{"createPricingRule", http.MethodPost, "/pricing/rules", `{}`, "pricing.manage", true},
	{"updatePricingRule", http.MethodPut, someRule, `{}`, "pricing.manage", true},
	{"listPricePublications", http.MethodGet, "/pricing/publications", "", "pricing.view", false},
	{"publishSalePrice", http.MethodPost, "/pricing/publications/0198c0a8-0000-7000-8000-000000000001", `{}`, "pricing.manage", true},
	{"updatePricePolicy", http.MethodPut, "/pricing/policy", `{}`, "pricing.manage", true},
	{"previewPricing", http.MethodPost, "/pricing/previews", `{}`, "pricing.view", false},
}

// sessionOperations of the admin: the Session tag's, which need a session of a level and no permission.
var adminSessionOperations = []adminOperation{
	{"verifyMfa", http.MethodPost, "/auth/mfa", `{"code":"123456"}`, "", false},
	{"signOut", http.MethodPost, "/auth/sign-out", "", "", false},
	{"getSession", http.MethodGet, "/session", "", "", false},
	{"beginTotpSetup", http.MethodPost, "/auth/totp/setup", "", "", false},
	{"confirmTotpSetup", http.MethodPost, "/auth/totp/confirm", `{"secret":"JBSWY3DPEHPK3PXP","password":"p","code":"123456"}`, "", false},
	{"regenerateRecoveryCodes", http.MethodPost, "/auth/recovery-codes", `{"code":"123456"}`, "", false},
}

func loadedAdminContract(t testing.TB) *contract {
	t.Helper()
	c, err := adminContract()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// operationOf is the rules of the contract's operation called name.
func operationOf(c *contract, name string) *rules {
	for _, r := range c.byPattern {
		if strings.EqualFold(r.operation, name) {
			return r
		}
	}
	return nil
}

// TestAdminOperationsAreTheContracts: each operation these tests exercise is the contract's, with the permission, the
// idempotency key and the data change the tests assume of it, so no operation is assumed wrong.
func TestAdminOperationsAreTheContracts(t *testing.T) {
	t.Parallel()
	c := loadedAdminContract(t)
	for _, op := range slices.Concat(adminOperations, adminSessionOperations) {
		found := operationOf(c, op.name)
		if found == nil {
			t.Errorf("%s is not an operation of the contract", op.name)
			continue
		}
		permission := ""
		if found.permission != nil {
			permission = found.permission.Name()
		}
		hasKey := slices.ContainsFunc(found.params, func(p *openapi3.Parameter) bool { return p.Name == "Idempotency-Key" })
		if !found.session || permission != op.permission || hasKey != op.idempotent || found.changes != op.changes() ||
			op.permission != "" && found.level != levelMFA {
			t.Errorf("%s: the contract says session=%v level=%q permission=%q idempotent=%v changes=%v, the tests assume %+v",
				op.name, found.session, found.level, permission, hasKey, found.changes, op)
		}
	}
}

// TestAdminTestsCoverTheContract: no operation of the contract is left out of the refusals below, which run over the
// tables above.
func TestAdminTestsCoverTheContract(t *testing.T) {
	t.Parallel()
	covered := map[string]bool{ // public: they need no session to refuse
		"getversion": true, "signin": true, "getadminsite": true, "getadminsitelogo": true,
	}
	for _, op := range slices.Concat(adminOperations, adminSessionOperations) {
		covered[strings.ToLower(op.name)] = true
	}
	for _, r := range loadedAdminContract(t).byPattern {
		if !covered[strings.ToLower(r.operation)] {
			t.Errorf("the contract's %s is in no test table", r.operation)
		}
	}
}

// TestAdminOperationsNeedASession: every operation that names a session refuses a request without one, or with one
// that was never issued, as unauthenticated, and says nothing about what it would have done.
func TestAdminOperationsNeedASession(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	forged := session{cookie: &http.Cookie{Name: "__Host-axiom_session", Value: strings.Repeat("A", 43)}, csrf: "x"} // #nosec G124 -- a cookie only a test sends
	for _, op := range slices.Concat(adminOperations, adminSessionOperations) {
		var mods []mod
		if op.idempotent {
			mods = append(mods, idempotent(newKey()))
		}
		expectAdmin(t, op.name+" without a session", f.do(op.method, op.path, op.body, mods...), http.StatusUnauthorized, codeUnauthenticated)
		expectAdmin(t, op.name+" with a forged session", f.do(op.method, op.path, op.body, append(mods, as(forged))...), http.StatusUnauthorized, codeUnauthenticated)
	}
}

// TestAdminOperationsNeedTwoStepVerification: a session that proved only the password is refused by every operation of
// the platform with the code that says why, before the body is looked at.
func TestAdminOperationsNeedTwoStepVerification(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.signIn(ownerEmail)
	for _, op := range adminOperations {
		mods := []mod{as(s)}
		if op.idempotent {
			mods = append(mods, idempotent(newKey()))
		}
		expectAdmin(t, op.name+" with a password session", f.do(op.method, op.path, "not json", mods...), http.StatusForbidden, codeMfaRequired)
	}
}

// TestAdminOperationsNeedTheirPermission: a session that passed two-step verification but holds no grant on the
// platform, or a role without the permission, is refused by every operation as forbidden, whether or not what it names
// exists. The role with the permission is the pair of this test, in the tests of each operation.
func TestAdminOperationsNeedTheirPermission(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	f.addUser("nobody@example.com", "")
	f.addUser("member@example.com", "owner") // a role of accounts, with none of the admin's permissions
	for _, email := range []string{"nobody@example.com", "member@example.com"} {
		s := f.verified(email)
		for _, op := range adminOperations {
			mods := []mod{as(s)}
			if op.idempotent {
				mods = append(mods, idempotent(newKey()))
			}
			expectAdmin(t, email+" "+op.name, f.do(op.method, op.path, op.body, mods...), http.StatusForbidden, codeForbidden)
		}
	}
}

// TestAdminPermissionsAreOneToOne: a role holding only one permission passes exactly the operations that name it.
func TestAdminPermissionsAreOneToOne(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	f.addUser("viewer@example.com", "viewer")
	s := f.verified("viewer@example.com")
	held := map[string]bool{"users.view": true, "suppliers.view": true, "catalog.view": true, "pricing.view": true}
	for _, op := range adminOperations {
		mods := []mod{as(s)}
		if op.idempotent {
			mods = append(mods, idempotent(newKey()))
		}
		rec := f.do(op.method, op.path, op.body, mods...)
		if held[op.permission] == (rec.Code == http.StatusForbidden) {
			t.Errorf("the viewer on %s (%s): %d %s", op.name, op.permission, rec.Code, rec.Body)
		}
	}
}

// TestAdminChangesNeedTheOriginAsJSONWithTheToken: a request that changes data and comes without the admin's Origin, from
// another or from "null", as something other than JSON, or without or with another CSRF token, is refused as
// origin_rejected, by every operation, before it is looked at; a read needs none of these.
func TestAdminChangesNeedTheOriginAsJSONWithTheToken(t *testing.T) {
	t.Parallel()
	refusals := map[string]mod{
		"no origin":          func(r *http.Request) { r.Header.Del("Origin") },
		"another origin":     header("Origin", "https://evil.example"),
		"the console origin": header("Origin", "https://"+consoleHost),
		"the origin of http": header("Origin", "http://"+adminHost),
		"a null origin":      header("Origin", "null"),
		"a form":             header("Content-Type", "application/x-www-form-urlencoded"),
		"no content type":    func(r *http.Request) { r.Header.Del("Content-Type") },
		"no token":           func(r *http.Request) { r.Header.Del("X-Csrf-Token") },
		"another token":      header("X-Csrf-Token", "not-the-token"),
	}
	f := newAdminFixture(t)
	s := f.owner()
	for _, op := range slices.Concat(adminOperations, adminSessionOperations) {
		if !op.changes() || op.name == "verifyMfa" { // a pending session's own request: below
			continue
		}
		for name, refusal := range refusals {
			mods := []mod{as(s)}
			if op.idempotent {
				mods = append(mods, idempotent(newKey()))
			}
			expectAdmin(t, op.name+" with "+name, f.do(op.method, op.path, op.body, append(mods, refusal)...), http.StatusForbidden, codeOriginRejected)
		}
	}
	pending := f.signIn(ownerEmail) // the owner has two-step verification now, so this session still waits for it
	for _, name := range []string{"no origin", "another origin", "no token"} {
		rec := f.do(http.MethodPost, "/auth/mfa", `{"code":"123456"}`, as(pending), refusals[name])
		expectAdmin(t, "verifyMfa with "+name, rec, http.StatusForbidden, codeOriginRejected)
	}
	for name, refusal := range map[string]mod{
		"another origin": header("Origin", "https://evil.example"),
		"no origin":      func(r *http.Request) { r.Header.Del("Origin") },
		"a form":         header("Content-Type", "text/plain"),
	} {
		expectAdmin(t, "sign-in with "+name, f.do(http.MethodPost, "/auth/sign-in", `{"email":"`+ownerEmail+`","password":"`+ownerPassword+`"}`, refusal), http.StatusForbidden, codeOriginRejected)
	}
	rec := f.do(http.MethodGet, "/session", "", as(s), header("Origin", "https://evil.example"))
	expectAdmin(t, "a read from another origin", rec, http.StatusOK, "")
	for name := range rec.Header() {
		if strings.HasPrefix(name, "Access-Control-") {
			t.Errorf("the API sent the CORS header %s", name)
		}
	}
}

// TestAdminWritesNeedAnIdempotencyKey: an operation that takes a key refuses a request without one or with one that
// is not a UUID in lower case, as a request that breaks the contract naming the header, after the session and its
// permission are checked.
func TestAdminWritesNeedAnIdempotencyKey(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	for _, op := range adminOperations {
		if !op.idempotent {
			continue
		}
		for name, mods := range map[string][]mod{
			"no key":        {as(s)},
			"a text":        {as(s), idempotent("not-a-uuid")},
			"an upper case": {as(s), idempotent(strings.ToUpper(newKey()))},
		} {
			rec := f.do(op.method, op.path, op.body, mods...)
			expectAdmin(t, op.name+" with "+name, rec, http.StatusBadRequest, codeInvalidRequest)
			fields := errorDetailOf(t, rec).Fields
			if fields == nil || len(*fields) == 0 || (*fields)[0].Pointer != "Idempotency-Key" {
				t.Errorf("%s with %s: fields %v, want the header named", op.name, name, fields)
			}
		}
	}
}

// TestAdminDomainsAreThePlace: the admin's routes answer on an admin domain only; a console domain, an unknown host or
// the model API's domain is no admin and gets the same 404 whatever the operation.
func TestAdminDomainsAreThePlace(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	for _, host := range []string{consoleHost, "api.main.example", "unknown.example"} {
		expectAdmin(t, "getSession at "+host, f.do(http.MethodGet, "/session", "", as(s), atHost(host)), http.StatusNotFound, codeNotFound)
		expectAdmin(t, "listUsers at "+host, f.do(http.MethodGet, "/users", "", as(s), atHost(host)), http.StatusNotFound, codeNotFound)
	}
	expectAdmin(t, "a path the contract lacks", f.do(http.MethodGet, "/nothing-here", "", as(s)), http.StatusNotFound, codeNotFound)
}

// TestAdminPermissionsAreCheckedEveryRequest: taking a permission from the role takes effect on the next request of a
// node that has reloaded the roles, for exactly the operations that need it.
func TestAdminPermissionsAreCheckedEveryRequest(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	expectAdmin(t, "listUsers before", f.do(http.MethodGet, "/users", "", as(s)), http.StatusOK, "")
	f.run(`ALTER TABLE accounts.roles NO FORCE ROW LEVEL SECURITY`,
		`UPDATE accounts.roles SET permissions = array_remove(permissions, 'users.view') WHERE name = 'admin'`,
		`ALTER TABLE accounts.roles FORCE ROW LEVEL SECURITY`)
	f = f.restarted()
	expectAdmin(t, "listUsers without users.view", f.do(http.MethodGet, "/users", "", as(s)), http.StatusForbidden, codeForbidden)
	expectAdmin(t, "listAuthors still allowed", f.do(http.MethodGet, "/authors", "", as(s)), http.StatusOK, "")
}

// TestAGrantRevokedStopsTheNextRequest: the platform grants are read from the database on every request, so revoking
// one ends the operator's access at once on every node.
func TestAGrantRevokedStopsTheNextRequest(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	expectAdmin(t, "before", f.do(http.MethodGet, "/users", "", as(s)), http.StatusOK, "")
	f.run(`UPDATE accounts.platform_grants SET revoked_at = now()`)
	expectAdmin(t, "after", f.do(http.MethodGet, "/users", "", as(s)), http.StatusForbidden, codeForbidden)
}
