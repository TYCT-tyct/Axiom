package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"time"
	"uuid"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// maxPasskeysPerUser bounds one account's credentials: every sign-in ceremony offers all of them, and a person has no
// use for more than their devices.
const maxPasskeysPerUser = 10

// ErrTooManyPasskeys is a user adding a credential past maxPasskeysPerUser.
var ErrTooManyPasskeys = errors.New("accounts: too many passkeys")

// ErrPasskeysOff is a passkey ceremony on a site or domain that cannot run one.
var ErrPasskeysOff = errors.New("accounts: passkeys are not enabled")

// errPasskeyAssertion is an assertion that parsed but did not verify against a live flow of this session: the one
// passkey failure that is a guessed factor. ErrPasskeyFailed also covers an unknown or expired flow and a malformed
// ceremony, which are not attacks and must not reach the failure counter.
var errPasskeyAssertion = errors.New("accounts: the passkey assertion did not verify")

// Passkey is one credential as the account page shows it.
type Passkey struct {
	ID             PasskeyID
	Name           string
	CreatedAt      time.Time
	LastUsedAt     *time.Time
	BackupEligible bool
}

// PasskeyRegistration is a creation challenge and the flow that completes it.
type PasskeyRegistration struct {
	PublicKey *protocol.CredentialCreation
	FlowID    string
}

// PasskeyChallenge is an assertion challenge and the flow that completes it.
type PasskeyChallenge struct {
	PublicKey *protocol.CredentialAssertion
	FlowID    string
}

// BeginPasskeyRegistration starts adding a credential. The current password is required, and a session that has not
// passed the user's second factor is refused: a passkey can sign in on its own, so adding one must not be possible with
// the password alone.
func (s *Service) BeginPasskeyRegistration(ctx context.Context, r *http.Request, p Principal, name, password string) (PasskeyRegistration, error) {
	site, wa, err := s.passkeySite(r.Host)
	if err != nil {
		return PasskeyRegistration{}, err
	}
	if err := s.checkPasswordForChange(ctx, p, password); err != nil {
		return PasskeyRegistration{}, err
	}
	if err := s.requireVerifiedForChange(ctx, p); err != nil {
		return PasskeyRegistration{}, err
	}
	if name, err = passkeyName(name); err != nil {
		return PasskeyRegistration{}, err
	}
	var count int64
	var user passkeyUser
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		if count, err = s.queries.CountPasskeys(ctx, tx, uuid.UUID(p.user)); err != nil {
			return err
		}
		user, err = s.passkeyUser(ctx, tx, uuid.UUID(p.user))
		return err
	}); err != nil {
		return PasskeyRegistration{}, err
	}
	if count >= maxPasskeysPerUser {
		return PasskeyRegistration{}, ErrTooManyPasskeys
	}
	selection := protocol.AuthenticatorSelection{
		ResidentKey:      protocol.ResidentKeyRequirementRequired,
		UserVerification: protocol.VerificationRequired,
	}
	var exclusions []protocol.CredentialDescriptor
	for _, c := range user.creds {
		exclusions = append(exclusions, c.Descriptor())
	}
	options, session, err := wa.BeginRegistration(user,
		webauthn.WithAuthenticatorSelection(selection), webauthn.WithExclusions(exclusions))
	if err != nil {
		return PasskeyRegistration{}, fmt.Errorf("%w: %w", ErrPasskeyFailed, err)
	}
	flow, err := s.insertWebAuthnFlow(ctx, site, "register", p, session, name)
	if err != nil {
		return PasskeyRegistration{}, err
	}
	return PasskeyRegistration{PublicKey: options, FlowID: flow}, nil
}

// FinishPasskeyRegistration verifies the attested credential, stores it, raises the factor version (every other
// session ends) and issues this browser a session that has passed the second factor, with new recovery codes when TOTP
// is on.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, r *http.Request, p Principal, flowID string, ip netip.Addr, userAgent string) (Passkey, Issued, []string, error) {
	site, _, err := s.passkeySite(r.Host)
	if err != nil {
		return Passkey{}, Issued{}, nil, err
	}
	flow, err := s.consumeWebAuthnFlow(ctx, site, flowID, "register", p)
	if err != nil {
		return Passkey{}, Issued{}, nil, err
	}
	user, err := s.accountPasskeyUser(ctx, p.account, p.user)
	if err != nil {
		return Passkey{}, Issued{}, nil, err
	}
	passkey, credential, transports, err := s.verifyPasskeyRegistration(site, flow, user, r)
	if err != nil {
		return Passkey{}, Issued{}, nil, err
	}
	issued, codes, err := s.storePasskey(ctx, p, site, flow, passkey, credential, transports, ip, userAgent)
	if err != nil {
		return Passkey{}, Issued{}, nil, err
	}
	return passkey, issued, codes, nil
}

