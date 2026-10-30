package accounts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode"
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

// passkeyUser is a user as WebAuthn sees one: the opaque handle is the user's UUID, and the credentials are the
// public-key records stored for it.
type passkeyUser struct {
	user, account uuid.UUID
	email         string
	active        bool
	version       int64
	creds         []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { id := u.user; return id[:] }
func (u passkeyUser) WebAuthnName() string                       { return u.email }
func (u passkeyUser) WebAuthnDisplayName() string                { return u.email }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// BeginPasskeyRegistration starts adding a credential. The current password is required, and a session that has not
// passed the user's second factor is refused: a passkey can sign in on its own, so adding one must not be possible with
// the password alone.
func (s *Service) BeginPasskeyRegistration(ctx context.Context, r *http.Request, p Principal, name, password string) (PasskeyRegistration, error) {
	site, wa, err := s.passkeySite(r.Host)
	if err != nil {
		return PasskeyRegistration{}, err
	}
	if err := s.checkPassword(ctx, p, password); err != nil {
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
		ResidentKey:      protocol.ResidentKeyRequirementPreferred,
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
// session ends) and issues this browser a session that has passed the second factor.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, r *http.Request, p Principal, flowID string, ip netip.Addr, userAgent string) (Passkey, Issued, error) {
	site, wa, err := s.passkeySite(r.Host)
	if err != nil {
		return Passkey{}, Issued{}, err
	}
	flow, err := s.consumeWebAuthnFlow(ctx, site, flowID, "register", p)
	if err != nil {
		return Passkey{}, Issued{}, err
	}
	var user passkeyUser
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		user, err = s.passkeyUser(ctx, tx, uuid.UUID(p.user))
		return err
	}); err != nil {
		return Passkey{}, Issued{}, err
	}
	name, err := passkeyName(flow.Name)
	if err != nil {
		return Passkey{}, Issued{}, err
	}
	credential, err := wa.FinishRegistration(user, sessionData(site, flow, uuid.UUID(p.user)), r)
	if err != nil {
		return Passkey{}, Issued{}, fmt.Errorf("%w: %w", ErrPasskeyFailed, err)
	}
	transports := make([]string, 0, len(credential.Transport))
	for _, t := range credential.Transport {
		transports = append(transports, string(t))
	}
	passkey := Passkey{
		ID: PasskeyID(uuid.NewV7()), Name: name, CreatedAt: s.now(),
		LastUsedAt: nil, BackupEligible: credential.Flags.BackupEligible,
	}
	var issued Issued
	err = s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		locked, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion)
		if err != nil {
			return err
		}
		// The count at begin is a fast refusal; this one is under the user's lock, so two ceremonies that began
		// together cannot leave the user with more than the bound.
		count, err := s.queries.CountPasskeys(ctx, tx, uuid.UUID(p.user))
		if err != nil {
			return err
		}
		if count >= maxPasskeysPerUser {
			return ErrTooManyPasskeys
		}
		if err := s.queries.InsertPasskeyCredential(ctx, tx, InsertPasskeyCredentialParams{
			ID: uuid.UUID(passkey.ID), SiteID: uuid.UUID(site.ID), AccountID: uuid.UUID(p.account), UserID: uuid.UUID(p.user),
			CredentialID: credential.ID, PublicKey: credential.PublicKey, SignCount: int64(credential.Authenticator.SignCount),
			Transports: transports, BackupEligible: credential.Flags.BackupEligible, BackupState: credential.Flags.BackupState,
			Name: name,
		}); err != nil {
			if _, duplicate := pg.Duplicate(err); duplicate {
				return ErrPasskeyFailed
			}
			return err
		}
		if err := s.queries.BumpFactorVersion(ctx, tx, uuid.UUID(p.user)); err != nil {
			return err
		}
		issued, err = s.issue(ctx, tx, newSession{
			user: uuid.UUID(p.user), account: uuid.UUID(p.account), site: site, surface: Console, level: levelMFA,
			factorVersion: locked.FactorVersion + 1, ip: ip, userAgent: userAgent,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{
			Actor: audit.User(uuid.UUID(p.user)), Action: "user.passkey_add",
			Target: audit.Target{Module: "accounts", Kind: "user", ID: p.user.TypeID(), Site: uuid.UUID(site.ID), Account: uuid.UUID(p.account)},
			Detail: map[string]any{"passkey_id": passkey.ID.TypeID()},
		})
	})
	if err != nil {
		return Passkey{}, Issued{}, err
	}
	return passkey, issued, nil
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
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
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
		var user passkeyUser
		if err := s.db.Tx(ctx, pg.AccountScope(tenant[0].AccountID), func(tx pg.Tx) error {
			var err error
			user, err = s.passkeyUser(ctx, tx, userID)
			return err
		}); err != nil {
			return nil, err
		}
		if !user.active {
			return nil, ErrPasskeyFailed
		}
		return user, nil
	}
	user, credential, err := wa.FinishPasskeyLogin(handler, sessionData(site, flow, uuid.UUID{}), r)
	if err != nil {
		return Issued{}, fmt.Errorf("%w: %w", ErrPasskeyFailed, err)
	}
	u, ok := user.(passkeyUser)
	if !ok {
		return Issued{}, ErrPasskeyFailed
	}
	if err := s.usePasskey(ctx, u.account, u.user, credential); err != nil {
		return Issued{}, err
	}
	var issued Issued
	err = s.db.Tx(ctx, pg.AccountScope(u.account), func(tx pg.Tx) error {
		locked, err := s.lockUser(ctx, tx, u.user, u.version)
		if err != nil {
			return err
		}
		issued, err = s.issue(ctx, tx, newSession{
			user: u.user, account: u.account, site: site, surface: Console, level: levelMFA,
			factorVersion: locked.FactorVersion, ip: ip, userAgent: userAgent,
		})
		if err != nil {
			return err
		}
		return (securityAttempt{site: site.ID, user: u.user, account: u.account, session: issued.session, surface: Console}).record(ctx, tx, "security.sign_in")
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
	var user passkeyUser
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		user, err = s.passkeyUser(ctx, tx, uuid.UUID(p.user))
		return err
	}); err != nil {
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

// FinishPasskeyMFA turns the pending session into a verified one after the assertion. A failed ceremony counts one of
// the pending session's attempts, as a wrong code does, so one challenge cannot be retried without bound.
func (s *Service) FinishPasskeyMFA(ctx context.Context, r *http.Request, p Principal, flowID string, ip netip.Addr, userAgent string) (out Issued, err error) {
	if !p.Pending() {
		return Issued{}, ErrNoSession
	}
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
	var user passkeyUser
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		user, err = s.passkeyUser(ctx, tx, uuid.UUID(p.user))
		return err
	}); err != nil {
		return Issued{}, err
	}
	credential, err := wa.FinishLogin(user, sessionData(site, flow, uuid.UUID(p.user)), r)
	if err != nil {
		// One challenge is one attempt: a failed assertion spends the same budget a wrong code does.
		_ = s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
			return s.queries.CountMFAAttempt(ctx, tx, uuid.UUID(p.session))
		})
		return Issued{}, fmt.Errorf("%w: %w", ErrPasskeyFailed, err)
	}
	if err := s.usePasskey(ctx, user.account, user.user, credential); err != nil {
		return Issued{}, err
	}
	err = s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
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
// ends, and this browser gets a new session with the level it had.
func (s *Service) DeletePasskey(ctx context.Context, p Principal, id PasskeyID, password string, ip netip.Addr, userAgent string) (Issued, error) {
	if err := s.checkPassword(ctx, p, password); err != nil {
		return Issued{}, err
	}
	if err := s.requireVerifiedForChange(ctx, p); err != nil {
		return Issued{}, err
	}
	var issued Issued
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
		if err := s.queries.BumpFactorVersion(ctx, tx, uuid.UUID(p.user)); err != nil {
			return err
		}
		level := levelPassword
		if p.Verified() {
			level = levelMFA
		}
		issued, err = s.issue(ctx, tx, newSession{
			user: uuid.UUID(p.user), account: uuid.UUID(p.account), site: s.snapshot.Load().sites[p.site], surface: p.surface,
			level: level, factorVersion: locked.FactorVersion + 1, ip: ip, userAgent: userAgent,
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
		return Issued{}, err
	}
	return issued, nil
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

// passkeySite is the console site of host when it is the login domain and turned passkeys on, with a relying party
// built for that one origin.
func (s *Service) passkeySite(host string) (Site, *webauthn.WebAuthn, error) {
	site, err := s.loginDomainSite(host)
	if err != nil {
		return Site{}, nil, err
	}
	if !site.Passkeys {
		return Site{}, nil, ErrPasskeysOff
	}
	wa, err := webauthn.New(&webauthn.Config{RPID: site.LoginDomain, RPDisplayName: site.Name, RPOrigins: []string{site.LoginOrigin}})
	if err != nil {
		return Site{}, nil, fmt.Errorf("build the passkey relying party: %w", err)
	}
	return site, wa, nil
}

// passkeyUser reads a user and its credentials in an account scope.
func (s *Service) passkeyUser(ctx context.Context, tx pg.Tx, user uuid.UUID) (passkeyUser, error) {
	rows, err := s.queries.UserForWebAuthn(ctx, tx, user)
	if err != nil {
		return passkeyUser{}, err
	}
	credentials, err := s.queries.ListUserCredentials(ctx, tx, user)
	if err != nil {
		return passkeyUser{}, err
	}
	creds := make([]webauthn.Credential, 0, len(credentials))
	for _, c := range credentials {
		transports := make([]protocol.AuthenticatorTransport, 0, len(c.Transports))
		for _, t := range c.Transports {
			transports = append(transports, protocol.AuthenticatorTransport(t))
		}
		creds = append(creds, webauthn.Credential{
			ID: c.CredentialID, PublicKey: c.PublicKey,
			Authenticator: webauthn.Authenticator{SignCount: uint32(c.SignCount)}, //nolint:gosec // the column is bounded to 2³²-1
			Flags:         webauthn.CredentialFlags{BackupEligible: c.BackupEligible, BackupState: c.BackupState},
			Transport:     transports,
		})
	}
	return passkeyUser{
		user: user, account: rows.AccountID, email: rows.Email, active: rows.Active,
		version: rows.FactorVersion, creds: creds,
	}, nil
}

// usePasskey records a successful assertion: a refused clone warning, the counter moved forward and the credential
// marked used.
func (s *Service) usePasskey(ctx context.Context, account, user uuid.UUID, credential *webauthn.Credential) error {
	if credential.Authenticator.CloneWarning {
		return fmt.Errorf("%w: the authenticator warned that it may be cloned", ErrPasskeyFailed)
	}
	signCount := int64(credential.Authenticator.SignCount)
	return s.db.Tx(ctx, pg.AccountScope(account), func(tx pg.Tx) error {
		rows, err := s.queries.ListUserCredentials(ctx, tx, user)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if !bytes.Equal(r.CredentialID, credential.ID) {
				continue
			}
			affected, err := s.queries.UsePasskey(ctx, tx, UsePasskeyParams{
				SignCount: signCount, BackupState: credential.Flags.BackupState, ID: r.ID,
			})
			if err != nil {
				return err
			}
			if affected != 1 {
				return fmt.Errorf("%w: the credential was not updated", ErrPasskeyFailed)
			}
			return nil
		}
		return fmt.Errorf("%w: the asserted credential is not stored (%d bytes, %d rows)", ErrPasskeyFailed, len(credential.ID), len(rows))
	})
}

func (s *Service) insertWebAuthnFlow(ctx context.Context, site Site, kind string, p Principal, session *webauthn.SessionData, sessionName string) (string, error) {
	id := uuid.NewV7()
	allowed := session.AllowedCredentialIDs
	if allowed == nil {
		allowed = [][]byte{}
	}
	if err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		return s.queries.InsertWebAuthnFlow(ctx, tx, InsertWebAuthnFlowParams{
			ID: id, SiteID: uuid.UUID(site.ID), Kind: kind, AccountID: optionalUUID(p.account), UserID: optionalUUID(p.user),
			SessionID: optionalUUID(p.session), Challenge: session.Challenge, Name: sessionName,
			AllowedCredentialIds: allowed,
		})
	}); err != nil {
		return "", fmt.Errorf("begin the passkey ceremony: %w", err)
	}
	return id.String(), nil
}

