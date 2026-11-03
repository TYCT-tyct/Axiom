package accounts

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"unicode"
	"uuid"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"axiom/internal/platform/pg"
)

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

// passkeyUser reads a user and its credentials in an account or site scope.
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

// accountPasskeyUser reads a user and its credentials in the account scope of account.
func (s *Service) accountPasskeyUser(ctx context.Context, account AccountID, user UserID) (passkeyUser, error) {
	var out passkeyUser
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(account)), func(tx pg.Tx) error {
		var err error
		out, err = s.passkeyUser(ctx, tx, uuid.UUID(user))
		return err
	}); err != nil {
		return passkeyUser{}, err
	}
	return out, nil
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
	if err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		return s.insertWebAuthnFlowTx(ctx, tx, id, site, kind, p, session, sessionName)
	}); err != nil {
		return "", fmt.Errorf("begin the passkey ceremony: %w", err)
	}
	return id.String(), nil
}

// insertWebAuthnFlowTx writes a begin's row in a transaction the caller already holds, so a budget check and the flow
// it allows commit or roll back together.
func (s *Service) insertWebAuthnFlowTx(ctx context.Context, tx pg.Tx, id uuid.UUID, site Site, kind string, p Principal, session *webauthn.SessionData, sessionName string) error {
	allowed := session.AllowedCredentialIDs
	if allowed == nil {
		allowed = [][]byte{}
	}
	return s.queries.InsertWebAuthnFlow(ctx, tx, InsertWebAuthnFlowParams{
		ID: id, SiteID: uuid.UUID(site.ID), Kind: kind, AccountID: optionalUUID(p.account), UserID: optionalUUID(p.user),
		SessionID: optionalUUID(p.session), Challenge: session.Challenge,
		RelyingPartyID: site.LoginDomain, Origin: site.LoginOrigin, Name: sessionName, AllowedCredentialIds: allowed,
	})
}

// consumeWebAuthnFlow reads a begin's row in the site's transaction, checks it belongs to this ceremony and this
// session, then spends it: one begin is one finish attempt.
func (s *Service) consumeWebAuthnFlow(ctx context.Context, site Site, flowID, kind string, p Principal) (FindWebAuthnFlowRow, error) {
	var row FindWebAuthnFlowRow
	if err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site.ID)), func(tx pg.Tx) error {
		var err error
		if row, err = s.findWebAuthnFlow(ctx, tx, site, flowID, kind, p); err != nil {
			return err
		}
		affected, err := s.queries.ConsumeWebAuthnFlow(ctx, tx, row.ID)
		if err == nil && affected != 1 {
			return ErrPasskeyFailed
		}
		return err
	}); err != nil {
		return FindWebAuthnFlowRow{}, fmt.Errorf("consume the passkey ceremony: %w", err)
	}
	return row, nil
}

// findWebAuthnFlow reads a begin's row and checks it belongs to this ceremony and this session. It does not spend it,
// so a caller can consume it in the same transaction as the work that must roll back with it.
func (s *Service) findWebAuthnFlow(ctx context.Context, tx pg.Tx, site Site, flowID, kind string, p Principal) (FindWebAuthnFlowRow, error) {
	id, err := uuid.Parse(flowID)
	if err != nil {
		return FindWebAuthnFlowRow{}, ErrPasskeyFailed
	}
	rows, err := s.queries.FindWebAuthnFlow(ctx, tx, FindWebAuthnFlowParams{ID: id, SiteID: uuid.UUID(site.ID)})
	if err != nil {
		return FindWebAuthnFlowRow{}, err
	}
	if len(rows) == 0 {
		return FindWebAuthnFlowRow{}, ErrPasskeyFailed
	}
	// Check everything before spending the flow: a request with the wrong kind or session must not burn a
	// ceremony that belongs to someone else.
	if err := webauthnFlowOwnedBy(rows[0], kind, p); err != nil {
		return FindWebAuthnFlowRow{}, err
	}
	return rows[0], nil
}

// webauthnFlowOwnedBy reports whether the flow is live and belongs to this ceremony and session.
func webauthnFlowOwnedBy(flow FindWebAuthnFlowRow, kind string, p Principal) error {
	if !flow.Unexpired || flow.Kind != kind {
		return ErrPasskeyFailed
	}
	if kind == "sign_in" {
		return nil
	}
	if flow.UserID == nil || flow.AccountID == nil || flow.SessionID == nil ||
		*flow.UserID != uuid.UUID(p.user) || *flow.AccountID != uuid.UUID(p.account) ||
		*flow.SessionID != uuid.UUID(p.session) {
		return ErrPasskeyFailed
	}
	return nil
}

// sessionData rebuilds the ceremony state go-webauthn verifies against from what the begin stored.
func sessionData(flow FindWebAuthnFlowRow, user uuid.UUID) webauthn.SessionData {
	var handle []byte
	if user != (uuid.UUID{}) {
		handle = user[:]
	}
	return webauthn.SessionData{
		Challenge: flow.Challenge, RelyingPartyID: flow.RelyingPartyID, Origin: flow.Origin,
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
