package accounts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
)

var (
	// ErrProfileName is an empty or overlong display name after trimming.
	ErrProfileName = errors.New("accounts: display name must be 1 to 100 characters")
	// ErrEmailChangeExpired is the current pending link past its deadline.
	ErrEmailChangeExpired = errors.New("accounts: email change link expired; request another")
	// ErrEmailChangeInvalid includes a superseded, already used or otherwise invalid link.
	ErrEmailChangeInvalid = errors.New("accounts: email change link invalid, replaced or already used")
)

const emailChangeTTL = 24 * time.Hour

// ChangeDisplayName updates the personal account name used by the console; no credential changes.
func (s *Service) ChangeDisplayName(ctx context.Context, p Principal, name string) error {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return ErrNoSession
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 || strings.ContainsRune(name, 0) {
		return ErrProfileName
	}
	return s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		if _, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion); err != nil {
			return err
		}
		before, err := s.queries.ProfileName(ctx, tx, uuid.UUID(p.account))
		if err != nil || before == name {
			return err
		}
		if err := s.queries.SetProfileName(ctx, tx, SetProfileNameParams{AccountID: uuid.UUID(p.account), Name: name}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{
			Actor: audit.User(uuid.UUID(p.user)), Action: "user.name_changed",
			Target: userTarget(uuid.UUID(p.site), uuid.UUID(p.account), uuid.UUID(p.user)),
			Detail: map[string]any{"before_length": utf8.RuneCountInString(before), "after_length": utf8.RuneCountInString(name)},
		})
	})
}

// RequestEmailChange proves the current password and, when enabled, MFA before revealing address conflicts or mailing.
// The user's lock serializes requests and confirmation; mail.Enqueue only writes the durable outbox, without SMTP.
func (s *Service) RequestEmailChange(ctx context.Context, p Principal, email, password, language string) error {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return ErrNoSession
	}
	if err := s.checkPasswordForChange(ctx, p, password); err != nil {
		return err
	}
	if err := s.recoveryReady(); err != nil {
		return err
	}
	limit := s.emailLimit(p.site, uuid.UUID(p.user).String(), s.snapshot.Load().sites[p.site].limits)
	limit.bucket, limit.kind = s.guards.resets, "email_change"
	if _, wait := takeAttempt(s, ctx, limit); wait > 0 {
		return &TooManyAttempts{RetryAfter: wait}
	}
	email = normalEmail(email)
	if !emailAllowed(email) {
		return ErrBadEmail
	}
	return s.db.Tx(ctx, pg.SiteScope(uuid.UUID(p.site)), func(tx pg.Tx) error {
		return s.issueEmailChange(ctx, tx, p, email, language)
	})
}

// issueEmailChange serializes with confirmation before checking site-wide uniqueness and replacing the pending link.
func (s *Service) issueEmailChange(ctx context.Context, tx pg.Tx, p Principal, email, language string) error {
	locked, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion)
	if err != nil {
		return err
	}
	if secondFactorUnproved(locked, p) {
		return ErrMFARequired
	}
	taken, err := s.queries.UserEmailTaken(ctx, tx, UserEmailTakenParams{SiteID: uuid.UUID(p.site), Email: email})
	if err != nil {
		return err
	}
	if taken {
		return ErrEmailTaken
	}
	token := randomToken()
	hash := sha256.Sum256([]byte(token))
	if err := s.queries.PutEmailChange(ctx, tx, PutEmailChangeParams{
		SiteID: uuid.UUID(p.site), AccountID: uuid.UUID(p.account),
		UserID: uuid.UUID(p.user), Email: email, TokenHash: hash[:], FactorVersion: p.factorVersion,
		Language: passwordLanguage(language), ExpiresAt: s.now().Add(emailChangeTTL).Format(time.RFC3339Nano),
	}); err != nil {
		return err
	}
	sites, err := s.queries.ConsoleSite(ctx, tx, ConsoleSiteParams{SiteID: uuid.UUID(p.site), Domain: s.snapshot.Load().sites[p.site].LoginDomain})
	if err != nil {
		return err
	}
	if len(sites) == 0 {
		return errNoRegistrationDomain
	}
	link := "https://" + sites[0].Domain + "/console/settings#email-change-token=" + token
	return s.recovery.mailer.Enqueue(ctx, tx, mail.Tenant{Site: uuid.UUID(p.site), Account: uuid.UUID(p.account)},
		registrationMail(email, sites[0].Name, passwordLanguage(language), link))
}

