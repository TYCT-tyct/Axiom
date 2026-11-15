package accounts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/netip"
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

// DisplayName reads the personal account name so a refreshed settings form uses the persisted value.
func (s *Service) DisplayName(ctx context.Context, p Principal) (name string, err error) {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return "", ErrNoSession
	}
	err = s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		name, err = s.queries.ProfileName(ctx, tx, uuid.UUID(p.account))
		return err
	})
	return name, err
}

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
	if err := s.checkEmailChangeRecipient(ctx, p); err != nil {
		return err
	}
	if err := s.recoveryReady(); err != nil {
		return err
	}
	email = normalEmail(email)
	if !emailAllowed(email) {
		return ErrBadEmail
	}
	site := s.snapshot.Load().sites[p.site]
	if !domainPermitted(site, domainOf(email)) {
		return ErrEmailDomainNotAllowed
	}
	limit := s.emailLimit(p.site, uuid.UUID(p.user).String(), site.limits)
	limit.bucket, limit.kind = s.guards.resets, "email_change"
	if _, wait := takeAttempt(s, ctx, limit); wait > 0 {
		return &TooManyAttempts{RetryAfter: wait}
	}
	return s.db.Tx(ctx, pg.SiteScope(uuid.UUID(p.site)), func(tx pg.Tx) error {
		return s.issueEmailChange(ctx, tx, p, email, language)
	})
}

// checkEmailChangeRecipient refuses addresses the mandatory old-mailbox notice cannot reach, before any sending budget.
func (s *Service) checkEmailChangeRecipient(ctx context.Context, p Principal) error {
	var email string
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		email, err = s.queries.PasswordRecipient(ctx, tx, uuid.UUID(p.user))
		return err
	}); err != nil {
		return err
	}
	if !emailAllowed(email) {
		return ErrBadEmail
	}
	return nil
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

// EmailChangeConfirmation carries the proof and the replacement device's metadata.
type EmailChangeConfirmation struct {
	Token     string
	ClientIP  netip.Addr
	UserAgent string
}

// ConfirmEmailChange consumes the verified address and completes the existing factor-change lifecycle atomically.
func (s *Service) ConfirmEmailChange(ctx context.Context, p Principal, c EmailChangeConfirmation) (Issued, []string, error) {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return Issued{}, nil, ErrNoSession
	}
	if len(c.Token) != 43 {
		return Issued{}, nil, ErrEmailChangeInvalid
	}
	if err := s.recoveryReady(); err != nil {
		return Issued{}, nil, err
	}
	hash := sha256.Sum256([]byte(c.Token))
	var issued Issued
	var codes []string
	err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		issued, codes, err = s.consumeEmailChange(ctx, tx, p, hash[:], c)
		return err
	})
	if constraint, ok := pg.Duplicate(err); ok && constraint == "users_site_id_email_key" {
		return Issued{}, nil, ErrEmailTaken
	}
	if err != nil {
		return Issued{}, nil, err
	}
	return issued, codes, nil
}

func (s *Service) consumeEmailChange(ctx context.Context, tx pg.Tx, p Principal, hash []byte, c EmailChangeConfirmation) (Issued, []string, error) {
	locked, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion)
	if errors.Is(err, ErrBadCredentials) {
		return Issued{}, nil, ErrEmailChangeInvalid
	}
	if err != nil {
		return Issued{}, nil, err
	}
	if secondFactorUnproved(locked, p) {
		return Issued{}, nil, ErrMFARequired
	}
	now := s.now()
	pending, err := s.queries.LockEmailChange(ctx, tx, LockEmailChangeParams{UserID: uuid.UUID(p.user), Now: now.Format(time.RFC3339Nano)})
	if err != nil {
		return Issued{}, nil, err
	}
	if len(pending) == 0 || !bytes.Equal(hash, pending[0].TokenHash) || pending[0].FactorVersion != p.factorVersion {
		return Issued{}, nil, ErrEmailChangeInvalid
	}
	change := pending[0]
	if !change.Unexpired {
		return Issued{}, nil, ErrEmailChangeExpired
	}
	if !domainPermitted(s.snapshot.Load().sites[p.site], domainOf(change.Email)) {
		return Issued{}, nil, ErrEmailDomainNotAllowed
	}
	if err := s.applyEmailChange(ctx, tx, p, change, now); err != nil {
		return Issued{}, nil, err
	}
	return s.finishEmailChange(ctx, tx, p, locked, c)
}

func (s *Service) finishEmailChange(ctx context.Context, tx pg.Tx, p Principal, locked LockUserRow, c EmailChangeConfirmation) (Issued, []string, error) {
	issued, codes, err := s.finishFactorChange(ctx, tx, locked, factorFinish{
		session: newSession{
			user: uuid.UUID(p.user), account: uuid.UUID(p.account), site: s.snapshot.Load().sites[p.site],
			surface: Console, level: p.level, ip: c.ClientIP, userAgent: c.UserAgent,
		},
		totpOn: locked.Mfa,
	})
	if err != nil {
		return Issued{}, nil, err
	}
	if err := markFactorNoticeEmailed(ctx, tx, uuid.UUID(p.user)); err != nil {
		return Issued{}, nil, err
	}
	return issued, codes, nil
}

// applyEmailChange must be followed by finishEmailChange in the same transaction, so a new address cannot leave old credentials valid.
func (s *Service) applyEmailChange(ctx context.Context, tx pg.Tx, p Principal, change LockEmailChangeRow, now time.Time) error {
	before, err := s.queries.PasswordRecipient(ctx, tx, uuid.UUID(p.user))
	if err != nil {
		return err
	}
	if !emailAllowed(before) {
		return ErrBadEmail
	}
	if err := s.queries.SetProfileEmail(ctx, tx, SetProfileEmailParams{UserID: uuid.UUID(p.user), Email: change.Email, VerifiedAt: &now}); err != nil {
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
	text := "Your email address has changed. Other sessions and all old recovery codes are invalid. If new recovery codes are shown after confirmation, save them. If this was not you, contact the site operator immediately."
	if language == "zh" {
		subject = "邮箱已变更"
		text = "您的邮箱已变更，其他会话和所有旧恢复码已失效，确认后若显示新恢复码，请妥善保存。如果不是您本人操作，请立即联系站点管理员。"
	}
	return mail.Message{To: to, Subject: subject, Text: text, HTML: "<p>" + text + "</p>"}
}
