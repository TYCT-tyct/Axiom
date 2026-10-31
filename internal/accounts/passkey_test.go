package accounts_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"uuid"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"

	"axiom/internal/accounts"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var b64 = base64.RawURLEncoding

// credentialIDLength is the length of the test authenticator's random credential IDs.
const credentialIDLength = 16

// softAuthenticator is a WebAuthn authenticator in the test: it makes a P-256 credential and signs assertions for the
// RP ID and origin it is told, which is enough to exercise the relying party's verification.
type softAuthenticator struct {
	key     *ecdsa.PrivateKey
	credID  []byte
	counter uint32
	rpID    string
	origin  string
	userID  []byte
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credID := make([]byte, credentialIDLength)
	if _, err := rand.Read(credID); err != nil {
		t.Fatal(err)
	}
	return &softAuthenticator{key: key, credID: credID, rpID: console, origin: "https://" + console}
}

func (a *softAuthenticator) coseKey(t *testing.T) []byte {
	t.Helper()
	// The uncompressed SEC 1 point is 0x04, then the 32-byte X and Y coordinates.
	point, err := a.key.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(point) != 65 {
		t.Fatalf("the P-256 public key encodes to %d bytes, want 65", len(point))
	}
	encoded, err := cbor.Marshal(map[int64]any{
		1: int64(2), 3: int64(-7), -1: int64(1),
		-2: point[1:33], -3: point[33:65],
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func (a *softAuthenticator) clientData(t *testing.T, kind, challenge string) []byte {
	t.Helper()
	encoded, err := jsonx.MarshalCanonical(map[string]any{"type": kind, "challenge": challenge, "origin": a.origin, "crossOrigin": false})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// authData is the RP ID hash, flags, counter and, at registration, the attested credential.
func (a *softAuthenticator) authData(t *testing.T, flags byte, attested bool) []byte {
	t.Helper()
	hash := sha256.Sum256([]byte(a.rpID))
	out := append([]byte{}, hash[:]...)
	out = append(out, flags)
	out = binary.BigEndian.AppendUint32(out, a.counter)
	if !attested {
		return out
	}
	out = append(out, make([]byte, 16)...) // AAGUID
	out = binary.BigEndian.AppendUint16(out, credentialIDLength)
	out = append(out, a.credID...)
	return append(out, a.coseKey(t)...)
}

// register answers a creation challenge with a none-attestation.
func (a *softAuthenticator) register(t *testing.T, options any) []byte {
	t.Helper()
	var creation struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	raw, err := jsonx.MarshalCanonical(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonx.Unmarshal(raw, &creation); err != nil {
		t.Fatal(err)
	}
	a.rpID = creation.PublicKey.RP.ID
	if a.userID, err = b64.DecodeString(creation.PublicKey.User.ID); err != nil {
		t.Fatal(err)
	}
	clientData := a.clientData(t, "webauthn.create", creation.PublicKey.Challenge)
	attestation, err := cbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(t, 0x45, true), // UP | UV | AT
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := jsonx.MarshalCanonical(map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(clientData), "attestationObject": b64.EncodeToString(attestation),
			"transports": []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// assert answers an assertion challenge, signing client data and authenticator data with the credential's key.
func (a *softAuthenticator) assert(t *testing.T, options any) []byte {
	t.Helper()
	var assertion struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	raw, err := jsonx.MarshalCanonical(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonx.Unmarshal(raw, &assertion); err != nil {
		t.Fatal(err)
	}
	a.counter++
	clientData := a.clientData(t, "webauthn.get", assertion.PublicKey.Challenge)
	authData := a.authData(t, 0x05, false) // UP | UV
	clientHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	response, err := jsonx.MarshalCanonical(map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(clientData), "authenticatorData": b64.EncodeToString(authData),
			"signature": b64.EncodeToString(signature), "userHandle": b64.EncodeToString(a.userID),
		},
		"clientExtensionResults": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// reSign rewrites an assertion's signature with mutate's answer, for the refusal cases that need a well-formed body.
func reSign(t *testing.T, evidence []byte, mutate func([]byte) []byte) []byte {
	t.Helper()
	var parsed map[string]any
	if err := jsonx.Unmarshal(evidence, &parsed); err != nil {
		t.Fatal(err)
	}
	response, ok := parsed["response"].(map[string]any)
	if !ok {
		t.Fatal("the assertion has no response")
	}
	signature, err := b64.DecodeString(response["signature"].(string))
	if err != nil {
		t.Fatal(err)
	}
	response["signature"] = b64.EncodeToString(mutate(signature))
	out, err := jsonx.MarshalCanonical(parsed)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// fatalPasskey reports a WebAuthn error with the relying party's details, which its message leaves out.
func fatalPasskey(t *testing.T, err error) {
	t.Helper()
	if pe, ok := errors.AsType[*protocol.Error](err); ok {
		t.Fatalf("the ceremony: %s: %s %s", pe.Details, pe.DevInfo, pe.Type)
	}
	t.Fatal(err)
}

// passkeyRequest is the ceremony request the module takes its Host and body from.
func passkeyRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+console+"/", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://"+console)
	return r
}

// registerPasskeyForTest adds the first credential and returns it, with the spent-flow refusals checked.
func registerPasskeyForTest(t *testing.T, x site, s *accounts.Service, auth *softAuthenticator) accounts.Passkey {
	t.Helper()
	session := x.signIn(t, console)
	p, err := s.Authenticate(t.Context(), x.request(t, console, "https://"+console, session), client)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := s.BeginPasskeyRegistration(t.Context(), passkeyRequest(t, nil), p, "Laptop", password)
	if err != nil {
		t.Fatal(err)
	}
	if got := registration.PublicKey.Response.AuthenticatorSelection.ResidentKey; got != protocol.ResidentKeyRequirementRequired {
		t.Fatalf("registration resident key = %q, want required", got)
	}
	evidence := auth.register(t, registration.PublicKey)
	passkey, issued, _, err := s.FinishPasskeyRegistration(t.Context(), passkeyRequest(t, evidence), p, registration.FlowID, client, "test")
	if err != nil {
		fatalPasskey(t, err)
	}
	if issued.Pending || passkey.Name != "Laptop" {
		t.Fatalf("registration: %+v %+v", passkey, issued)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.passkey_credentials"); got[0] != "1" {
		t.Errorf("passkeys: %v", got)
	}
	// An unknown or already spent flow is a refused ceremony, not a panic or a second grant.
	if _, _, _, err := s.FinishPasskeyRegistration(t.Context(), passkeyRequest(t, evidence), p, uuid.NewV7().String(), client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("registration with an unknown flow: %v", err)
	}
	if _, _, _, err := s.FinishPasskeyRegistration(t.Context(), passkeyRequest(t, evidence), p, registration.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("registration with a spent flow: %v", err)
	}
	return passkey
}

// assertPasskeysStayOn checks that a site cannot turn passkeys off while a user holds one.
func assertPasskeysStayOn(t *testing.T, x site, s *accounts.Service) {
	t.Helper()
	// Turning passkeys off while a user holds one would leave that credential on a second factor path the sign-in page
	// cannot offer, so it is refused until the users remove them.
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		return s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{
			LoginDomain: &domain, Passkeys: false,
		}, "test")
	}); !errors.Is(err, accounts.ErrPasskeysInUse) {
		t.Fatalf("turning passkeys off with a credential: %v", err)
	}
}

// pendingPasskeySignIn is the pending session a password sign-in now gets, and its cookie.
func pendingPasskeySignIn(t *testing.T, x site, s *accounts.Service) (accounts.Principal, *http.Cookie) {
	t.Helper()
	pending := x.signIn(t, console)
	if !pending.Pending {
		t.Fatal("a password sign-in skipped the passkey factor")
	}
	// The admin listener cannot run a WebAuthn ceremony, so a passkey must not leave its password sign-in waiting for
	// one: an operator with a passkey and no TOTP could never finish.
	if onAdmin := x.signIn(t, admin); onAdmin.Pending {
		t.Error("the admin sign-in waited for a passkey")
	}
	p, err := s.AuthenticateAny(t.Context(), x.request(t, console, "https://"+console, pending), client)
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := s.Cookie(console, pending)
	if err != nil {
		t.Fatal(err)
	}
	return p, cookie
}

// assertWrongAssertionSpendsAttempt checks that a failed assertion spends one of the pending session's attempts, like a
// wrong code does.
func assertWrongAssertionSpendsAttempt(t *testing.T, x site, s *accounts.Service, auth *softAuthenticator, p accounts.Principal, cookie *http.Cookie) {
	t.Helper()
	flow, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), p)
	if err != nil {
		t.Fatal(err)
	}
	bad := reSign(t, auth.assert(t, flow.PublicKey), func(signature []byte) []byte {
		signature[len(signature)-1] ^= 0xff
		return signature
	})
	r := passkeyRequest(t, bad)
	r.AddCookie(cookie)
	if _, err := s.FinishPasskeyMFA(t.Context(), r, p, flow.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Fatalf("a wrong assertion: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT mfa_attempts::text FROM accounts.sessions WHERE user_id = (SELECT id FROM accounts.users WHERE email = 'someone@example.com') AND level = 'pending_mfa' ORDER BY created_at DESC LIMIT 1"); got[0] != "1" {
		t.Errorf("attempts after a wrong assertion: %v", got)
	}
}

// completePasskeyMFA finishes the pending sign-in with a passkey and returns the verified session.
func completePasskeyMFA(t *testing.T, s *accounts.Service, auth *softAuthenticator, p accounts.Principal, cookie *http.Cookie) accounts.Issued {
	t.Helper()
	flow, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), p)
	if err != nil {
		t.Fatal(err)
	}
	r := passkeyRequest(t, auth.assert(t, flow.PublicKey))
	r.AddCookie(cookie)
	verified, err := s.FinishPasskeyMFA(t.Context(), r, p, flow.FlowID, client, "test")
	if err != nil {
		t.Fatal(err)
	}
	if verified.Pending {
		t.Error("the passkey factor left a pending session")
	}
	if _, err := s.FinishPasskeyMFA(t.Context(), r, p, flow.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("MFA with a spent flow: %v", err)
	}
	if _, err := s.FinishPasskeyMFA(t.Context(), r, p, uuid.NewV7().String(), client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("MFA with an unknown flow: %v", err)
	}
	return verified
}

// assertDiscoverableSignIn checks the sign-in that names no email: the credential's user handle finds the user.
func assertDiscoverableSignIn(t *testing.T, x site, s *accounts.Service, auth *softAuthenticator) {
	t.Helper()
	challenge, err := s.BeginPasskeySignIn(t.Context(), console, client)
	if err != nil {
		t.Fatal(err)
	}
	signedIn, err := s.FinishPasskeySignIn(t.Context(), passkeyRequest(t, auth.assert(t, challenge.PublicKey)), challenge.FlowID, client, "test")
	if err != nil || signedIn.Pending {
		t.Fatalf("discoverable sign-in: %+v %v", signedIn, err)
	}
	if p, err := s.Authenticate(t.Context(), cookieRequest(t, s, signedIn), client); err != nil || p.UserID() != x.user {
		t.Errorf("the session is the passkey's user: %v %v", p, err)
	}
	if _, err := s.FinishPasskeySignIn(t.Context(), passkeyRequest(t, auth.assert(t, challenge.PublicKey)), challenge.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("sign-in with a spent flow: %v", err)
	}
	if _, err := s.FinishPasskeySignIn(t.Context(), passkeyRequest(t, nil), uuid.NewV7().String(), client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("sign-in with an unknown flow: %v", err)
	}
}

// assertCounterRefusals checks the authenticator's clone warning and that a zero counter never lowers a stored one.
func assertCounterRefusals(t *testing.T, x site, s *accounts.Service, auth *softAuthenticator) {
	t.Helper()
	// A counter that goes backwards is the authenticator's clone warning: the assertion is refused before the session.
	auth.counter = 0
	backwards, err := s.BeginPasskeySignIn(t.Context(), console, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishPasskeySignIn(t.Context(), passkeyRequest(t, auth.assert(t, backwards.PublicKey)), backwards.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("a counter that went backwards: %v", err)
	}

	// A zero counter is accepted only while the stored counter is also zero. Simulate the interleaving where a 1
	// landed first, then a stale assertion carrying 0: the conditional update must not lower it.
	if err := pgtest.Run(t.Context(), x.database.Superuser, "UPDATE accounts.passkey_credentials SET sign_count = 1"); err != nil {
		t.Fatal(err)
	}
	auth.counter = ^uint32(0) // the next assert increments to zero
	zero, err := s.BeginPasskeySignIn(t.Context(), console, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishPasskeySignIn(t.Context(), passkeyRequest(t, auth.assert(t, zero.PublicKey)), zero.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("a zero counter over a non-zero stored count: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT sign_count::text FROM accounts.passkey_credentials"); got[0] != "1" {
		t.Errorf("the counter was lowered: %v", got)
	}
}

// assertTamperedAssertion checks that a signature over other bytes is refused.
func assertTamperedAssertion(t *testing.T, s *accounts.Service, auth *softAuthenticator) {
	t.Helper()
	replay, err := s.BeginPasskeySignIn(t.Context(), console, client)
	if err != nil {
		t.Fatal(err)
	}
	tampered := reSign(t, auth.assert(t, replay.PublicKey), func([]byte) []byte { return []byte("not a signature") })
	if _, err := s.FinishPasskeySignIn(t.Context(), passkeyRequest(t, tampered), replay.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("a tampered assertion: %v", err)
	}
}

// removePasskey checks that removing a credential needs the password, ends other sessions, and is audited.
func removePasskey(t *testing.T, x site, s *accounts.Service, verified accounts.Issued, passkey accounts.Passkey) {
	t.Helper()
	verifiedPrincipal, err := s.Authenticate(t.Context(), x.request(t, console, "https://"+console, verified), client)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DeletePasskey(t.Context(), verifiedPrincipal, passkey.ID, password, client, "test"); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.passkey_credentials"); got[0] != "0" {
		t.Errorf("passkeys after removal: %v", got)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action IN ('user.passkey_add','user.passkey_remove')"); got[0] != "2" {
		t.Errorf("passkey audits: %v", got)
	}
}

func TestPasskeyRegistrationSignInAndMFA(t *testing.T) {
	t.Parallel()
	x, s := passkeySite(t)
	auth := newSoftAuthenticator(t)
	passkey := registerPasskeyForTest(t, x, s, auth)
	assertPasskeysStayOn(t, x, s)
	pendingPrincipal, pendingCookie := pendingPasskeySignIn(t, x, s)
	assertWrongAssertionSpendsAttempt(t, x, s, auth, pendingPrincipal, pendingCookie)
	verified := completePasskeyMFA(t, s, auth, pendingPrincipal, pendingCookie)
	assertDiscoverableSignIn(t, x, s, auth)
	assertCounterRefusals(t, x, s, auth)
	assertTamperedAssertion(t, s, auth)
	removePasskey(t, x, s, verified, passkey)
}

// TestPasskeyRegistrationNeedsThePassword: a passkey cannot be added with a session alone.
func TestPasskeyRegistrationNeedsThePassword(t *testing.T) {
	t.Parallel()
	x, s := passkeySite(t)
	session := x.signIn(t, console)
	p, err := s.Authenticate(t.Context(), x.request(t, console, "https://"+console, session), client)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 11 {
		_, err := s.BeginPasskeyRegistration(t.Context(), passkeyRequest(t, nil), p, "Laptop", "wrong password")
		if i < 10 && !errors.Is(err, accounts.ErrBadCredentials) {
			t.Errorf("wrong password %d: %v", i, err)
		}
		if i == 10 && !errors.Is(err, accounts.ErrTooManyAttempts) {
			t.Errorf("the eleventh wrong password did not spend the user's budget: %v", err)
		}
	}
}

// TestPasskeySignInNeedsTheLoginDomain keeps passkey ceremonies off a console domain that is not the relying party.
func TestPasskeySignInNeedsTheLoginDomain(t *testing.T) {
	t.Parallel()
	x, s := passkeySite(t)
	const secondConsole = "login4.main.example"
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`INSERT INTO accounts.site_domains VALUES ('`+secondConsole+`', :s1, 'console', 'https://`+secondConsole+`', NULL)`)); err != nil {
		t.Fatal(err)
	}
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`INSERT INTO accounts.passkey_credentials (id, site_id, account_id, user_id, credential_id, public_key, name)
		 SELECT :x, u.site_id, u.account_id, u.id, '\x01', '\x02', 'Test' FROM accounts.users u
		 WHERE u.email = 'someone@example.com'`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginPasskeySignIn(t.Context(), secondConsole, client); !errors.Is(err, accounts.ErrWrongLoginDomain) {
		t.Errorf("another console domain: %v", err)
	}
	// A password sign-in there must not hand out a session that skips the passkey just because the page did not
	// redirect first, and a correct password sent to the wrong domain must not spend the email's failure budget.
	for range 6 {
		if _, err := s.SignIn(t.Context(), accounts.SignInRequest{Host: secondConsole, Email: x.email, Password: password}); !errors.Is(err, accounts.ErrWrongLoginDomain) {
			t.Errorf("password on another console domain: %v", err)
		}
	}
}
