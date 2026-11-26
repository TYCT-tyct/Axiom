package vertex

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// testKey is one RSA key for the package's tests, made once: a key in the repository would look like a real one.
var testKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

// keyFile is a service account's JSON key file as Google writes one, with the members Axiom does not read.
func keyFile(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(testKey())
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	b, err := jsonx.MarshalCanonical(map[string]string{ //nolint:gosec // a key made by the test itself
		"type": "service_account", "project_id": "harbor-prod", "private_key_id": "0123456789abcdef0123456789abcdef0123cafe",
		"private_key": pemText, "client_email": "relay@harbor-prod.iam.gserviceaccount.com", "token_uri": "https://evil.example/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readKey(t *testing.T) adapter.CloudCredential {
	t.Helper()
	c, err := New().ReadCredential(adapter.CloudInput{ServiceAccountJSON: keyFile(t)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestReadCredential: the project, account and key are read, the key's last four characters shown, and the file's
// other members, the token address among them, are not kept; a member of another platform, or a file without an RSA
// key, is refused.
func TestReadCredential(t *testing.T) {
	t.Parallel()
	c := readKey(t)
	if c.Account != (adapter.CloudAccount{ProjectID: "harbor-prod", ClientEmail: "relay@harbor-prod.iam.gserviceaccount.com"}) || c.Last4 != "cafe" {
		t.Errorf("read %+v, last4 %s", c.Account, c.Last4)
	}
	if sealed := c.Key.Reveal(); strings.Contains(sealed, "token_uri") || strings.Contains(sealed, "evil") {
		t.Errorf("the sealed text keeps the file's token address: %s", sealed)
	}
	for _, in := range []adapter.CloudInput{
		{ServiceAccountJSON: keyFile(t), APIKey: "extra"},
		{ServiceAccountJSON: `{"project_id":"p","client_email":"a@b","private_key_id":"k","private_key":"not a key"}`},
		{ServiceAccountJSON: `not json`},
	} {
		var formErr *adapter.CredentialFormError
		if _, err := New().ReadCredential(in); !errors.As(err, &formErr) {
			t.Errorf("%+v: %v, want a CredentialFormError", in.APIKey, err)
		}
	}
}

// TestJWTIsSignedWithTheAccountsKey: the assertion is header.claims.signature, RS256 over the first two with the
// account's key, naming the key, the account, the token service, the scope and the hour from the time given.
func TestJWTIsSignedWithTheAccountsKey(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	sa := serviceAccount{ProjectID: "p", PrivateKeyID: "kid-1", ClientEmail: "a@p.iam.gserviceaccount.com"}
	jwt, err := signJWT(sa, testKey(), exchangeAt, at)
	if err != nil {
		t.Fatal(err)
	}
	header, claims := verifyJWT(t, jwt)
	if header != `{"alg":"RS256","kid":"kid-1","typ":"JWT"}` {
		t.Errorf("the header is %s", header)
	}
	if want := `{"aud":"https://oauth2.googleapis.com/token","exp":1795683600,"iat":1795680000,"iss":"a@p.iam.gserviceaccount.com","scope":"https://www.googleapis.com/auth/cloud-platform"}`; claims != want {
		t.Errorf("the claims are %s, want %s", claims, want)
	}
}

// verifyJWT checks the signature with the test key's public half and returns the header and the claims.
func verifyJWT(t *testing.T, jwt string) (header, claims string) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %s", jwt)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&testKey().PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("the signature does not verify: %v", err)
	}
	h, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	c, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	return string(h), string(c)
}

// tokenService is a fake of Google's token service: it verifies each assertion and grants an hour's token, or refuses
// every one with invalid_grant.
type tokenService struct {
	server  *httptest.Server
	asked   atomic.Int32
	refuses bool
}

func newTokenService(t *testing.T) *tokenService {
	t.Helper()
	s := &tokenService{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.asked.Add(1)
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("the exchange is %v %v", r.Form, err)
		}
		verifyJWT(t, r.Form.Get("assertion"))
		if s.refuses {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`) //nolint:errcheck // a test server
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"ya29.token-`+strconv.Itoa(int(s.asked.Load()))+`","expires_in":3599,"token_type":"Bearer"}`) //nolint:errcheck // a test server
	}))
	t.Cleanup(s.server.Close)
	return s
}

