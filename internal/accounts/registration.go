package accounts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
)

// Registration owns the durable open-registration requests, their verification links and their cleanup. It is shared
// by the HTTP flow and the worker, like PasswordRecovery for resets.
type Registration struct {
	db     *pg.DB
	queue  *pg.Queue
	mailer *mail.Mailer
	scope  pg.Scope
	logger *slog.Logger
}

// RegisterRegistration registers before the queue starts. A node without SMTP does not take verification jobs, but it
// still removes expired requests, so a node that once sent mail leaves no rows behind after SMTP is turned off.
func RegisterRegistration(db *pg.DB, queue *pg.Queue, mailer *mail.Mailer, scope pg.Scope, logger *slog.Logger) (*Registration, error) {
	r := &Registration{db: db, queue: queue, mailer: mailer, scope: scope, logger: logger}
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(r.cleanup)); err != nil {
		return nil, err
	}
	queue.Every(time.Minute, func() river.JobArgs { return registrationCleanupArgs{} })
	if !mailer.Configured() {
		return r, nil
	}
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(r.work)); err != nil {
		return nil, err
	}
	return r, nil
}

type registrationArgs struct {
	ID uuid.UUID `json:"id"`
}

func (registrationArgs) Kind() string { return "accounts.registration" }
func (registrationArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications, MaxAttempts: 5}
}

var (
	errRegistrationJob      = errors.New("accounts: registration processing failed")
	errNoRegistrationDomain = errors.New("accounts: no HTTPS console domain")
)

func (r *Registration) work(ctx context.Context, job *river.Job[registrationArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := r.process(ctx, job.Args); err != nil {
		r.logFailure(ctx, job.ID, job.Attempt, err)
		if errors.Is(err, errNoRegistrationDomain) || errors.Is(err, ErrBadEmail) {
			return river.JobCancel(errRegistrationJob)
		}
		return errRegistrationJob
	}
	return nil
}

// process mails the link once. A redelivered job that finds the token already written sends nothing, and one that
// finds the request gone or expired stops.
func (r *Registration) process(ctx context.Context, args registrationArgs) error {
	found, err := r.db.TenantTx(ctx, "accounts.registration_request_tenant", args.ID, func(tx pg.Tx) error {
		q := New()
		rows, err := q.LockRegistrationRequest(ctx, tx, args.ID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		request := rows[0]
		if !request.Unexpired {
			_, err := q.DeleteRegistrationRequest(ctx, tx, args.ID)
			return err
		}
		if len(request.TokenHash) != 0 {
			return nil
		}
		sites, err := q.ConsoleSite(ctx, tx, ConsoleSiteParams{SiteID: request.SiteID, Domain: request.Domain})
		if err != nil {
			return err
		}
		if len(sites) == 0 {
			return errNoRegistrationDomain
		}
		token := randomToken()
		hash := sha256.Sum256([]byte(token))
		n, err := q.SetRegistrationToken(ctx, tx, SetRegistrationTokenParams{ID: args.ID, TokenHash: hash[:]})
		if err != nil {
			return err
		}
		if n != 1 {
			return nil
		}
		link := "https://" + sites[0].Domain + "/verify-email#token=" + token
		return r.mailer.Enqueue(ctx, tx, mail.Tenant{Site: request.SiteID},
			registrationMail(request.Email, sites[0].Name, request.Language, link))
	})
	if !found {
		return nil
	}
	return err
}

// VerifyEmail turns a verification link into a user. The token is looked up on the request's console domain, then the
// request is locked in its site's scope; the user and the deletion of the request commit together. The password is
// supplied here, not at the unauthenticated request, so a caller cannot choose the password of an address it does not
// own. A token that is unknown, expired, already used or from another site is one ErrInvalidVerification, and a taken
// email is ErrEmailTaken once the pending request is gone.
func (s *Service) VerifyEmail(ctx context.Context, host, token, password string) (out UserID, err error) {
	d, snap, err := s.domain(host)
	if err != nil {
		return UserID{}, err
	}
	if d.surface != Console {
		return UserID{}, ErrUnknownHost
	}
	event := securityAttempt{site: d.site, surface: d.surface}
	defer func() { s.failed(ctx, event, "security.registration_failed", &err) }()
	if !passwordAllowed(password) {
		return UserID{}, ErrWeakPassword
	}
	if len(token) != 43 {
		return UserID{}, ErrInvalidVerification
	}
	hash := sha256.Sum256([]byte(token))
	var targets []RegistrationTenantRow
	if err := s.db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		var err error
		targets, err = s.queries.RegistrationTenant(ctx, tx, RegistrationTenantParams{Domain: strings.ToLower(host), TokenHash: hash[:]})
		return err
	}); err != nil {
		return UserID{}, fmt.Errorf("verify email: %w", err)
	}
	if len(targets) == 0 {
		return UserID{}, ErrInvalidVerification
	}
	var passwordHash string
	if err := s.hash(ctx, func() { passwordHash = hashPassword(password) }); err != nil {
		return UserID{}, err
	}
	var user UserID
	var taken bool
	found, err := s.db.TenantTx(ctx, "accounts.registration_request_tenant", targets[0].ID, func(tx pg.Tx) error {
		rows, err := s.queries.LockRegistrationRequest(ctx, tx, targets[0].ID)
		if err != nil {
			return err
		}
		if len(rows) == 0 || !rows[0].Unexpired || !bytes.Equal(rows[0].TokenHash, hash[:]) {
			return ErrInvalidVerification
		}
		request := rows[0]
		taken, err = s.queries.UserEmailTaken(ctx, tx, UserEmailTakenParams{SiteID: request.SiteID, Email: request.Email})
		if err != nil {
			return err
		}
		if taken {
			_, err := s.queries.DeleteRegistrationRequest(ctx, tx, targets[0].ID)
			return err
		}
		site, ok := snap.sites[SiteID(request.SiteID)]
		if !ok {
			return ErrInvalidVerification
		}
		// A deny entry added while the link was pending must stop it: the list is an abuse response, not just a
		// request-time filter.
		if !domainPermitted(site, domainOf(request.Email)) {
			return ErrEmailDomainNotAllowed
		}
		id, account := uuid.NewV7(), uuid.NewV7()
		now := s.now()
		if err := createUser(ctx, tx, s.queries, s.opened, newUser{
			id: id, account: account, site: request.SiteID, tier: request.TierID, role: snap.roleIDs["owner"],
			email: request.Email, passwordHash: passwordHash, verified: &now,
		}); err != nil {
			return err
		}
		n, err := s.queries.DeleteRegistrationRequest(ctx, tx, targets[0].ID)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrInvalidVerification
		}
		user = UserID(id)
		success := securityAttempt{site: site.ID, user: id, account: account, surface: d.surface}
		return success.record(ctx, tx, "security.registration")
	})
	if err != nil {
		if constraint, ok := pg.Duplicate(err); ok && constraint == "users_site_id_email_key" {
			return UserID{}, ErrEmailTaken
		}
		return UserID{}, fmt.Errorf("verify email: %w", err)
	}
	if !found {
		return UserID{}, ErrInvalidVerification
	}
	if taken {
		return UserID{}, ErrEmailTaken
	}
	return user, nil
}

