package declared

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

func acmeSpec(t *testing.T) Spec {
	t.Helper()
	raw, err := os.ReadFile("testdata/acme.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec Spec
	if err := jsonx.UnmarshalKnown(raw, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

func loadAcme(t *testing.T) *Adapter {
	t.Helper()
	adapters, err := LoadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(adapters) != 1 {
		t.Fatalf("%d adapters, want 1", len(adapters))
	}
	a, ok := adapters[0].(*Adapter)
	if !ok {
		t.Fatalf("%T, want *declared.Adapter", adapters[0])
	}
	return a
}

// TestRequiredListingsRejectNull: JSON null decodes into a nil slice without an error. Each required listing must
// reject it, so an abnormal answer cannot withdraw a projection or end a charge scan.
func TestRequiredListingsRejectNull(t *testing.T) {
	t.Parallel()
	a := loadAcme(t)
	cases := []struct {
		name string
		call func() error
	}{
		{"groups", func() error { _, err := parseGroups(a.offer, []byte(`{"data":null}`)); return err }},
		{"models", func() error { _, err := parseModels(a.offer.models, []byte(`{"data":null}`)); return err }},
		{"prices", func() error { _, err := parsePrices(a.offer.prices, []byte(`{"data":{"rates":null}}`)); return err }},
		{"keys", func() error {
			_, err := parseKeys(a.keys.list, a.keys.policy, []byte(`{"data":{"total":0,"items":null}}`))
			return err
		}},
		{"charges", func() error { _, _, err := parseCharges(a.charges, []byte(`{"data":{"items":null}}`)); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if err := c.call(); !errors.Is(err, adapter.ErrUnrecognised) {
				t.Fatalf("error = %v, want ErrUnrecognised", err)
			}
		})
	}
}

// TestLoadDir: the one declaration in testdata compiles to the kind, native unit and buckets it states.
func TestLoadDir(t *testing.T) {
	t.Parallel()
	a := loadAcme(t)
	if a.Kind() != "acme" || a.NativeUnit() != "quota" {
		t.Errorf("kind %q, native unit %q", a.Kind(), a.NativeUnit())
	}
	if len(a.Buckets()) != 2 || a.Buckets()[0].Name != "global" || a.Buckets()[1].Name != "critical" {
		t.Errorf("buckets %+v", a.Buckets())
	}
	if adapters, err := LoadDir(""); err != nil || adapters != nil {
		t.Errorf("an unset directory: %v, %v", adapters, err)
	}
}

// TestLoadDirNamesTheFile: a bad declaration stops the load with its path, so the operator knows which file to fix.
func TestLoadDirNamesTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw, err := os.ReadFile("testdata/acme.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme.json"), raw, 0o600); err != nil { //nolint:gosec // a test temporary directory
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"v":1,"kind":"Broken"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "broken.json") || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("%v, want the file and the kind", err)
	}
}

// TestLoadDirRejectsUnknownMember: the declaration is strict, so a member a later version adds is not silently ignored
// by this node.
func TestLoadDirRejectsUnknownMember(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw, err := os.ReadFile("testdata/acme.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]jsonx.Raw
	if err := jsonx.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["unexpected"] = jsonx.Raw(`true`)
	raw, err = jsonx.MarshalCanonical(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("%v, want the unknown member named", err)
	}
}

// TestLoadDirRejectsDuplicateKind: two files that declare one kind would make the registry pick one silently.
func TestLoadDirRejectsDuplicateKind(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw, err := os.ReadFile("testdata/acme.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.json", "b.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil { //nolint:gosec // a test temporary directory
			t.Fatal(err)
		}
	}
	_, err = LoadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "both declare kind") || !strings.Contains(err.Error(), "a.json") || !strings.Contains(err.Error(), "b.json") {
		t.Fatalf("%v, want both files", err)
	}
}