// TestTokenIsCachedUntilShortlyBeforeItExpires: calls at once share one exchange, a token serves until five minutes
// before it expires, and then one exchange replaces it; a refused key is the refusal, every time.
func TestTokenIsCachedUntilShortlyBeforeItExpires(t *testing.T) {
	t.Parallel()
	svc := newTokenService(t)
	tokens := newTokens(svc.server.URL + "/token")
	key := readKey(t).Key
	at := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if a, err := tokens.get(t.Context(), svc.server.Client(), key, at); err != nil || a.project != "harbor-prod" || a.token == "" {
				t.Errorf("a token: %+v %v", a, err)
			}
		})
	}
	wg.Wait()
	for _, tc := range []struct {
		after time.Duration
		asked int32
	}{{54 * time.Minute, 1}, {55 * time.Minute, 2}, {56 * time.Minute, 2}} {
		if _, err := tokens.get(t.Context(), svc.server.Client(), key, at.Add(tc.after)); err != nil {
			t.Fatal(err)
		}
		if n := svc.asked.Load(); n != tc.asked {
			t.Errorf("after %s the service was asked %d times, want %d", tc.after, n, tc.asked)
		}
	}
	refusing := newTokenService(t)
	refusing.refuses = true
	var refused *refusal
	if _, err := newTokens(refusing.server.URL).get(t.Context(), refusing.server.Client(), key, at); !errors.As(err, &refused) || refused.status != http.StatusBadRequest {
		t.Errorf("a refused key: %v", err)
	}
}

// TestSign: Messages go to the model's rawPredict, or streamRawPredict for a stream, in the region's host, with the
// model out of the body and Vertex AI's version in it; Chat goes to the OpenAI-compatible endpoint as it is; both carry
// the token. A key the token service refuses is the credential refused, before anything is sent.
func TestSign(t *testing.T) {
	t.Parallel()
	svc := newTokenService(t)
	a := &Adapter{tokens: newTokens(svc.server.URL)}
	key := readKey(t).Key
	at := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		format             protocol.Format
		region, model, url string
		body, want         string
	}{
		{
			protocol.Messages, "us-east5", "claude-sonnet-4-5@20250929",
			"https://us-east5-aiplatform.googleapis.com/v1/projects/harbor-prod/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-5@20250929:streamRawPredict",
			`{"model":"claude-sonnet-4-5@20250929","stream":true,"max_tokens":8}`, `{"stream":true,"max_tokens":8,"anthropic_version":"vertex-2023-10-16"}`,
		},
		{
			protocol.Messages, "global", "claude-sonnet-4-5@20250929",
			"https://aiplatform.googleapis.com/v1/projects/harbor-prod/locations/global/publishers/anthropic/models/claude-sonnet-4-5@20250929:rawPredict",
			`{"model":"claude-sonnet-4-5@20250929","max_tokens":8}`, `{"max_tokens":8,"anthropic_version":"vertex-2023-10-16"}`,
		},
		{
			protocol.Chat, "europe-west4", "google/gemini-2.5-pro",
			"https://europe-west4-aiplatform.googleapis.com/v1/projects/harbor-prod/locations/europe-west4/endpoints/openapi/chat/completions",
			`{"model":"google/gemini-2.5-pro","stream":true}`, `{"model":"google/gemini-2.5-pro","stream":true}`,
		},
	} {
		req, f := a.Calls().(adapter.Signer).Sign(t.Context(), svc.server.Client(), adapter.SignedCall{
			Format: tc.format, Region: tc.region, Model: tc.model, Body: []byte(tc.body), Header: http.Header{"Anthropic-Version": {"2023-06-01"}}, Key: key, At: at,
		})
		if f != nil {
			t.Fatalf("%s: %+v", tc.url, f)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if req.URL.String() != tc.url || string(body) != tc.want || !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ya29.") || req.Header.Get("Anthropic-Version") != "" {
			t.Errorf("sent %s %s with %v", req.URL, body, req.Header)
		}
	}
	refusing := newTokenService(t)
	refusing.refuses = true
	_, f := (&Adapter{tokens: newTokens(refusing.server.URL)}).Calls().(adapter.Signer).Sign(t.Context(), refusing.server.Client(),
		adapter.SignedCall{Format: protocol.Messages, Region: "us-east5", Model: "m", Body: []byte(`{}`), Key: key, At: at})
	if _, rejected := f.Cause.(adapter.CredentialRejected); f == nil || !rejected || f.Scope != adapter.ScopeCredential || f.Executed != adapter.NotExecuted {
		t.Errorf("a refused key's call: %+v", f)
	}
}