// consumeWebAuthnFlow reads a begin's row in the site's transaction, checks it belongs to this ceremony and this
// session, then spends it: one begin is one finish attempt.
func (s *Service) consumeWebAuthnFlow(ctx context.Context, site Site, flowID, kind string, p Principal) (FindWebAuthnFlowRow, error) {
	id, err := uuid.Parse(flowID)
	if err != nil {
		return FindWebAuthnFlowRow{}, ErrPasskeyFailed
	}
	var rows []FindWebAuthnFlowRow
	if err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		var err error
		rows, err = s.queries.FindWebAuthnFlow(ctx, tx, FindWebAuthnFlowParams{ID: id, SiteID: uuid.UUID(site.ID)})
		if err != nil || len(rows) == 0 {
			return err
		}
		// Check everything before spending the flow: a request with the wrong kind or session must not burn a
		// ceremony that belongs to someone else.
		if !rows[0].Unexpired || rows[0].Kind != kind {
			return ErrPasskeyFailed
		}
		if kind != "sign_in" {
			if rows[0].UserID == nil || rows[0].AccountID == nil || rows[0].SessionID == nil ||
				*rows[0].UserID != uuid.UUID(p.user) || *rows[0].AccountID != uuid.UUID(p.account) ||
				*rows[0].SessionID != uuid.UUID(p.session) {
				return ErrPasskeyFailed
			}
		}
		affected, err := s.queries.ConsumeWebAuthnFlow(ctx, tx, id)
		if err == nil && affected != 1 {
			return ErrPasskeyFailed
		}
		return err
	}); err != nil {
		return FindWebAuthnFlowRow{}, fmt.Errorf("consume the passkey ceremony: %w", err)
	}
	return rows[0], nil
}

// sessionData rebuilds the ceremony state go-webauthn verifies against from what the begin stored.
func sessionData(site Site, flow FindWebAuthnFlowRow, user uuid.UUID) webauthn.SessionData {
	var handle []byte
	if user != (uuid.UUID{}) {
		id := uuid.UUID(user)
		handle = id[:]
	}
	return webauthn.SessionData{
		Challenge: flow.Challenge, RelyingPartyID: site.LoginDomain, Origin: site.LoginOrigin,
		UserID: handle, AllowedCredentialIDs: flow.AllowedCredentialIds, Expires: flow.ExpiresAt.Time,
		CredParams: webauthn.CredentialParametersDefault(), UserVerification: protocol.VerificationRequired,
	}
}

// passkeyName is the user's label for a credential, "Passkey" when they give none.
func passkeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Passkey", nil
	}
	if len([]rune(name)) > 100 {
		return "", ErrPasskeyFailed
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrPasskeyFailed
		}
	}
	return name, nil
}