// verifyPasskeyRegistration checks the attestation against the relying party the ceremony began with, not the snapshot
// that may have moved since.
func (s *Service) verifyPasskeyRegistration(site Site, flow FindWebAuthnFlowRow, user passkeyUser, r *http.Request) (Passkey, *webauthn.Credential, []string, error) {
	name, err := passkeyName(flow.Name)
	if err != nil {
		return Passkey{}, nil, nil, err
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID: flow.RelyingPartyID, RPDisplayName: site.Name, RPOrigins: []string{flow.Origin},
	})
	if err != nil {
		return Passkey{}, nil, nil, fmt.Errorf("build the passkey relying party: %w", err)
	}
	credential, err := wa.FinishRegistration(user, sessionData(flow, user.user), r)
	if err != nil {
		return Passkey{}, nil, nil, fmt.Errorf("%w: %w", ErrPasskeyFailed, err)
	}
	transports := make([]string, 0, len(credential.Transport))
	for _, t := range credential.Transport {
		transports = append(transports, string(t))
	}
	passkey := Passkey{
		ID: PasskeyID(uuid.NewV7()), Name: name, CreatedAt: s.now(),
		LastUsedAt: nil, BackupEligible: credential.Flags.BackupEligible,
	}
	return passkey, credential, transports, nil
}

// storePasskey rechecks the site under its lock, stores the credential and issues the verified session.
func (s *Service) storePasskey(ctx context.Context, p Principal, site Site, flow FindWebAuthnFlowRow, passkey Passkey, credential *webauthn.Credential, transports []string, ip netip.Addr, userAgent string) (Issued, []string, error) {
	var issued Issued
	var codes []string
	err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		if err := s.passkeySiteHeld(ctx, tx, site, flow); err != nil {
			return err
		}
		locked, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion)
		if err != nil {
			return err
		}
		issued, codes, err = s.addPasskey(ctx, tx, p, site, passkey, credential, transports, locked, ip, userAgent)
		return err
	})
	if err != nil {
		return Issued{}, nil, err
	}
	return issued, codes, nil
}

// passkeySiteHeld refuses a credential when the site moved off the relying party the ceremony was begun for: a
// registration that began before an operator changed the switch or the login domain must not store it.
func (s *Service) passkeySiteHeld(ctx context.Context, tx pg.Tx, site Site, flow FindWebAuthnFlowRow) error {
	rows, err := s.queries.LockSite(ctx, tx, uuid.UUID(site.ID))
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return ErrNotFound
	}
	current, err := s.queries.ReadPasskeySite(ctx, tx, uuid.UUID(site.ID))
	if err != nil {
		return err
	}
	if !current.Passkeys || current.LoginDomain != flow.RelyingPartyID || current.LoginOrigin != flow.Origin {
		return ErrPasskeysOff
	}
	return nil
}

