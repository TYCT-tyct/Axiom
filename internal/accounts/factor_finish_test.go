package accounts_test

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
)

// enableTOTPForTest turns on two-step verification through s and returns the verified principal of the session it
// issued, that session, and its recovery codes.
func enableTOTPForTest(t *testing.T, s *accounts.Service, x site, session accounts.Issued) (accounts.Principal, accounts.Issued, []string) {
	t.Helper()
	p, err := s.Authenticate(t.Context(), x.request(t, console, "https://"+console, session), client)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := s.BeginTOTP(p)
	mfa, codes, err := s.ConfirmTOTP(t.Context(), p, accounts.FactorChange{Secret: key, Password: password, Code: accounts.TOTPCode(key, time.Now())})
	if err != nil || len(codes) != 10 {
		t.Fatalf("enable two-step verification: %v, %d codes", err, len(codes))
	}
	verified, err := s.Authenticate(t.Context(), cookieRequest(t, s, mfa), client)
	if err != nil {
		t.Fatal(err)
	}
	return verified, mfa, codes
}

// usesReplacementCodes checks that a factor change made replacements the only working codes: one of the old codes is
// refused and one of the replacements signs in.
func (x site) usesReplacementCodes(t *testing.T, currentPassword string, old, replacements []string) {
	t.Helper()
	if len(replacements) != 10 {
		t.Fatalf("replacement recovery codes: %d, want 10", len(replacements))
	}
	challenge, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: currentPassword})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.verify(t, console, challenge, old[0]); !errors.Is(err, accounts.ErrBadCode) {
		t.Fatalf("an old recovery code: %v, want a refusal", err)
	}
	if _, err := x.verify(t, console, challenge, replacements[0]); err != nil {
		t.Fatalf("a replacement recovery code: %v", err)
	}
}

// TestLinkAndUnlinkReplaceRecoveryCodes: a provider link and an unlink both raise the factor version, so the old
// recovery codes stop working; with TOTP on, each makes new ones. The link's answer is a redirect, so its codes are
// sealed on the new session and the console takes them once; the unlink returns its codes in the answer.
func TestLinkAndUnlinkReplaceRecoveryCodes(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t, 42)
	oauthSite(t, f.x, f.s, "github")
	verified, session, codes := enableTOTPForTest(t, f.s, f.x, f.x.signIn(t, console))

	state, _ := startURL(t, f.s, &verified)
	out, err := f.s.CompleteOAuth(t.Context(), callback(t, f.s, state, "", &session), "github", "the-code", state, client, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Linked || out.Issued == nil {
		t.Fatalf("link: %+v", out)
	}
	request := cookieRequest(t, f.s, *out.Issued)
	linked, err := f.s.Authenticate(t.Context(), request, client)
	if err != nil {
		t.Fatal(err)
	}
	taken, err := f.s.TakePendingRecoveryCodes(t.Context(), request, linked)
	if err != nil {
		t.Fatal(err)
	}
	f.x.usesReplacementCodes(t, password, codes, taken)
	// Reading clears: the codes exist in the browser exactly once.
	if again, err := f.s.TakePendingRecoveryCodes(t.Context(), request, linked); err != nil || len(again) != 0 {
		t.Fatalf("a second take: %v, %d codes", err, len(again))
	}

	_, unlinked, err := f.s.UnlinkIdentity(t.Context(), linked, "github", client, "test")
	if err != nil {
		t.Fatal(err)
	}
	f.x.usesReplacementCodes(t, password, taken, unlinked)
}

// TestPasskeyChangesReplaceRecoveryCodes: adding and removing a passkey both raise the factor version; with TOTP on,
// each returns new recovery codes, and the ones from before stop working.
func TestPasskeyChangesReplaceRecoveryCodes(t *testing.T) {
	t.Parallel()
	x, s := passkeySite(t)
	verified, _, codes := enableTOTPForTest(t, s, x, x.signIn(t, console))

	registration, err := s.BeginPasskeyRegistration(t.Context(), passkeyRequest(t, nil), verified, "Laptop", password)
	if err != nil {
		t.Fatal(err)
	}
	evidence := newSoftAuthenticator(t).register(t, registration.PublicKey)
	passkey, issued, added, err := s.FinishPasskeyRegistration(t.Context(), passkeyRequest(t, evidence), verified, registration.FlowID, client, "test")
	if err != nil {
		fatalPasskey(t, err)
	}
	x.usesReplacementCodes(t, password, codes, added)

	after, err := s.Authenticate(t.Context(), cookieRequest(t, s, issued), client)
	if err != nil {
		t.Fatal(err)
	}
	_, removed, err := s.DeletePasskey(t.Context(), after, passkey.ID, password, client, "test")
	if err != nil {
		t.Fatal(err)
	}
	x.usesReplacementCodes(t, password, added, removed)
}

// TestRevokedSessionDropsPendingCodes: codes left unread must not outlive their session. The clearing lives in the
// database, so a revocation that names only revoked_at -- the one an older node sends -- drops them too.
func TestRevokedSessionDropsPendingCodes(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t, 42)
	oauthSite(t, f.x, f.s, "github")
	verified, session, _ := enableTOTPForTest(t, f.s, f.x, f.x.signIn(t, console))
	state, _ := startURL(t, f.s, &verified)
	out, err := f.s.CompleteOAuth(t.Context(), callback(t, f.s, state, "", &session), "github", "the-code", state, client, "test")
	if err != nil || !out.Linked || out.Issued == nil {
		t.Fatalf("link: %+v %v", out, err)
	}
	id := uuid.UUID(out.Issued.SessionID()).String()
	left := "SELECT CASE WHEN recovery_codes_ciphertext IS NOT NULL THEN 'yes' ELSE 'no' END FROM accounts.sessions WHERE id='" + id + "'"
	if got := texts(t, f.x.db, platform, left); len(got) != 1 || got[0] != "yes" {
		t.Fatalf("codes left on the session: %v", got)
	}
	if _, err := exec(t, f.x.db, platform, "UPDATE accounts.sessions SET revoked_at=now() WHERE id='"+id+"'"); err != nil {
		t.Fatal(err)
	}
	clear := "SELECT CASE WHEN recovery_codes_ciphertext IS NULL AND recovery_codes_nonce IS NULL THEN 'yes' ELSE 'no' END FROM accounts.sessions WHERE id='" + id + "'"
	if got := texts(t, f.x.db, platform, clear); len(got) != 1 || got[0] != "yes" {
		t.Fatalf("codes after revocation: %v", got)
	}
}
