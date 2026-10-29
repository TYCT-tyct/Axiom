package accounts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	netmail "net/mail"
	"net/netip"
	"strings"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

var (
	// ErrInvalidInvite is an invite code that does not admit a registration at the request's Host: unknown, of a
	// disabled user, or on a site that does not register by invitation.
	ErrInvalidInvite = errors.New("accounts: invalid invite code")
	// ErrEmailTaken is an email already registered on the site.
	ErrEmailTaken = errors.New("accounts: email already registered")
	// ErrWeakPassword is a password shorter than 12 characters or longer than 256.
	ErrWeakPassword = errors.New("accounts: password must be 12 to 256 characters")
	// ErrBadEmail is an address outside the printable ASCII mailbox syntax accepted by registration and mail.
	ErrBadEmail = errors.New("accounts: not an email address")
	// ErrRegistrationClosed is a site that does not accept new registrations.
	ErrRegistrationClosed = errors.New("accounts: registration is closed")
	// ErrHumanCheck is an open registration without a proof of work that meets the site's difficulty.
	ErrHumanCheck = errors.New("accounts: human check failed")
	// ErrInvalidVerification is a verification link unknown, expired, already used, or for another site.
	ErrInvalidVerification = errors.New("accounts: invalid or expired verification link")
)

// OpenedFunc runs in the transaction that opens an account, once the account exists, so that a module above accounts
// can give the account what it needs to exist there too (billing's balance row) and the account is never seen
// without it. cmd/axiom supplies it: accounts may not import the modules above it.
type OpenedFunc func(ctx context.Context, tx pg.Tx, site, account uuid.UUID) error

// RegisterRequest is a registration at a console domain. An invite code is required on an invite-only site; an open
// site instead takes the proof of work its page solved, and a language for the verification mail.
type RegisterRequest struct {
	Host, InviteCode, Email, Password string
	HumanCheck, Language              string
	ClientIP                          netip.Addr
}

// RegisterResult is a registration that made a user at once (an invitation) or one waiting for its address to be
// verified (open registration). Pending registrations send no session and no user id: the page asks the person to
// click the link, and the user exists only then.
type RegisterResult struct {
	User    UserID
	Pending bool
}

// Register registers at the request's console domain according to the site's mode, read from the database on every
// request so a closed site cannot be admitted from a stale snapshot. An invitation makes the user in one account
// transaction; open registration stores a pending request and mails a single-use link. Both spend one of the IP's
// attempts; open registration also spends one of the email domain's. Open registration does not keep Password: the
// verification page supplies it after the address is proved.
func (s *Service) Register(ctx context.Context, r RegisterRequest) (out RegisterResult, err error) {
	d, snap, err := s.domain(r.Host)
	if err != nil {
		return RegisterResult{}, err
	}
	if d.surface != Console {
		return RegisterResult{}, ErrUnknownHost
	}
	event := securityAttempt{site: d.site, surface: d.surface}
	defer func() { s.failed(ctx, event, "security.registration_failed", &err) }()
	if err := s.fromIP(ctx, d.site, r.ClientIP); err != nil {
		return RegisterResult{}, err
	}
	registration, err := s.registrationSite(ctx, r.Host)
	if err != nil {
		return RegisterResult{}, err
	}
	// The database is authoritative for the domain, while the snapshot above supplied the site's settings. A domain
	// that moved to another site since the snapshot must wait for the reload rather than register in two scopes.
	if SiteID(registration.SiteID) != d.site {
		return RegisterResult{}, ErrUnknownHost
	}
	switch registration.Registration {
	case "invite":
		return s.registerInvite(ctx, d, snap, r)
	case "open":
		return s.registerOpen(ctx, d, snap, registration, r)
	case "closed":
		return RegisterResult{}, ErrRegistrationClosed
	default:
		return RegisterResult{}, fmt.Errorf("register: unknown registration mode %q", registration.Registration)
	}
}