// addPasskey inserts the credential under the user's lock and finishes the change: the factor version rises and the
// verified session, with new recovery codes when TOTP is on, is issued.
func (s *Service) addPasskey(ctx context.Context, tx pg.Tx, p Principal, site Site, passkey Passkey, credential *webauthn.Credential, transports []string, locked LockUserRow, ip netip.Addr, userAgent string) (Issued, []string, error) {
	// The count at begin is a fast refusal; this one is under the user's lock, so two ceremonies that began together
	// cannot leave the user with more than the bound.
	count, err := s.queries.CountPasskeys(ctx, tx, uuid.UUID(p.user))
	if err != nil {
		return Issued{}, nil, err
	}
	if count >= maxPasskeysPerUser {
		return Issued{}, nil, ErrTooManyPasskeys
	}
	if err := s.queries.InsertPasskeyCredential(ctx, tx, InsertPasskeyCredentialParams{
		ID: uuid.UUID(passkey.ID), SiteID: uuid.UUID(site.ID), AccountID: uuid.UUID(p.account), UserID: uuid.UUID(p.user),
		CredentialID: credential.ID, PublicKey: credential.PublicKey, SignCount: int64(credential.Authenticator.SignCount),
		Transports: transports, BackupEligible: credential.Flags.BackupEligible, BackupState: credential.Flags.BackupState,
		Name: passkey.Name,
	}); err != nil {
		if _, duplicate := pg.Duplicate(err); duplicate {
			return Issued{}, nil, ErrPasskeyFailed
		}
		return Issued{}, nil, err
	}
	issued, codes, err := s.finishFactorChange(ctx, tx, locked, factorFinish{
		session: newSession{
			user: uuid.UUID(p.user), account: uuid.UUID(p.account), site: site, surface: Console, level: levelMFA,
			ip: ip, userAgent: userAgent,
		},
		totpOn: locked.Mfa,
	})
	if err != nil {
		return Issued{}, nil, err
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: audit.User(uuid.UUID(p.user)), Action: "user.passkey_add",
		Target: audit.Target{Module: "accounts", Kind: "user", ID: p.user.TypeID(), Site: uuid.UUID(site.ID), Account: uuid.UUID(p.account)},
		Detail: map[string]any{"passkey_id": passkey.ID.TypeID()},
	}); err != nil {
		return Issued{}, nil, err
	}
	return issued, codes, nil
}

// BeginPasskeySignIn starts a discoverable sign-in. The credential carries the user handle, so no email is asked for
// and an anonymous caller learns nothing about which addresses exist.
func (s *Service) BeginPasskeySignIn(ctx context.Context, host string, ip netip.Addr) (PasskeyChallenge, error) {
	site, wa, err := s.passkeySite(host)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	if err := s.fromIP(ctx, site.ID, ip); err != nil {
		return PasskeyChallenge{}, err
	}
	options, session, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return PasskeyChallenge{}, fmt.Errorf("%w: %w", ErrPasskeyFailed, err)
	}
	flow, err := s.insertWebAuthnFlow(ctx, site, "sign_in", Principal{}, session, "")
	if err != nil {
		return PasskeyChallenge{}, err
	}
	return PasskeyChallenge{PublicKey: options, FlowID: flow}, nil
}

// FinishPasskeySignIn verifies a discoverable assertion and issues a session that has already passed the second
// factor: the authenticator verified the person and proved possession of the credential.
func (s *Service) FinishPasskeySignIn(ctx context.Context, r *http.Request, flowID string, ip netip.Addr, userAgent string) (Issued, error) {
	site, wa, err := s.passkeySite(r.Host)
	if err != nil {
		return Issued{}, err
	}
	if err := s.fromIP(ctx, site.ID, ip); err != nil {
		return Issued{}, err
	}
	flow, err := s.consumeWebAuthnFlow(ctx, site, flowID, "sign_in", Principal{})
	if err != nil {
		return Issued{}, err
	}
	user, credential, err := wa.FinishPasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		return s.passkeyLoginUser(ctx, site, rawID, userHandle)
	}, sessionData(flow, uuid.UUID{}), r)
	if err != nil {
		return Issued{}, fmt.Errorf("%w: %w", ErrPasskeyFailed, err)
	}
	won, ok := user.(passkeyUser)
	if !ok {
		return Issued{}, ErrPasskeyFailed
	}
	if err := s.usePasskey(ctx, won.account, won.user, credential); err != nil {
		return Issued{}, err
	}
	return s.issuePasskeySession(ctx, site, won, ip, userAgent)
}

// passkeyLoginUser finds the user a discoverable assertion names, in the site the ceremony belongs to.
func (s *Service) passkeyLoginUser(ctx context.Context, site Site, rawID, userHandle []byte) (webauthn.User, error) {
	if len(userHandle) != 16 {
		return nil, ErrPasskeyFailed
	}
	userID := uuid.UUID(userHandle)
	var tenant []PasskeyTenantRow
	if err := s.db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		var err error
		tenant, err = s.queries.PasskeyTenant(ctx, tx, rawID)
		return err
	}); err != nil {
		return nil, err
	}
	if len(tenant) == 0 || tenant[0].UserID != userID || tenant[0].SiteID != uuid.UUID(site.ID) {
		return nil, ErrPasskeyFailed
	}
	user, err := s.accountPasskeyUser(ctx, AccountID(tenant[0].AccountID), UserID(userID))
	if err != nil {
		return nil, err
	}
	if !user.active {
		return nil, ErrPasskeyFailed
	}
	return user, nil
}