// TestSignBetas: of the client's anthropic-beta features Vertex AI gets those it takes, once each; the rest go, and
// with none left it gets no anthropic-beta header at all.
func TestSignBetas(t *testing.T) {
	t.Parallel()
	svc := newTokenService(t)
	a := &Adapter{tokens: newTokens(svc.server.URL)}
	key := readKey(t).Key
	at := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		values []string
		want   string
	}{
		{"kept and dropped", []string{"claude-code-20250219,context-1m-2025-08-07", "advisor-tool-2026-03-01, interleaved-thinking-2025-05-14, context-1m-2025-08-07"}, "context-1m-2025-08-07,interleaved-thinking-2025-05-14"},
		{"none left", []string{"claude-code-20250219, oauth-2025-04-20"}, ""},
		{"none given", nil, ""},
	} {
		h := http.Header{"Anthropic-Beta": tc.values}
		req, f := a.Calls().(adapter.Signer).Sign(t.Context(), svc.server.Client(), adapter.SignedCall{
			Format: protocol.Messages, Region: "us-east5", Model: "claude-sonnet-4-5@20250929", Body: []byte(`{"max_tokens":8}`), Header: h, Key: key, At: at,
		})
		if f != nil {
			t.Fatalf("%s: %+v", tc.name, f)
		}
		if got := req.Header.Get("Anthropic-Beta"); got != tc.want || len(req.Header.Values("Anthropic-Beta")) > 1 || len(h["Anthropic-Beta"]) != len(tc.values) {
			t.Errorf("%s: sent %q, the call's own %v", tc.name, req.Header.Values("Anthropic-Beta"), h["Anthropic-Beta"])
		}
	}
}

// TestCheckRegion: the region is read with the token; a region the project cannot use fails as region_unavailable,
// a key the token service refuses fails as refused, and a 403 says nothing about the credential.
func TestCheckRegion(t *testing.T) {
	t.Parallel()
	svc := newTokenService(t)
	key := readKey(t).Key
	at := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		status int
		want   adapter.CloudCheck
	}{
		{http.StatusOK, adapter.CloudCheck{Outcome: adapter.KeyPassed, Status: 200}},
		{http.StatusNotFound, adapter.CloudCheck{Outcome: adapter.KeyFailed, Cause: adapter.CauseRegionUnavailable, Status: 404}},
		{http.StatusForbidden, adapter.CloudCheck{Cause: adapter.CauseNotPermitted, Status: 403}},
	} {
		d := regionDoer{tokens: svc.server.Client(), status: tc.status, t: t}
		got := (&Adapter{tokens: newTokens(svc.server.URL)}).CheckRegion(t.Context(), d, origin, "us-east5", key, at)
		if got.Outcome != tc.want.Outcome || got.Cause != tc.want.Cause || got.Status != tc.want.Status || got.Models != nil {
			t.Errorf("a %d: %+v", tc.status, got)
		}
	}
	refusing := newTokenService(t)
	refusing.refuses = true
	got := (&Adapter{tokens: newTokens(refusing.server.URL)}).CheckRegion(t.Context(), refusing.server.Client(), origin, "us-east5", key, at)
	if got.Outcome != adapter.KeyFailed || got.Cause != adapter.CauseRejected || got.Status != 400 {
		t.Errorf("a refused key: %+v", got)
	}
}

// regionDoer answers the region's read with status, and sends the exchange to the fake token service.
type regionDoer struct {
	tokens *http.Client
	status int
	t      *testing.T
}

func (d regionDoer) Do(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Host, "aiplatform.googleapis.com") {
		return d.tokens.Do(r)
	}
	if r.URL.Path != "/v1/projects/harbor-prod/locations/us-east5" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ya29.") {
		d.t.Errorf("the check asked %s with %q", r.URL, r.Header.Get("Authorization"))
	}
	return &http.Response{StatusCode: d.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

// TestOffer: Anthropic's dated names take an @ before the date, Gemini is google/ and its name, and another vendor's
// model is not served.
func TestOffer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		author, official, want string
		ok                     bool
	}{
		{"anthropic", "claude-sonnet-4-5-20250929", "claude-sonnet-4-5@20250929", true},
		{"anthropic", "claude-opus-4-6", "claude-opus-4-6", true},
		{"google", "gemini-2.5-pro", "google/gemini-2.5-pro", true},
		{"openai", "gpt-5", "", false},
	} {
		if got, ok := New().Offer(catalog.Model{Author: tc.author}, tc.official); got != tc.want || ok != tc.ok {
			t.Errorf("%s %s: %q %v", tc.author, tc.official, got, ok)
		}
	}
}
