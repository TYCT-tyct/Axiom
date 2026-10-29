package accounts

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// securityAttempt holds identifiers only; unrecognized identities remain absent.
type securityAttempt struct {
	outcome                string
	site                   SiteID
	user, account, session uuid.UUID
	surface                Surface
}

func (a securityAttempt) record(ctx context.Context, tx pg.Tx, action string) error {
	target := audit.Target{Module: "accounts", Kind: "site", ID: a.site.TypeID(), Site: uuid.UUID(a.site)}
	actor := audit.System("accounts.authentication")
	detail := map[string]any{"surface": a.surface}
	if a.outcome != "" {
		detail["outcome"] = a.outcome
	}
	if a.user != (uuid.UUID{}) {
		target = userTarget(uuid.UUID(a.site), a.account, a.user)
		actor = audit.User(a.user)
	}
	if a.session != (uuid.UUID{}) {
		detail["session_id"] = SessionID(a.session).TypeID()
	}
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: action, Target: target, Detail: detail})
}

// failed persists expected security refusals even when the operation's transaction rolled back.
// No request-controlled text crosses this boundary; storage errors replace the refusal so they are logged as failures.
func (s *Service) failed(ctx context.Context, a securityAttempt, action string, result *error) {
	for _, c := range []struct {
		err     error
		outcome string
	}{
		{ErrBadCredentials, "bad_credentials"},
		{ErrBadCode, "bad_code"},
		{ErrBadOrigin, "bad_origin"},
		{ErrNoSession, "invalid_session"},
		{ErrInvalidInvite, "invalid_invite"},
		{ErrEmailTaken, "email_taken"},
		{ErrBadEmail, "bad_email"},
		{ErrWeakPassword, "weak_password"},
		{ErrBadSecret, "bad_secret"},
		{ErrTooManyAttempts, "rate_limited"},
	} {
		if errors.Is(*result, c.err) {
			a.outcome = c.outcome
			break
		}
	}
	if a.outcome == "" {
		return
	}
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := s.db.Tx(record, s.platform, func(tx pg.Tx) error {
		if err := a.record(record, tx, action); err != nil {
			return err
		}
		var count int64
		err := tx.QueryRow(record, `INSERT INTO accounts.security_failures(site_id,failures) VALUES($1,1)
ON CONFLICT(site_id) DO UPDATE SET failures=CASE WHEN accounts.security_failures.started_at<=statement_timestamp()-interval '1 minute' THEN 1 ELSE accounts.security_failures.failures+1 END,
started_at=CASE WHEN accounts.security_failures.started_at<=statement_timestamp()-interval '1 minute' THEN statement_timestamp() ELSE accounts.security_failures.started_at END RETURNING failures`, uuid.UUID(a.site)).Scan(&count)
		if err != nil {
			return err
		}
		return alert.Persist(record, tx, uuid.NewV7(), alert.Signal{Kind: "accounts.authentication_failures", Key: a.site.TypeID(), Severity: "warning", Fields: map[string]string{"value": strconv.FormatInt(count, 10)}})
	})
	if err != nil {
		*result = fmt.Errorf("record authentication security event: %w", err)
	}
}

// SampleSecurityAlerts resolves at most 100 expired failure windows in the background.
// Taking the counter's lock before the signal lock orders recovery before the next failure.
func SampleSecurityAlerts(ctx context.Context, tx pg.Tx) error {
	rows, err := tx.Query(ctx, `WITH expired AS (
        SELECT site_id FROM accounts.security_failures
        WHERE failures>0 AND started_at<=statement_timestamp()-interval '1 minute'
        ORDER BY started_at,site_id FOR UPDATE SKIP LOCKED LIMIT 100)
        UPDATE accounts.security_failures f SET failures=0 FROM expired e WHERE f.site_id=e.site_id RETURNING f.site_id`)
	if err != nil {
		return fmt.Errorf("expire security failure windows: %w", err)
	}
	var sites []uuid.UUID
	for rows.Next() {
		var site uuid.UUID
		if err = rows.Scan(&site); err != nil {
			break
		}
		sites = append(sites, site)
	}
	rows.Close()
	if err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, site := range sites {
		if err := alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "accounts.authentication_failures", Key: SiteID(site).TypeID(), Severity: "warning", Resolved: true}); err != nil {
			return err
		}
	}
	return nil
}

func (p Principal) securityAttempt() securityAttempt {
	return securityAttempt{site: p.site, user: uuid.UUID(p.user), account: uuid.UUID(p.account), session: uuid.UUID(p.session), surface: p.surface}
}