// issuePasskeySession issues the session a discoverable sign-in earned, after the credential's counter was stored.
func (s *Service) issuePasskeySession(ctx context.Context, site Site, user passkeyUser, ip netip.Addr, userAgent string) (Issued, error) {
	var issued Issued
	err := s.db.Tx(ctx, pg.AccountScope(user.account), func(tx pg.Tx) error {
		locked, err := s.lockUser(ctx, tx, user.user, user.version)
		if err != nil {
			return err
		}
		issued, err = s.issue(ctx, tx, newSession{
			user: user.user, account: user.account, site: site, surface: Console, level: levelMFA,
			factorVersion: locked.FactorVersion, ip: ip, userAgent: userAgent,
		})
		if err != nil {
			return err
		}
		return (securityAttempt{site: site.ID, user: user.user, account: user.account, session: issued.session, surface: Console}).record(ctx, tx, "security.sign_in")
	})
	if err != nil {
		return Issued{}, err
	}
	return issued, nil
}

// BeginPasskeyMFA starts the passkey factor of a pending sign-in. The pending session is the only credential the
// caller needs; the assertion proves the second factor.
func (s *Service) BeginPasskeyMFA(ctx context.Context, r *http.Request, p Principal) (PasskeyChallenge, error) {
	if !p.Pending() {
		return PasskeyChallenge{}, ErrNoSession
	}
	site, wa, err := s.passkeySite(r.Host)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	user, err := s.accountPasskeyUser(ctx, p.account, p.user)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	if len(user.creds) == 0 {
		return PasskeyChallenge{}, ErrPasskeyFailed
	}
	options, session, err := wa.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return PasskeyChallenge{}, fmt.Errorf("%w: %w", ErrPasskeyFailed, err)
	}
	flow, err := s.insertWebAuthnFlow(ctx, site, "mfa", p, session, "")
	if err != nil {
		return PasskeyChallenge{}, err
	}
	return PasskeyChallenge{PublicKey: options, FlowID: flow}, nil
}

// FinishPasskeyMFA turns the pending session into a verified one after the assertion. A failed assertion counts one of
// the pending session's attempts and one of the site's failures, as a wrong code does: one challenge cannot be retried
// without bound, and the failure is audited as security.mfa_failed.
func (s *Service) FinishPasskeyMFA(ctx context.Context, r *http.Request, p Principal, flowID string, ip netip.Addr, userAgent string) (out Issued, err error) {
	if !p.Pending() {
		return Issued{}, ErrNoSession
	}
	defer func() { s.failed(ctx, p.securityAttempt(), "security.mfa_failed", &err) }()
	site, wa, err := s.passkeySite(r.Host)
	if err != nil {
		return Issued{}, err
	}
	// The pending session's token hash is what lockPending counts attempts by; the guard already checked its CSRF.
	_, tokenHash, _, err := s.sessionOf(ctx, r)
	if err != nil {
		return Issued{}, err
	}
	flow, err := s.consumeWebAuthnFlow(ctx, site, flowID, "mfa", p)
	if err != nil {
		return Issued{}, err
	}
	user, err := s.accountPasskeyUser(ctx, p.account, p.user)
	if err != nil {
		return Issued{}, err
	}
	// A body that does not parse is a form error, not a guessed factor, so it is refused without a failure record.
	// Only an assertion that parsed but did not verify against this session's live flow counts.
	parsed, err := protocol.ParseCredentialRequestResponse(r)
	if err != nil {
		return Issued{}, fmt.Errorf("%w: %w", ErrPasskeyFailed, err)
	}
	credential, err := wa.ValidateLogin(user, sessionData(flow, uuid.UUID(p.user)), parsed)
	if err != nil {
		// One challenge is one attempt: a failed assertion spends the same budget a wrong code does. The assertion's
		// error is what the caller acts on; a failure to record the attempt is joined so it is not silently lost.
		return Issued{}, errors.Join(fmt.Errorf("%w: %w", ErrPasskeyFailed, err), errPasskeyAssertion, s.spendMFAAttempt(ctx, p))
	}
	if err := s.usePasskey(ctx, user.account, user.user, credential); err != nil {
		return Issued{}, err
	}
	return s.passkeyMFAVerified(ctx, p, site, tokenHash, ip, userAgent)
}

