package accounts

import (
	"context"
	"errors"
	"net/url"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// NotificationPreferences are user-owned, with defaults resolved in code alone.
type NotificationPreferences struct {
	LowBalance    bool   `json:"low_balance"`
	TopUp         bool   `json:"top_up"`
	FactorChanged bool   `json:"factor_changed"`
	Threshold     int64  `json:"-"`
	WebhookURL    string `json:"webhook_url"`
	Language      string `json:"language"`
}

// ErrNotificationPreferences rejects an invalid threshold, language or destination.
var ErrNotificationPreferences = errors.New("accounts: invalid notification preferences")

func notificationDefaults() NotificationPreferences {
	return NotificationPreferences{LowBalance: true, TopUp: true, FactorChanged: true, Threshold: 5_000_000_000, Language: "en"}
}

// ValidateNotificationWebhookURL validates structure; the HTTP boundary also checks egress DNS policy.
func ValidateNotificationWebhookURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", ErrNotificationPreferences
	}
	return u.Hostname(), nil
}

// GetNotificationPreferences resolves unset values against the code defaults.
func GetNotificationPreferences(ctx context.Context, tx pg.Tx, user uuid.UUID) (NotificationPreferences, error) {
	p := notificationDefaults()
	var low, top, factor *bool
	var threshold *int64
	err := tx.QueryRow(ctx, `SELECT low_balance,top_up,factor_changed,threshold,webhook_url,language FROM accounts.notification_preferences WHERE user_id=$1`, user).
		Scan(&low, &top, &factor, &threshold, &p.WebhookURL, &p.Language)
	if errors.Is(err, pg.ErrNoRows) {
		return p, nil
	}
	if low != nil {
		p.LowBalance = *low
	}
	if top != nil {
		p.TopUp = *top
	}
	if factor != nil {
		p.FactorChanged = *factor
	}
	if threshold != nil {
		p.Threshold = *threshold
	}
	return p, err
}

// SetNotificationPreferences replaces preferences in the authenticated user's tenant transaction.
func SetNotificationPreferences(ctx context.Context, tx pg.Tx, site, account, user uuid.UUID, p NotificationPreferences) error {
	if p.Threshold < 0 || (p.Language != "en" && p.Language != "zh") {
		return ErrNotificationPreferences
	}
	if p.WebhookURL != "" {
		if _, err := ValidateNotificationWebhookURL(p.WebhookURL); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO accounts.notification_preferences(site_id,account_id,user_id,low_balance,top_up,factor_changed,threshold,webhook_url,language)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(user_id) DO UPDATE SET low_balance=$4,top_up=$5,factor_changed=$6,threshold=$7,webhook_url=$8,language=$9`, site, account, user, p.LowBalance, p.TopUp, p.FactorChanged, p.Threshold, p.WebhookURL, p.Language)
	return err
}

// RecordBalanceNotice resolves only the account's own active user; no email crosses the caller boundary.
func RecordBalanceNotice(ctx context.Context, tx pg.Tx, id, site, account uuid.UUID, previous, balance, topUp int64) error {
	var user uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM accounts.users WHERE site_id=$1 AND account_id=$2 AND status='active'`, site, account).Scan(&user)
	if errors.Is(err, pg.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	p, err := GetNotificationPreferences(ctx, tx, user)
	if err != nil {
		return err
	}
	for _, n := range []struct {
		kind  string
		value int64
		send  bool
	}{
		{"low_balance", balance, p.LowBalance && previous >= p.Threshold && balance < p.Threshold},
		{"top_up", topUp, p.TopUp && topUp > 0},
	} {
		if !n.send {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO accounts.user_notices(site_id,account_id,user_id,source,kind,value) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(user_id,kind,source) DO NOTHING`, site, account, user, id.String(), n.kind, n.value); err != nil {
			return err
		}
	}
	return nil
}

// Announcement is plain text. Withdrawals are terminal; the validity interval is [StartsAt, EndsAt).
type Announcement struct {
	ID       uuid.UUID `json:"id"`
	Site     uuid.UUID `json:"site_id"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
}

// ErrAnnouncement rejects an invalid text length or validity interval.
var ErrAnnouncement = errors.New("accounts: invalid announcement")

// PublishAnnouncement inserts immutable content; writes require platform scope.
func PublishAnnouncement(ctx context.Context, tx pg.Tx, a Announcement) error {
	if len([]rune(a.Title)) < 1 || len([]rune(a.Title)) > 200 || len([]rune(a.Body)) < 1 || len([]rune(a.Body)) > 20000 || !a.EndsAt.After(a.StartsAt) {
		return ErrAnnouncement
	}
	_, err := tx.Exec(ctx, `INSERT INTO accounts.announcements(id,site_id,title,body,starts_at,ends_at) VALUES($1,$2,$3,$4,$5,$6)`, a.ID, a.Site, a.Title, a.Body, a.StartsAt, a.EndsAt)
	return err
}

// WithdrawAnnouncement makes a publication permanently inactive.
func WithdrawAnnouncement(ctx context.Context, tx pg.Tx, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `UPDATE accounts.announcements SET withdrawn_at=clock_timestamp() WHERE id=$1 AND withdrawn_at IS NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ActiveAnnouncements returns only the requested site's active publications.
func ActiveAnnouncements(ctx context.Context, tx pg.Tx, site uuid.UUID, now time.Time) ([]Announcement, error) {
	rows, err := tx.Query(ctx, `SELECT id,site_id,title,body,starts_at,ends_at FROM accounts.announcements WHERE site_id=$1 AND withdrawn_at IS NULL AND starts_at<=$2 AND ends_at>$2 ORDER BY starts_at DESC,id DESC LIMIT 100`, site, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Announcement{}
	for rows.Next() {
		var a Announcement
		if err = rows.Scan(&a.ID, &a.Site, &a.Title, &a.Body, &a.StartsAt, &a.EndsAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// NotificationPreferences reads only the signed-in user's preferences.
func (s *Service) NotificationPreferences(ctx context.Context, p Principal) (out NotificationPreferences, err error) {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return out, ErrNoSession
	}
	err = s.db.ReadTx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error { out, err = GetNotificationPreferences(ctx, tx, uuid.UUID(p.user)); return err })
	return out, err
}

// UpdateNotificationPreferences binds all tenant identifiers to the authenticated principal.
func (s *Service) UpdateNotificationPreferences(ctx context.Context, p Principal, prefs NotificationPreferences) error {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return ErrNoSession
	}
	return s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		if err := SetNotificationPreferences(ctx, tx, uuid.UUID(p.site), uuid.UUID(p.account), uuid.UUID(p.user), prefs); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{Actor: audit.User(uuid.UUID(p.user)), Action: "user.notification_preferences", Target: userTarget(uuid.UUID(p.site), uuid.UUID(p.account), uuid.UUID(p.user))})
	})
}

// Announcements uses the principal's site, never a client-selected tenant.
func (s *Service) Announcements(ctx context.Context, p Principal, now time.Time) (out []Announcement, err error) {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return nil, ErrNoSession
	}
	err = s.db.ReadTx(ctx, pg.AccountScopeIn(uuid.UUID(p.site), uuid.UUID(p.account)), func(tx pg.Tx) error { out, err = ActiveAnnouncements(ctx, tx, uuid.UUID(p.site), now); return err })
	return out, err
}
