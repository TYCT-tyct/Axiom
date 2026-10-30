package accounts_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"uuid"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"

	"axiom/internal/accounts"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var b64 = base64.RawURLEncoding

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
	credID := make([]byte, 16)
	if _, err := rand.Read(credID); err != nil {
		t.Fatal(err)
	}
	return &softAuthenticator{key: key, credID: credID, rpID: console, origin: "https://" + console}
}

func (a *softAuthenticator) coseKey(t *testing.T) []byte {
	t.Helper()
	encoded, err := cbor.Marshal(map[int64]any{
		1: int64(2), 3: int64(-7), -1: int64(1),
		-2: a.key.PublicKey.X.FillBytes(make([]byte, 32)), -3: a.key.PublicKey.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func (a *softAuthenticator) clientData(t *testing.T, kind, challenge string) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": a.origin, "crossOrigin": false})
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
	out = binary.BigEndian.AppendUint16(out, uint16(len(a.credID)))
	out = append(out, a.credID...)
	return append(out, a.coseKey(t)...)
}

// register answers a creation challenge with a none-attestation.
func (a *softAuthenticator) register(t *testing.T, options any) json.RawMessage {
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
	raw, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &creation); err != nil {
		t.Fatal(err)
	}
	a.rpID = creation.PublicKey.RP.ID
	a.userID, _ = b64.DecodeString(creation.PublicKey.User.ID)
	clientData := a.clientData(t, "webauthn.create", creation.PublicKey.Challenge)
	attestation, err := cbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(t, 0x45, true), // UP | UV | AT
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal(map[string]any{
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
func (a *softAuthenticator) assert(t *testing.T, options any) json.RawMessage {
	t.Helper()
	var assertion struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	raw, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &assertion); err != nil {
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
	response, err := json.Marshal(map[string]any{
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

// passkeyRequest is the ceremony request the module takes its Host and body from.
func passkeyRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+console+"/", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://"+console)
	return r
}

func TestPasskeyRegistrationSignInAndMFA(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	s := oauthService(t, x.db, egress.NewForTesting(egresstest.Loopback(t)))
	x.s = s
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		return s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{
			LoginDomain: &domain, Passkeys: true,
		}, "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	session := x.signIn(t, console)
	p, err := s.Authenticate(t.Context(), x.request(t, console, "https://"+console, session), client)
	if err != nil {
		t.Fatal(err)
	}
	auth := newSoftAuthenticator(t)
	registration, err := s.BeginPasskeyRegistration(t.Context(), passkeyRequest(t, nil), p, "Laptop", password)
	if err != nil {
		t.Fatal(err)
	}
	if got := registration.PublicKey.Response.AuthenticatorSelection.ResidentKey; got != protocol.ResidentKeyRequirementRequired {
		t.Fatalf("registration resident key = %q, want required", got)
	}
	evidence := auth.register(t, registration.PublicKey)
	passkey, issued, err := s.FinishPasskeyRegistration(t.Context(), passkeyRequest(t, evidence), p, registration.FlowID, client, "test")
	if err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) {
			t.Fatalf("registration: %s: %s %s", pe.Details, pe.DevInfo, pe.Type)
		}
		t.Fatal(err)
	}
	if issued.Pending || passkey.Name != "Laptop" {
		t.Fatalf("registration: %+v %+v", passkey, issued)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.passkey_credentials"); got[0] != "1" {
		t.Errorf("passkeys: %v", got)
	}
	// An unknown or already spent flow is a refused ceremony, not a panic or a second grant.
	if _, _, err := s.FinishPasskeyRegistration(t.Context(), passkeyRequest(t, evidence), p, uuid.NewV7().String(), client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("registration with an unknown flow: %v", err)
	}
	if _, _, err := s.FinishPasskeyRegistration(t.Context(), passkeyRequest(t, evidence), p, registration.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("registration with a spent flow: %v", err)
	}

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

	// The user now has a second factor: the password alone gets a pending session, and a passkey assertion completes it.
	pending := x.signIn(t, console)
	if !pending.Pending {
		t.Fatal("a password sign-in skipped the passkey factor")
	}
	// The admin listener cannot run a WebAuthn ceremony, so a passkey must not leave its password sign-in waiting for
	// one: an operator with a passkey and no TOTP could never finish.
	if onAdmin := x.signIn(t, admin); onAdmin.Pending {
		t.Error("the admin sign-in waited for a passkey")
	}
	pendingPrincipal, err := s.AuthenticateAny(t.Context(), x.request(t, console, "https://"+console, pending), client)
	if err != nil {
		t.Fatal(err)
	}
	pendingCookie, err := s.Cookie(console, pending)
	if err != nil {
		t.Fatal(err)
	}

	// A failed assertion spends one of the pending session's attempts, like a wrong code does.
	badFlow, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), pendingPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	var badEvidence map[string]any
	if err := json.Unmarshal(auth.assert(t, badFlow.PublicKey), &badEvidence); err != nil {
		t.Fatal(err)
	}
	badResponse := badEvidence["response"].(map[string]any)
	signature, err := b64.DecodeString(badResponse["signature"].(string))
	if err != nil {
		t.Fatal(err)
	}
	signature[len(signature)-1] ^= 0xff
	badResponse["signature"] = b64.EncodeToString(signature)
	badAssertion, err := json.Marshal(badEvidence)
	if err != nil {
		t.Fatal(err)
	}
	badRequest := passkeyRequest(t, badAssertion)
	badRequest.AddCookie(pendingCookie)
	if _, err := s.FinishPasskeyMFA(t.Context(), badRequest, pendingPrincipal, badFlow.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Fatalf("a wrong assertion: %v", err)
	}
	if got := texts(t, x.db, platform, "SELECT mfa_attempts::text FROM accounts.sessions WHERE user_id = (SELECT id FROM accounts.users WHERE email = 'someone@example.com') AND level = 'pending_mfa' ORDER BY created_at DESC LIMIT 1"); got[0] != "1" {
		t.Errorf("attempts after a wrong assertion: %v", got)
	}

	mfa, err := s.BeginPasskeyMFA(t.Context(), passkeyRequest(t, nil), pendingPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	mfaRequest := passkeyRequest(t, auth.assert(t, mfa.PublicKey))
	mfaRequest.AddCookie(pendingCookie)
	verified, err := s.FinishPasskeyMFA(t.Context(), mfaRequest, pendingPrincipal, mfa.FlowID, client, "test")
	if err != nil {
		t.Fatal(err)
	}
	if verified.Pending {
		t.Error("the passkey factor left a pending session")
	}
	if _, err := s.FinishPasskeyMFA(t.Context(), mfaRequest, pendingPrincipal, mfa.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("MFA with a spent flow: %v", err)
	}
	if _, err := s.FinishPasskeyMFA(t.Context(), mfaRequest, pendingPrincipal, uuid.NewV7().String(), client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("MFA with an unknown flow: %v", err)
	}

	// A discoverable sign-in names no email: the credential's user handle finds the user.
	challenge, err := s.BeginPasskeySignIn(t.Context(), console, client)
	if err != nil {
		t.Fatal(err)
	}
	signedIn, err := s.FinishPasskeySignIn(t.Context(), passkeyRequest(t, auth.assert(t, challenge.PublicKey)), challenge.FlowID, client, "test")
	if err != nil || signedIn.Pending {
		t.Fatalf("discoverable sign-in: %+v %v", signedIn, err)
	}
	if p, err := s.Authenticate(t.Context(), func() *http.Request {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+console+"/", nil)
		cookie, _ := s.Cookie(console, signedIn)
		r.AddCookie(cookie)
		return r
	}(), client); err != nil || p.UserID() != x.user {
		t.Errorf("the session is the passkey's user: %v %v", p.UserID(), err)
	}
	if _, err := s.FinishPasskeySignIn(t.Context(), passkeyRequest(t, auth.assert(t, challenge.PublicKey)), challenge.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("sign-in with a spent flow: %v", err)
	}
	if _, err := s.FinishPasskeySignIn(t.Context(), passkeyRequest(t, nil), uuid.NewV7().String(), client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("sign-in with an unknown flow: %v", err)
	}

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

	// A signature over other bytes is refused.
	replay, err := s.BeginPasskeySignIn(t.Context(), console, client)
	if err != nil {
		t.Fatal(err)
	}
	bad := auth.assert(t, replay.PublicKey)
	var parsed map[string]any
	if err := json.Unmarshal(bad, &parsed); err != nil {
		t.Fatal(err)
	}
	response := parsed["response"].(map[string]any)
	response["signature"] = b64.EncodeToString([]byte("not a signature"))
	tampered, _ := json.Marshal(parsed)
	if _, err := s.FinishPasskeySignIn(t.Context(), passkeyRequest(t, tampered), replay.FlowID, client, "test"); !errors.Is(err, accounts.ErrPasskeyFailed) {
		t.Errorf("a tampered assertion: %v", err)
	}

	// Removing the credential is a credential change: it needs the password and the current session, and it ends every
	// other session.
	verifiedPrincipal, err := s.Authenticate(t.Context(), x.request(t, console, "https://"+console, verified), client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeletePasskey(t.Context(), verifiedPrincipal, passkey.ID, password, client, "test"); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM accounts.passkey_credentials"); got[0] != "0" {
		t.Errorf("passkeys after removal: %v", got)
	}
	if got := texts(t, x.db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action IN ('user.passkey_add','user.passkey_remove')"); got[0] != "2" {
		t.Errorf("passkey audits: %v", got)
	}
}

// TestPasskeyRegistrationNeedsThePassword: a passkey cannot be added with a session alone.
func TestPasskeyRegistrationNeedsThePassword(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	s := oauthService(t, x.db, egress.NewForTesting(egresstest.Loopback(t)))
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		return s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{
			LoginDomain: &domain, Passkeys: true,
		}, "test")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
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
	x := newTestSite(t)
	const secondConsole = "login4.main.example"
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`INSERT INTO accounts.site_domains VALUES ('`+secondConsole+`', :s1, 'console', 'https://`+secondConsole+`', NULL)`)); err != nil {
		t.Fatal(err)
	}
	s := oauthService(t, x.db, egress.NewForTesting(egresstest.Loopback(t)))
	if err := x.db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		domain := console
		return s.SetSiteSignIn(t.Context(), tx, system, accounts.SiteID(s1), accounts.SiteSignInUpdate{
			LoginDomain: &domain, Passkeys: true,
		}, "test")
	}); err != nil {
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
