package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
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
		{ErrInvalidInvite, "invalid_invite"},
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
		if a.outcome != "unknown_session" {
			if err := a.record(record, tx, action); err != nil {
				return err
			}
		}
		_, err := tx.Exec(record, `INSERT INTO accounts.security_failures(site_id,failures) VALUES($1,1)
ON CONFLICT(site_id) DO UPDATE SET
completed_peak=CASE WHEN accounts.security_failures.started_at<date_trunc('minute',statement_timestamp())
 THEN greatest(accounts.security_failures.completed_peak,accounts.security_failures.failures) ELSE accounts.security_failures.completed_peak END,
failures=CASE WHEN accounts.security_failures.started_at<date_trunc('minute',statement_timestamp()) THEN 1 ELSE accounts.security_failures.failures+1 END,
started_at=greatest(accounts.security_failures.started_at,date_trunc('minute',statement_timestamp()))`, uuid.UUID(a.site))
		return err
	})
	if err != nil {
		*result = fmt.Errorf("record authentication security event: %w", err)
	}
}

// SampleSecurityAlerts emits at most one completed-window sample per site per minute.
// Peaks survive delayed sampling; row locks serialize replicas without a per-failure signal.
func SampleSecurityAlerts(ctx context.Context, tx pg.Tx) error {
	rows, err := tx.Query(ctx, `WITH ready AS (
        SELECT site_id, greatest(completed_peak, CASE WHEN started_at<date_trunc('minute',statement_timestamp()) THEN failures ELSE 0 END) AS value
        FROM accounts.security_failures
        WHERE (failures>0 OR completed_peak>0 OR reported)
          AND sampled_at<=statement_timestamp()-interval '1 minute'
          AND (completed_peak>0 OR started_at<date_trunc('minute',statement_timestamp()))
        ORDER BY started_at,site_id FOR UPDATE SKIP LOCKED LIMIT 100)
        UPDATE accounts.security_failures f SET completed_peak=0,
        failures=CASE WHEN f.started_at<date_trunc('minute',statement_timestamp()) THEN 0 ELSE f.failures END,
        started_at=date_trunc('minute',statement_timestamp()),sampled_at=statement_timestamp(),reported=ready.value>0
        FROM ready WHERE f.site_id=ready.site_id RETURNING f.site_id,ready.value`)
	if err != nil {
		return fmt.Errorf("sample security failure windows: %w", err)
	}
	var signals []alert.Signal
	for rows.Next() {
		var site uuid.UUID
		var count int64
		if err = rows.Scan(&site, &count); err != nil {
			break
		}
		signals = append(signals, alert.Signal{Kind: "accounts.authentication_failures", Key: SiteID(site).TypeID(), Severity: "warning", Fields: map[string]string{"value": strconv.FormatInt(count, 10)}, Resolved: count == 0})
	}
	rows.Close()
	if err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, signal := range signals {
		if err := alert.Persist(ctx, tx, uuid.NewV7(), signal); err != nil {
			return err
		}
	}
	return nil
}

func (p Principal) securityAttempt() securityAttempt {
	return securityAttempt{site: p.site, user: uuid.UUID(p.user), account: uuid.UUID(p.account), session: uuid.UUID(p.session), surface: p.surface}
}

// Unknown cookies have no authenticated actor and no IP gate before lookup. Budget
// their counter writes without auditing every anonymous request or counting expiry.
func (s *Service) failedSession(ctx context.Context, r *http.Request, d domain, ip netip.Addr, result error) error {
	if !errors.Is(result, ErrNoSession) || d.site == (SiteID{}) {
		return result
	}
	if _, err := r.Cookie(cookieName(d.origin)); err != nil {
		return result
	}
	if err := s.fromIP(ctx, d.site, ip); err != nil {
		return result
	}
	s.failed(ctx, securityAttempt{site: d.site, surface: d.surface, outcome: "unknown_session"}, "security.authentication_failed", &result)
	return result
}