// TestLoadEnv: the deployment's directory is read through the one environment variable the module adds.
func TestLoadEnv(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile("testdata/acme.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme.json"), raw, 0o600); err != nil { //nolint:gosec // a test temporary directory
		t.Fatal(err)
	}
	t.Setenv(EnvVar, dir)
	adapters, err := LoadEnv()
	if err != nil || len(adapters) != 1 || adapters[0].Kind() != "acme" {
		t.Fatalf("%v, %v", adapters, err)
	}
}

// TestCompileRefusesBrokenDeclarations: every check names what is wrong before the node serves a request. Each case
// changes the one valid declaration in a way the contract cannot accept.
func TestCompileRefusesBrokenDeclarations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(*Spec)
		want   string
	}{
		{"version", func(s *Spec) { s.V = 2 }, "version"},
		{"kind", func(s *Spec) { s.Kind = "Acme" }, "lower-case"},
		{"native unit", func(s *Spec) { s.NativeUnit = "eur" }, "native_unit"},
		{"no global bucket", func(s *Spec) { s.Buckets = s.Buckets[1:] }, "global"},
		{"bad bucket window", func(s *Spec) { s.Buckets[0].Window = "soon" }, "window"},
		{"bad bucket limit", func(s *Spec) { s.Buckets[0].Limit = 0 }, "limit"},
		{"bad bucket per", func(s *Spec) { s.Buckets[0].Per = "user" }, "per"},
		{"unknown field", func(s *Spec) { s.Auth.Fields = []string{"token"} }, "not access_token"},
		{"email alone", func(s *Spec) { s.Auth.Fields = []string{"email"} }, "go together"},
		{"expiry alone", func(s *Spec) { s.Auth.Fields = []string{"access_token_expires_at"} }, "goes with"},
		{"token and login without account", func(s *Spec) { s.Auth.Fields = []string{"access_token"} }, "email and password"},
		{"no token place", func(s *Spec) { s.Auth.Header = "" }, "header or query"},
		{"bad query name", func(s *Spec) { s.Auth.Header, s.Auth.Query, s.Auth.Prefix = "", "api key", "" }, "query name"},
		{"two token places", func(s *Spec) { s.Auth.Query = "token" }, "not both"},
		{"no login for account fields", func(s *Spec) { s.Auth.Login = nil }, "without a login"},
		{"detect with no marker", func(s *Spec) { s.Detect = Detect{Path: "/api/status"} }, "does not claim"},
		{"detect path with a space", func(s *Spec) { s.Detect.Path = "/api/status page" }, "character"},
		{"path with a network reference", func(s *Spec) { s.Account.Path = "//api/me" }, "single /"},
		{"path with a bad escape", func(s *Spec) { s.Account.Path = "/api/%zz" }, "percent escape"},
		{"header with a separator", func(s *Spec) { s.Auth.Header = "Bad(Name" }, "header name"},
		{"captcha without a token", func(s *Spec) { s.Auth.Fields = []string{"email", "password"} }, "access_token"},
		{"models without group", func(s *Spec) { s.Offer.Models.Path = "/api/models" }, "{group}"},
		{"format not in calls", func(s *Spec) { s.Offer.Formats = map[string]string{"chat": "embeddings"} }, "calls.formats"},
		{"unknown price item", func(s *Spec) { s.Offer.Prices.Rates = map[string]string{"bogus": "x"} }, "bogus"},
		{"partial price list is valid", func(s *Spec) { delete(s.Offer.Prices.Rates, "input") }, ""},
		{"zero scale", func(s *Spec) { s.Offer.Prices.Scale = "0" }, "positive"},
		{"skip without ratio", func(s *Spec) { s.Offer.Groups.Ratio = "" }, "ratio is not read"},
		{"page without size", func(s *Spec) { s.Keys.List.Path = "/api/keys?page={page}" }, "go together"},
		{"page size missing", func(s *Spec) { s.Keys.List.PageSize = 0 }, "page_size"},
		{"total without page", func(s *Spec) { s.Keys.List.Path = "/api/keys"; s.Keys.List.PageSize = 0 }, "only for a path"},
		{"create without name", func(s *Spec) { s.Keys.Create.Body = map[string]string{"group_id": "{group}"} }, "name"},
		{"create without list name", func(s *Spec) { s.Keys.List.Name = "" }, "keys.list.name"},
		{"create without list group", func(s *Spec) { s.Keys.List.Group = "" }, "keys.list.group"},
		{"create name with a prefix", func(s *Spec) { s.Keys.Create.Body["name"] = "prefix-{name}" }, "as {name} alone"},
		{"policy and unrestricted", func(s *Spec) { s.Keys.Unrestricted = true }, "not both"},
		{"no policy", func(s *Spec) { s.Keys.Policy = nil }, "declare unrestricted"},
		{"no formats", func(s *Spec) { s.Calls.Formats = nil }, "at least one format"},
		{"unknown format", func(s *Spec) { s.Calls.Formats = []string{"grpc"} }, "protocol format"},
		{"bad charge ref header", func(s *Spec) { s.Calls.ChargeRefHeader = "Bad Header" }, "header name"},
		{"charge ref header with a separator", func(s *Spec) { s.Calls.ChargeRefHeader = "Bad(Name" }, "header name"},
		{"unknown cause", func(s *Spec) { s.Calls.Errors.Rules[0].Cause = "nope" }, "cause"},
		{"unknown scope", func(s *Spec) { s.Calls.Errors.Rules[0].Scope = "world" }, "scope"},
		{"rule is not a refusal", func(s *Spec) {
			s.Calls.Errors.Rules = append(s.Calls.Errors.Rules, ErrorRule{Status: 200, Cause: "bad_request", Scope: "request"})
		}, "refusal status"},
		{"5xx not executed", func(s *Spec) {
			s.Calls.Errors.Rules = append(s.Calls.Errors.Rules, ErrorRule{Status: 503, Cause: "upstream_error", Scope: "endpoint", Executed: "no"})
		}, "5xx"},
		{"code-only rule not executed", func(s *Spec) {
			s.Calls.Errors.Rules = append(s.Calls.Errors.Rules, ErrorRule{Code: "invalid", Cause: "bad_request", Scope: "request", Executed: "no"})
		}, "could match"},
		{"code without field", func(s *Spec) { s.Calls.Errors.Code = "" }, "code is named"},
		{"empty rule", func(s *Spec) { s.Calls.Errors.Rules = []ErrorRule{{Cause: "bad_request", Scope: "request"}} }, "status or a code"},
		{"charge without size", func(s *Spec) { s.Charges.Path = "/api/charges?start={start}&end={end}&page={page}" }, "{size}"},
		{"charge page size", func(s *Spec) { s.Charges.PageSize = 0 }, "page_size"},
		{"charge amount missing", func(s *Spec) { s.Charges.Amount = "" }, "amount"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := acmeSpec(t)
			tc.change(&spec)
			_, err := Compile(spec)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid declaration refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v, want %q", err, tc.want)
			}
		})
	}
}

// TestCompileAcceptsTokenOnly: a declaration may take only a static token, with the expiry the person supplies.
func TestCompileAcceptsTokenOnly(t *testing.T) {
	t.Parallel()
	spec := acmeSpec(t)
	spec.Auth = Auth{Fields: []string{"access_token", "access_token_expires_at"}, Header: "Authorization", Prefix: "Bearer "}
	a, err := Compile(spec)
	if err != nil {
		t.Fatal(err)
	}
	fields := a.LoginFields(false)
	if len(fields) != 2 || fields[0] != adapter.FieldAccessToken || fields[1] != adapter.FieldAccessTokenExpiresAt {
		t.Fatalf("fields %v", fields)
	}
	if got := a.LoginFields(true); len(got) != 2 || got[0] != adapter.FieldAccessToken {
		t.Fatalf("CAPTCHA fields %v", got)
	}
	captcha := loadAcme(t)
	if got := captcha.LoginFields(true); len(got) != 1 || got[0] != adapter.FieldAccessToken {
		t.Fatalf("a CAPTCHA site offers %v, want the access token", got)
	}
}