// registrationMail keeps untrusted brand text escaped in HTML and out of headers containing control characters.
func registrationMail(to, brand, language, link string) mail.Message {
	brand = strings.Map(func(r rune) rune {
		if r < ' ' || r == 127 {
			return -1
		}
		return r
	}, brand)
	subject, text := brand+" — verify your email", "Confirm your email address using the link below. It expires 24 hours after your request. If you did not request this, ignore this email."
	if language == "zh" {
		subject, text = brand+" — 验证邮箱", "请使用以下链接确认邮箱，链接在请求后 24 小时过期。如果不是您本人请求，请忽略此邮件。"
	}
	return mail.Message{
		To: to, Subject: subject,
		Text: brand + "\n\n" + text + "\n\n" + link,
		HTML: "<h1>" + html.EscapeString(brand) + "</h1><p>" + html.EscapeString(text) + "</p><p><a href=\"" +
			html.EscapeString(link) + "\">" + html.EscapeString(link) + "</a></p>",
	}
}

// cleanup removes expired pending registrations, in batches so one tick cannot hold the table for long.
func (r *Registration) cleanup(ctx context.Context, _ *river.Job[registrationCleanupArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := r.db.Tx(ctx, r.scope, func(tx pg.Tx) error {
		return New().PurgeRegistrationRequests(ctx, tx)
	}); err != nil {
		r.logger.ErrorContext(ctx, "accounts.registration_cleanup.failed", "err", err)
		return err
	}
	return nil
}

type registrationCleanupArgs struct{}

func (registrationCleanupArgs) Kind() string { return "accounts.registration_cleanup" }
func (registrationCleanupArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications, MaxAttempts: 5}
}

// logFailure records only the category, the SQLSTATE and the attempt: a link or an address must not reach the log.
func (r *Registration) logFailure(ctx context.Context, jobID int64, attempt int, err error) {
	category := "unknown"
	switch {
	case errors.Is(err, errNoRegistrationDomain):
		category = "no_console_domain"
	case errors.Is(err, ErrBadEmail):
		category = "bad_email"
	case errors.Is(err, mail.ErrNotConfigured):
		category = "email_disabled"
	case errors.Is(err, context.DeadlineExceeded):
		category = "timeout"
	case errors.Is(err, context.Canceled):
		category = "canceled"
	case pg.SQLState(err) != "":
		category = "database"
	}
	r.logger.ErrorContext(ctx, "accounts.registration.failed", "job_id", jobID, "attempt", attempt,
		"category", category, "sqlstate", pg.SQLState(err))
}