// spendMFAAttempt records the failed assertion against the pending session's budget.
func (s *Service) spendMFAAttempt(ctx context.Context, p Principal) error {
	return s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		return s.queries.CountMFAAttempt(ctx, tx, uuid.UUID(p.session))
	})
}

// passkeyMFAVerified replaces the pending session with a verified one after the assertion.
func (s *Service) passkeyMFAVerified(ctx context.Context, p Principal, site Site, tokenHash []byte, ip netip.Addr, userAgent string) (Issued, error) {
	var out Issued
	err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		pending, locked, err := s.lockPending(ctx, tx, uuid.UUID(p.user), tokenHash,
			s.snapshot.Load().sites[p.site].limits.challengeAttempts)
		if err != nil {
			return err
		}
		revoked, err := s.queries.RevokeSession(ctx, tx, pending.ID)
		if err != nil || revoked != 1 {
			return errors.Join(err, ErrNoSession)
		}
		out, err = s.issue(ctx, tx, newSession{
			user: uuid.UUID(p.user), account: uuid.UUID(p.account), site: site, surface: Console, level: levelMFA,
			factorVersion: locked.FactorVersion, ip: ip, userAgent: userAgent,
		})
		if err != nil {
			return err
		}
		return (securityAttempt{site: p.site, user: uuid.UUID(p.user), account: uuid.UUID(p.account), session: out.session, surface: p.surface}).record(ctx, tx, "security.mfa")
	})
	if err != nil {
		return Issued{}, err
	}
	return out, nil
}

// DeletePasskey removes a credential. The current password is required; the factor version rises, every other session
// ends, and this browser gets a new session with the level it had and, with TOTP on, new recovery codes.
func (s *Service) DeletePasskey(ctx context.Context, p Principal, id PasskeyID, password string, ip netip.Addr, userAgent string) (Issued, []string, error) {
	if err := s.checkPasswordForChange(ctx, p, password); err != nil {
		return Issued{}, nil, err
	}
	if err := s.requireVerifiedForChange(ctx, p); err != nil {
		return Issued{}, nil, err
	}
	var issued Issued
	var codes []string
	err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		locked, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion)
		if err != nil {
			return err
		}
		removed, err := s.queries.DeletePasskey(ctx, tx, DeletePasskeyParams{ID: uuid.UUID(id), UserID: uuid.UUID(p.user)})
		if err != nil {
			return err
		}
		if removed != 1 {
			return ErrNotFound
		}
		issued, codes, err = s.finishFactorChange(ctx, tx, locked, factorFinish{
			session: newSession{
				user: uuid.UUID(p.user), account: uuid.UUID(p.account), site: s.snapshot.Load().sites[p.site],
				surface: p.surface, level: resumedLevel(p), ip: ip, userAgent: userAgent,
			},
			totpOn: locked.Mfa,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{
			Actor: audit.User(uuid.UUID(p.user)), Action: "user.passkey_remove",
			Target: audit.Target{Module: "accounts", Kind: "user", ID: p.user.TypeID(), Site: uuid.UUID(p.site), Account: uuid.UUID(p.account)},
			Detail: map[string]any{"passkey_id": id.TypeID()},
		})
	})
	if err != nil {
		return Issued{}, nil, err
	}
	return issued, codes, nil
}

// checkPasswordForChange spends the same per-user failure budget as password changes and TOTP: a signed-in page must
// not become an unbounded password oracle through the passkey endpoints.
func (s *Service) checkPasswordForChange(ctx context.Context, p Principal, password string) (err error) {
	defer func() { s.failed(ctx, p.securityAttempt(), "security.factor_failed", &err) }()
	l := s.snapshot.Load().sites[p.site].limits
	return failing(s, ctx, s.userLimit(p.site, uuid.UUID(p.user), l), func() error {
		return s.checkPassword(ctx, p, password)
	})
}

// Passkeys lists a user's credentials, oldest first.
func (s *Service) Passkeys(ctx context.Context, p Principal) ([]Passkey, error) {
	var rows []ListPasskeysRow
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		rows, err = s.queries.ListPasskeys(ctx, tx, uuid.UUID(p.user))
		return err
	}); err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	out := make([]Passkey, 0, len(rows))
	for _, r := range rows {
		out = append(out, Passkey{
			ID: PasskeyID(r.ID), Name: r.Name, CreatedAt: r.CreatedAt.Time,
			LastUsedAt: r.LastUsedAt, BackupEligible: r.BackupEligible,
		})
	}
	return out, nil
}