// registerInvite is the invite-only flow that predates open registration.
func (s *Service) registerInvite(ctx context.Context, d domain, snap *snapshot, r RegisterRequest) (RegisterResult, error) {
	email := normalEmail(r.Email)
	if !emailAllowed(email) {
		return RegisterResult{}, ErrBadEmail
	}
	if !passwordAllowed(r.Password) {
		return RegisterResult{}, ErrWeakPassword
	}
	invite, err := s.invite(ctx, r.Host, r.InviteCode)
	if err != nil {
		return RegisterResult{}, err
	}
	var hash string
	if err := s.hash(ctx, func() { hash = hashPassword(r.Password) }); err != nil {
		return RegisterResult{}, err
	}
	user, account := uuid.NewV7(), uuid.NewV7()
	err = s.db.Tx(ctx, pg.AccountScope(account), func(tx pg.Tx) error {
		if err := createUser(ctx, tx, s.queries, s.opened, newUser{
			id: user, account: account, site: invite.SiteID, tier: invite.DefaultTierID, role: snap.roleIDs["owner"],
			email: email, passwordHash: hash, inviter: &invite.InviterUserID,
		}); err != nil {
			return err
		}
		success := securityAttempt{site: d.site, user: user, account: account, surface: d.surface}
		return success.record(ctx, tx, "security.registration")
	})
	if constraint, ok := pg.Duplicate(err); ok && constraint == "users_site_id_email_key" {
		return RegisterResult{}, ErrEmailTaken
	}
	if err != nil {
		return RegisterResult{}, fmt.Errorf("register: %w", err)
	}
	return RegisterResult{User: UserID(user)}, nil
}

// registerOpen stores one pending registration and queues its verification mail. It does not keep the request's
// password: the link's page asks for it after the address is proved. The request always succeeds for a well-formed
// address, whether or not it is registered already: the page and the API must not tell an anonymous caller which
// addresses exist. A duplicate is found when the link is clicked and answered with ErrEmailTaken there.
func (s *Service) registerOpen(ctx context.Context, d domain, snap *snapshot, registration RegistrationSiteRow, r RegisterRequest) (RegisterResult, error) {
	site, ok := snap.sites[SiteID(registration.SiteID)]
	if !ok {
		return RegisterResult{}, ErrUnknownHost
	}
	email := normalEmail(r.Email)
	if !emailAllowed(email) {
		return RegisterResult{}, ErrBadEmail
	}
	emailDomain := domainOf(email)
	if !domainPermitted(site, emailDomain) {
		return RegisterResult{}, ErrEmailDomainNotAllowed
	}
	if site.RegistrationHumanCheck && !proofWorked(site.ID, email, r.HumanCheck, site.RegistrationHumanCheckBits) {
		return RegisterResult{}, ErrHumanCheck
	}
	if err := s.registrationReady(); err != nil {
		return RegisterResult{}, err
	}
	if err := s.domainLimit(ctx, d.site, emailDomain, site.limits); err != nil {
		return RegisterResult{}, err
	}
	emailHash := sha256.Sum256([]byte(email))
	language := r.Language
	if language == "" {
		language = "en"
	}
	var id uuid.UUID
	err := s.db.Tx(ctx, pg.SiteScope(uuid.UUID(d.site)), func(tx pg.Tx) error {
		var err error
		id, err = s.queries.InsertRegistrationRequest(ctx, tx, InsertRegistrationRequestParams{
			ID: uuid.NewV7(), SiteID: uuid.UUID(d.site), EmailHash: emailHash[:], Email: email,
			TierID: registration.DefaultTierID, Domain: strings.ToLower(r.Host), Language: language,
		})
		if err != nil {
			return err
		}
		_, err = s.registration.queue.InsertManyTx(ctx, tx, []river.InsertManyParams{{Args: registrationArgs{ID: id}}})
		return err
	})
	if err != nil {
		return RegisterResult{}, fmt.Errorf("register: %w", err)
	}
	return RegisterResult{Pending: true}, nil
}

// registrationSite reads the site's mode from the database, where a closed or disabled site is authoritative.
func (s *Service) registrationSite(ctx context.Context, host string) (RegistrationSiteRow, error) {
	var row RegistrationSiteRow
	err := s.db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		var err error
		row, err = s.queries.RegistrationSite(ctx, tx, strings.ToLower(host))
		return err
	})
	if errors.Is(err, pg.ErrNoRows) {
		return RegistrationSiteRow{}, ErrUnknownHost
	}
	if err != nil {
		return RegistrationSiteRow{}, fmt.Errorf("register: %w", err)
	}
	return row, nil
}

