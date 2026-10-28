package accounts

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/netip"
	"strings"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
)

// ErrInvalidReset is identical for a wrong, expired, spent, superseded or cross-site reset token.
var ErrInvalidReset = errors.New("accounts: invalid or expired password reset link")

// PasswordResetRequest is accepted without an account lookup. Language is zh or en (empty defaults to en).
type PasswordResetRequest struct {
	Host, Email, Language string
	ClientIP              netip.Addr
}

// RequestPasswordReset does exactly the same database and queue work for known and unknown addresses.
func (s *Service) RequestPasswordReset(ctx context.Context, r PasswordResetRequest) error {
	d, snap, err := s.domain(r.Host)
	if err != nil {
		return err
	}
	if d.surface != Console {
		return ErrUnknownHost
	}
	if err := s.recoveryReady(); err != nil {
		return err
	}
	if err := s.fromIP(ctx, d.site, r.ClientIP); err != nil {
		return err
	}
	email := normalEmail(r.Email)
	if !emailAllowed(email) {
		return ErrBadEmail
	}
	limit := s.emailLimit(d.site, email, snap.sites[d.site].limits)
	limit.bucket, limit.kind = s.guards.resets, "reset_email"
	if _, wait := takeAttempt(s, ctx, limit); wait > 0 {
		return &TooManyAttempts{RetryAfter: wait}
	}
	hash, id := sha256.Sum256([]byte(email)), uuid.NewV7()
	return s.db.Tx(ctx, pg.SiteScope(uuid.UUID(d.site)), func(tx pg.Tx) error {
		if err := s.queries.RequestPasswordReset(ctx, tx, RequestPasswordResetParams{
			SiteID: uuid.UUID(d.site), EmailHash: hash[:], ID: id, Email: &email, Domain: strings.ToLower(r.Host), Language: passwordLanguage(r.Language),
		}); err != nil {
			return err
		}
		_, err := s.recovery.queue.InsertManyTx(ctx, tx, []river.InsertManyParams{{Args: passwordResetArgs{ID: id}}})
		return err
	})
}

func passwordLanguage(language string) string {
	if language == "zh" {
		return "zh"
	}
	return "en"
}

// ResetPassword consumes a link and changes its user's password atomically, without issuing a session or bypassing MFA.
func (s *Service) ResetPassword(ctx context.Context, host, token, password, language string, ip netip.Addr) error {
	d, _, err := s.domain(host)
	if err != nil {
		return err
	}
	if d.surface != Console {
		return ErrUnknownHost
	}
	if err := s.recoveryReady(); err != nil {
		return err
	}
	if err := s.fromIP(ctx, d.site, ip); err != nil {
		return err
	}
	if !passwordAllowed(password) {
		return ErrWeakPassword
	}
	if len(token) != 43 {
		return ErrInvalidReset
	}
	hash := sha256.Sum256([]byte(token))
	var found []PasswordResetTenantRow
	if err := s.db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		found, err = s.queries.PasswordResetTenant(ctx, tx, PasswordResetTenantParams{Domain: strings.ToLower(host), TokenHash: hash[:]})
		return err
	}); err != nil {
		return err
	}
	if len(found) == 0 {
		return ErrInvalidReset
	}
	var passwordHash string
	if err := s.hash(ctx, func() { passwordHash = hashPassword(password) }); err != nil {
		return err
	}
	return s.consumeReset(ctx, d.site, found[0], hash[:], passwordHash, language)
}