// ConfirmEmailChange requires the same user's signed-in session and consumes one pending request atomically with
// the verified address, masked audit and mandatory notification to the old mailbox. Existing sessions remain valid.
func (s *Service) ConfirmEmailChange(ctx context.Context, p Principal, token string) error {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return ErrNoSession
	}
	if len(token) != 43 {
		return ErrEmailChangeInvalid
	}
	if err := s.recoveryReady(); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(token))
	err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		return s.consumeEmailChange(ctx, tx, p, hash[:])
	})
	if constraint, ok := pg.Duplicate(err); ok && constraint == "users_site_id_email_key" {
		return ErrEmailTaken
	}
	return err
}

func (s *Service) consumeEmailChange(ctx context.Context, tx pg.Tx, p Principal, hash []byte) error {
	locked, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion)
	if err != nil {
		return err
	}
	if secondFactorUnproved(locked, p) {
		return ErrMFARequired
	}
	now := s.now()
	pending, err := s.queries.LockEmailChange(ctx, tx, LockEmailChangeParams{UserID: uuid.UUID(p.user), Now: now.Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	if len(pending) == 0 || !bytes.Equal(hash, pending[0].TokenHash) || pending[0].FactorVersion != p.factorVersion {
		return ErrEmailChangeInvalid
	}
	change := pending[0]
	if !change.Unexpired {
		return ErrEmailChangeExpired
	}
	return s.applyEmailChange(ctx, tx, p, change, now)
}

// applyEmailChange keeps the address, outstanding reset links, audit and old-mailbox notice in one transaction.
func (s *Service) applyEmailChange(ctx context.Context, tx pg.Tx, p Principal, change LockEmailChangeRow, now time.Time) error {
	before, err := s.queries.PasswordRecipient(ctx, tx, uuid.UUID(p.user))
	if err != nil {
		return err
	}
	if err := s.queries.SetProfileEmail(ctx, tx, SetProfileEmailParams{UserID: uuid.UUID(p.user), Email: change.Email, VerifiedAt: &now}); err != nil {
		return err
	}
	if err := s.queries.InvalidateEmailResetTokens(ctx, tx, InvalidateEmailResetTokensParams{UserID: uuid.UUID(p.user), UsedAt: &now}); err != nil {
		return err
	}
	if err := s.queries.DeleteEmailChange(ctx, tx, uuid.UUID(p.user)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: audit.User(uuid.UUID(p.user)), Action: "user.email_changed",
		Target: userTarget(uuid.UUID(p.site), uuid.UUID(p.account), uuid.UUID(p.user)),
		Detail: map[string]any{"before": maskedEmail(before), "after": maskedEmail(change.Email)},
	}); err != nil {
		return err
	}
	return s.recovery.mailer.Enqueue(ctx, tx, mail.Tenant{Site: uuid.UUID(p.site), Account: uuid.UUID(p.account)},
		emailChangedMail(before, change.Language))
}

func maskedEmail(email string) string {
	return "***@" + domainOf(email)
}

func emailChangedMail(to, language string) mail.Message {
	subject := "Email address changed"
	text := "Your email address has changed. If this was not you, contact the site operator immediately."
	if language == "zh" {
		subject = "邮箱已变更"
		text = "您的邮箱已变更。如果不是您本人操作，请立即联系站点管理员。"
	}
	return mail.Message{To: to, Subject: subject, Text: text, HTML: "<p>" + text + "</p>"}
}