// registrationReady refuses an open registration when this node cannot send the verification mail.
func (s *Service) registrationReady() error {
	if s.registration == nil || !s.registration.mailer.Configured() {
		return fmt.Errorf("%w: registration email", ErrBusy)
	}
	return nil
}

// domainLimit spends one of the email domain's open registrations on the site. Like the IP's attempts it is counted in
// the shared store while it answers, and each registration counts: a success does not give it back.
func (s *Service) domainLimit(ctx context.Context, site SiteID, emailDomain string, l limits) error {
	limit := loginLimit[siteEmail]{
		bucket: s.guards.domains, key: siteEmail{site, emailDomain}, site: site, kind: domainKind, value: emailDomain,
		size: l.domainBurst, per: time.Hour / time.Duration(l.domainPerHour),
	}
	if _, wait := takeAttempt(s, ctx, limit); wait > 0 {
		return &TooManyAttempts{RetryAfter: wait}
	}
	return nil
}

// newUser is a user and the personal account that comes with them. inviter is nil for a user the command line makes.
type newUser struct {
	id, account, site, tier, role uuid.UUID
	email, passwordHash           string
	inviter                       *uuid.UUID
	verified                      *time.Time
}

// createUser inserts n's account, its tenant registration, its default workspace, the user and the user as the
// account's owner in tx, which must hold the new account's scope, then runs opened. Registration and the command line
// make users through this one body.
func createUser(ctx context.Context, tx pg.Tx, q *Queries, opened OpenedFunc, n newUser) error {
	for _, step := range []func() error{
		func() error {
			return q.InsertAccount(ctx, tx, InsertAccountParams{ID: n.account, SiteID: n.site, Name: truncate(n.email, 100), TierID: n.tier})
		},
		func() error { return audit.RegisterAccount(ctx, tx, n.site, n.account) },
		func() error {
			return q.InsertDefaultWorkspace(ctx, tx, InsertDefaultWorkspaceParams{ID: uuid.NewV7(), SiteID: n.site, AccountID: n.account})
		},
		func() error {
			return q.InsertUser(ctx, tx, InsertUserParams{
				ID: n.id, SiteID: n.site, AccountID: n.account, Email: n.email, PasswordHash: n.passwordHash,
				InviteCode: newInviteCode(), InviterUserID: n.inviter, EmailVerifiedAt: n.verified,
			})
		},
		func() error {
			return q.InsertOwner(ctx, tx, InsertOwnerParams{SiteID: n.site, AccountID: n.account, UserID: n.id, RoleID: n.role})
		},
		func() error {
			if opened == nil {
				return nil
			}
			return opened(ctx, tx, n.site, n.account)
		},
	} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// emailAllowed reports whether email, already normalized, is one the users table takes: 3 to 254 printable ASCII
// characters (what lower() and the length check agree on in every locale), parseable as that exact mailbox.
func emailAllowed(email string) bool {
	if len(email) < 3 || len(email) > 254 {
		return false
	}
	for i := range len(email) {
		if email[i] <= ' ' || email[i] > '~' {
			return false
		}
	}
	address, err := netmail.ParseAddress(email)
	return err == nil && address.Address == email
}

// invite is what code admits at host's site, or ErrInvalidInvite.
func (s *Service) invite(ctx context.Context, host, code string) (InviteTenantRow, error) {
	var invites []InviteTenantRow
	if err := s.db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		var err error
		invites, err = s.queries.InviteTenant(ctx, tx, InviteTenantParams{Domain: strings.ToLower(host), Code: code})
		return err
	}); err != nil {
		return InviteTenantRow{}, fmt.Errorf("register: %w", err)
	}
	if len(invites) == 0 {
		return InviteTenantRow{}, ErrInvalidInvite
	}
	return invites[0], nil
}

// newInviteCode is a user's own invite code: 16 characters, 80 random bits.
func newInviteCode() string {
	return strings.ReplaceAll(newRecoveryCode(), "-", "")
}