func (s *Service) consumeReset(ctx context.Context, site SiteID, target PasswordResetTenantRow, tokenHash []byte, passwordHash, language string) error {
	return s.db.Tx(ctx, pg.SiteScope(uuid.UUID(site)), func(tx pg.Tx) error {
		// Both issuance and consumption lock the request before the user. Reissue takes only the request lock.
		requests, err := s.queries.LockPasswordResetRequest(ctx, tx, target.ID)
		if err != nil {
			return err
		}
		if len(requests) == 0 || !requests[0].Unexpired {
			return ErrInvalidReset
		}
		users, err := s.queries.LockUser(ctx, tx, target.UserID)
		if err != nil {
			return err
		}
		if len(users) == 0 || !users[0].Active {
			return ErrInvalidReset
		}
		n, err := s.queries.ConsumePasswordResetToken(ctx, tx, ConsumePasswordResetTokenParams{
			ID: target.ID, UserID: target.UserID, TokenHash: tokenHash, FactorVersion: users[0].FactorVersion,
		})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrInvalidReset
		}
		return s.setPassword(ctx, tx, target.UserID, target.AccountID, uuid.UUID(site), passwordHash, language)
	})
}

// PasswordChange is a signed-in change; the current device receives a new session with its existing level.
type PasswordChange struct {
	CurrentPassword, NewPassword, Language string
	ClientIP                               netip.Addr
	UserAgent                              string
}

// ChangePassword rechecks the current password outside the user lock, then refuses any intervening factor change.
func (s *Service) ChangePassword(ctx context.Context, p Principal, c PasswordChange) (Issued, []string, error) {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return Issued{}, nil, ErrNoSession
	}
	if err := s.recoveryReady(); err != nil {
		return Issued{}, nil, err
	}
	if !passwordAllowed(c.NewPassword) {
		return Issued{}, nil, ErrWeakPassword
	}
	l := s.snapshot.Load().sites[p.site].limits
	var issued Issued
	var codes []string
	err := failing(s, ctx, s.userLimit(p.site, uuid.UUID(p.user), l), func() error {
		if err := s.checkPassword(ctx, p, c.CurrentPassword); err != nil {
			return err
		}
		var hash string
		if err := s.hash(ctx, func() { hash = hashPassword(c.NewPassword) }); err != nil {
			return err
		}
		var err error
		issued, codes, err = s.replacePassword(ctx, p, c, hash)
		return err
	})
	if err != nil {
		return Issued{}, nil, err
	}
	return issued, codes, nil
}

func (s *Service) replacePassword(ctx context.Context, p Principal, c PasswordChange, hash string) (Issued, []string, error) {
	var issued Issued
	var codes []string
	err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		locked, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion)
		if err != nil {
			return err
		}
		if locked.Mfa && !p.Verified() {
			return ErrForbidden
		}
		if err := s.setPassword(ctx, tx, uuid.UUID(p.user), uuid.UUID(p.account), uuid.UUID(p.site), hash, c.Language); err != nil {
			return err
		}
		if locked.Mfa {
			codes, err = s.insertRecoveryCodes(ctx, tx, uuid.UUID(p.user), uuid.UUID(p.account), uuid.UUID(p.site), locked.FactorVersion+1)
			if err != nil {
				return err
			}
		}
		issued, err = s.issue(ctx, tx, newSession{
			user: uuid.UUID(p.user), account: uuid.UUID(p.account), site: s.snapshot.Load().sites[p.site], surface: Console,
			level: p.level, factorVersion: locked.FactorVersion + 1, ip: c.ClientIP, userAgent: c.UserAgent,
		})
		return err
	})
	if err != nil {
		return Issued{}, nil, err
	}
	return issued, codes, nil
}

func (s *Service) setPassword(ctx context.Context, tx pg.Tx, user, account, site uuid.UUID, hash, language string) error {
	email, err := s.queries.PasswordRecipient(ctx, tx, user)
	if err != nil {
		return err
	}
	if !emailAllowed(email) {
		return ErrBadEmail
	}
	if err := s.queries.SetPassword(ctx, tx, SetPasswordParams{ID: user, PasswordHash: hash}); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Event{Actor: audit.User(user), Action: "user.password_set", Target: userTarget(site, account, user)}); err != nil {
		return err
	}
	return s.recovery.mailer.Enqueue(ctx, tx, mail.Tenant{Site: site, Account: account},
		passwordMail(email, s.snapshot.Load().sites[SiteID(site)].Name, passwordLanguage(language), ""))
}
