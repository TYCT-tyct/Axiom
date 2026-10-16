package accounts

import (
	"context"
	"fmt"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// What the admin API does with sites, users and accounts (README.md「管理接口」). Each function takes the caller's
// transaction, which must hold the platform scope: the admin acts on every tenant, and the audit log is written in it.

// Tier is a tier of a site.
type Tier struct {
	ID   TierID
	Name string
}

// SiteSettings are a site's own values for the sessions and keys its users get. A duration of 0 and a nil limit are
// not set: the default in code applies (DefaultSiteSettings).
type SiteSettings struct {
	ConsoleIdle, ConsoleMaxAge time.Duration
	AdminIdle, AdminMaxAge     time.Duration
	// NewKeyDailyLimit is the daily spend limit a new key gets, in billionths of a credit.
	NewKeyDailyLimit *int64
}

// DefaultSiteSettings are the values that apply where a site sets none.
func DefaultSiteSettings() SiteSettings {
	limit := int64(defaultNewKeyDailyLimit)
	return SiteSettings{
		ConsoleIdle: defaultLifetimes[Console].idle, ConsoleMaxAge: defaultLifetimes[Console].max,
		AdminIdle: defaultLifetimes[Admin].idle, AdminMaxAge: defaultLifetimes[Admin].max,
		NewKeyDailyLimit: &limit,
	}
}

// The bounds the table keeps (00001_tables.sql), the same the contract states.
const (
	minSession     = 5 * time.Minute
	maxConsoleIdle = 30 * 24 * time.Hour
	maxConsoleAge  = 90 * 24 * time.Hour
	maxAdminIdle   = 12 * time.Hour
	maxAdminAge    = 24 * time.Hour
	maxNewKeyDaily = 1_000_000_000_000_000
)

// SettingError is a setting outside its bounds. Field is the setting's name in the admin API.
type SettingError struct{ Field, Reason string }

func (e *SettingError) Error() string { return "accounts: " + e.Field + " " + e.Reason }

// Check reports the first value outside its bounds.
func (v SiteSettings) Check() error {
	for _, c := range []struct {
		field    string
		d, limit time.Duration
	}{
		{"console_idle_timeout_seconds", v.ConsoleIdle, maxConsoleIdle},
		{"console_max_age_seconds", v.ConsoleMaxAge, maxConsoleAge},
		{"admin_idle_timeout_seconds", v.AdminIdle, maxAdminIdle},
		{"admin_max_age_seconds", v.AdminMaxAge, maxAdminAge},
	} {
		if c.d != 0 && (c.d < minSession || c.d > c.limit) {
			return &SettingError{c.field, fmt.Sprintf("must be from %d to %d seconds", minSession/time.Second, c.limit/time.Second)}
		}
	}
	if l := v.NewKeyDailyLimit; l != nil && (*l < 0 || *l > maxNewKeyDaily) {
		return &SettingError{"new_key_daily_limit", fmt.Sprintf("must be from 0 to %d billionths of a credit", int64(maxNewKeyDaily))}
	}
	return nil
}

func (v SiteSettings) detail() map[string]any {
	seconds := func(d time.Duration) any {
		if d == 0 {
			return nil
		}
		return int64(d / time.Second)
	}
	return map[string]any{
		"console_idle_timeout_seconds": seconds(v.ConsoleIdle), "console_max_age_seconds": seconds(v.ConsoleMaxAge),
		"admin_idle_timeout_seconds": seconds(v.AdminIdle), "admin_max_age_seconds": seconds(v.AdminMaxAge),
		"new_key_daily_limit": v.NewKeyDailyLimit,
	}
}

// Site is the site called id in the snapshot, which is at most 30 seconds behind the database.
func (s *Service) Site(id SiteID) (Site, bool) {
	site, ok := s.snapshot.Load().sites[id]
	return site, ok
}

// MainSite is the platform's main site.
func (s *Service) MainSite() (Site, bool) {
	for _, site := range s.snapshot.Load().sites {
		if site.Main {
			return site, true
		}
	}
	return Site{}, false
}

// Refresh reloads the sites, domains and roles now, for a node that has just changed them: the other nodes follow
// within their reload interval. A failure is not the change's: the write is done, and the next reload tries again.
func (s *Service) Refresh(ctx context.Context) error { return s.reloadWithin(ctx) }

// SiteSettings reads the site's own settings; ErrNotFound when there is no such site.
func (s *Service) SiteSettings(ctx context.Context, tx pg.Tx, site SiteID) (SiteSettings, error) {
	rows, err := s.queries.ReadSiteSettings(ctx, tx, uuid.UUID(site))
	if err != nil {
		return SiteSettings{}, fmt.Errorf("read the site's settings: %w", err)
	}
	if len(rows) == 0 {
		return SiteSettings{}, ErrNotFound
	}
	r := rows[0]
	return SiteSettings{
		ConsoleIdle: time.Duration(r.ConsoleIdleSeconds) * time.Second, ConsoleMaxAge: time.Duration(r.ConsoleMaxAgeSeconds) * time.Second,
		AdminIdle: time.Duration(r.AdminIdleSeconds) * time.Second, AdminMaxAge: time.Duration(r.AdminMaxAgeSeconds) * time.Second,
		NewKeyDailyLimit: r.NewKeyDailyLimit,
	}, nil
}

// SetSiteSettings replaces the site's own settings with set and records the change, in the site's turn so that the
// entry names the values it replaced. A setting outside its bounds is a *SettingError, and no site ErrNotFound.
func (s *Service) SetSiteSettings(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, set SiteSettings, reason string) error {
	if err := set.Check(); err != nil {
		return err
	}
	locked, err := s.queries.LockSite(ctx, tx, uuid.UUID(site))
	if err != nil {
		return fmt.Errorf("lock the site: %w", err)
	}
	if len(locked) == 0 {
		return ErrNotFound
	}
	before, err := s.SiteSettings(ctx, tx, site)
	if err != nil {
		return err
	}
	if err := s.queries.WriteSiteSettings(ctx, tx, WriteSiteSettingsParams{
		SiteID: uuid.UUID(site), ConsoleIdleSeconds: seconds64(set.ConsoleIdle), ConsoleMaxAgeSeconds: seconds64(set.ConsoleMaxAge),
		AdminIdleSeconds: seconds64(set.AdminIdle), AdminMaxAgeSeconds: seconds64(set.AdminMaxAge), NewKeyDailyLimit: set.NewKeyDailyLimit,
	}); err != nil {
		return fmt.Errorf("write the site's settings: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "site_settings.update", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)},
		Detail: map[string]any{"before": before.detail(), "after": set.detail()},
	})
}

// seconds64 is d in whole seconds, nil when it is not set.
func seconds64(d time.Duration) *int64 {
	if d == 0 {
		return nil
	}
	n := int64(d / time.Second)
	return &n
}

// Tiers are the site's tiers by name.
func (s *Service) Tiers(ctx context.Context, tx pg.Tx, site SiteID) ([]Tier, error) {
	rows, err := s.queries.ListTiers(ctx, tx, uuid.UUID(site))
	if err != nil {
		return nil, fmt.Errorf("list the tiers: %w", err)
	}
	tiers := make([]Tier, len(rows))
	for i, r := range rows {
		tiers[i] = Tier{TierID(r.ID), r.Name}
	}
	return tiers, nil
}

// UserQuery chooses the users to list, newest first. A nil Site is every site; EmailPrefix is matched literally
// against the start of the lower-cased email; After is the last user of the page before, and the zero UserID the first
// page.
type UserQuery struct {
	Site        *SiteID
	EmailPrefix string
	After       UserID
	PageSize    int
}

// UserSummary is a user as a list shows it.
type UserSummary struct {
	ID        UserID
	Account   AccountID
	Site      SiteID
	Email     string
	Status    string
	Tier      Tier
	MFA       bool
	CreatedAt time.Time
}

// likeEscaper makes a prefix a literal LIKE pattern (backslash is LIKE's default escape).
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Users lists a page of users; more tells whether users remain beyond it.
func (s *Service) Users(ctx context.Context, tx pg.Tx, q UserQuery) (users []UserSummary, more bool, err error) {
	params := ListUsersParams{PageSize: int32(q.PageSize + 1)} // #nosec G115 -- a page is at most 200
	if q.Site != nil {
		id := uuid.UUID(*q.Site)
		params.SiteID = &id
	}
	if q.EmailPrefix != "" {
		prefix := likeEscaper.Replace(strings.ToLower(q.EmailPrefix))
		params.EmailPrefix = &prefix
	}
	if q.After != (UserID{}) {
		after := uuid.UUID(q.After)
		params.After = &after
	}
	rows, err := s.queries.ListUsers(ctx, tx, params)
	if err != nil {
		return nil, false, fmt.Errorf("list the users: %w", err)
	}
	if more = len(rows) > q.PageSize; more {
		rows = rows[:q.PageSize]
	}
	users = make([]UserSummary, len(rows))
	for i, r := range rows {
		users[i] = UserSummary{
			ID: UserID(r.ID), Account: AccountID(r.AccountID), Site: SiteID(r.SiteID), Email: r.Email, Status: r.Status,
			Tier: Tier{TierID(r.TierID), r.TierName}, MFA: r.Mfa, CreatedAt: r.CreatedAt.Time,
		}
	}
	return users, more, nil
}

// AccountDetail is an account with its tier and its owner.
type AccountDetail struct {
	ID         AccountID
	Site       SiteID
	Name, Kind string
	Status     string
	Tier       Tier
	Owner      struct {
		ID    UserID
		Email string
	}
	CreatedAt time.Time
}

// Account reads one account; ErrNotFound when there is none.
func (s *Service) Account(ctx context.Context, tx pg.Tx, id AccountID) (AccountDetail, error) {
	rows, err := s.queries.ReadAccount(ctx, tx, uuid.UUID(id))
	if err != nil {
		return AccountDetail{}, fmt.Errorf("read the account: %w", err)
	}
	if len(rows) == 0 {
		return AccountDetail{}, ErrNotFound
	}
	r := rows[0]
	a := AccountDetail{
		ID: AccountID(r.ID), Site: SiteID(r.SiteID), Name: r.Name, Kind: r.Kind, Status: r.Status,
		Tier: Tier{TierID(r.TierID), r.TierName}, CreatedAt: r.CreatedAt.Time,
	}
	a.Owner.ID, a.Owner.Email = UserID(r.OwnerID), r.OwnerEmail
	return a, nil
}
