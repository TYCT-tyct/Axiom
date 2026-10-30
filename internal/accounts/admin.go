package accounts

import (
	"cmp"
	"context"
	"crypto/sha256"
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
// entry names the values it replaced. Shorter session lifetimes are written into the site's sessions in the same
// transaction (CutSessions). A setting outside its bounds is a *SettingError, and no site ErrNotFound.
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
	for surface, life := range map[Surface]lifetime{
		Console: {cmp.Or(set.ConsoleIdle, defaultLifetimes[Console].idle), cmp.Or(set.ConsoleMaxAge, defaultLifetimes[Console].max)},
		Admin:   {cmp.Or(set.AdminIdle, defaultLifetimes[Admin].idle), cmp.Or(set.AdminMaxAge, defaultLifetimes[Admin].max)},
	} {
		maxAge, idle := life.seconds()
		if err := s.queries.CutSessions(ctx, tx, CutSessionsParams{
			SiteID: uuid.UUID(site), Surface: string(surface), MaxAgeSeconds: maxAge, IdleSeconds: idle,
		}); err != nil {
			return fmt.Errorf("cut the site's sessions: %w", err)
		}
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

// RegistrationPolicy is a site's open-registration mode and its anti-abuse values. The mode is stored on the site;
// the rest are settings, where nil in the table means the default in code.
type RegistrationPolicy struct {
	Mode           string
	HumanCheck     bool
	HumanCheckBits int
	DomainBurst    int
	DomainPerHour  int
	DomainAllow    []string
	DomainDeny     []string
}

// DefaultRegistrationPolicy is what a site that sets none of the values gets: registration by invitation, the
// self-hosted proof of work on at 18 bits, five registrations from one domain in a burst and thirty an hour.
func DefaultRegistrationPolicy() RegistrationPolicy {
	return RegistrationPolicy{
		Mode: "invite", HumanCheck: defaultHumanCheck, HumanCheckBits: defaultHumanCheckBits,
		DomainBurst: defaultLimits.domainBurst, DomainPerHour: defaultLimits.domainPerHour,
		DomainAllow: []string{}, DomainDeny: []string{},
	}
}

// Check reports the first value outside its bounds and normalizes the domain lists in place: lower-case, trimmed and
// without duplicates, so the table and the snapshot hold one spelling.
func (p *RegistrationPolicy) Check() error {
	switch p.Mode {
	case "invite", "closed", "open":
	default:
		return &SettingError{"registration_mode", "must be invite, closed or open"}
	}
	if p.HumanCheckBits < 12 || p.HumanCheckBits > 24 {
		return &SettingError{"registration_human_check_bits", "must be from 12 to 24"}
	}
	if p.DomainBurst < 1 || p.DomainBurst > 10_000 {
		return &SettingError{"registration_domain_burst", "must be from 1 to 10000"}
	}
	if p.DomainPerHour < 1 || p.DomainPerHour > 100_000 {
		return &SettingError{"registration_domain_per_hour", "must be from 1 to 100000"}
	}
	var err error
	if p.DomainAllow, err = cleanDomains(p.DomainAllow); err != nil {
		return &SettingError{"registration_domain_allow", "must be domains, at most 100"}
	}
	if p.DomainDeny, err = cleanDomains(p.DomainDeny); err != nil {
		return &SettingError{"registration_domain_deny", "must be domains, at most 100"}
	}
	return nil
}

func (p *RegistrationPolicy) detail() map[string]any {
	return map[string]any{
		"mode": p.Mode, "human_check": p.HumanCheck, "human_check_bits": p.HumanCheckBits,
		"domain_burst": p.DomainBurst, "domain_per_hour": p.DomainPerHour,
		"domain_allow": domainSummary(p.DomainAllow), "domain_deny": domainSummary(p.DomainDeny),
	}
}

// domainSummary describes a list without copying it into the audit entry: a hundred long domains would exceed the
// entry's 8 KiB limit and make an otherwise valid policy impossible to save.
func domainSummary(domains []string) map[string]any {
	hash := sha256.Sum256([]byte(strings.Join(domains, "\n")))
	return map[string]any{"count": len(domains), "sha256": fmt.Sprintf("%x", hash)}
}

// SiteRegistration reads the site's registration policy; ErrNotFound when there is no such site.
func (s *Service) SiteRegistration(ctx context.Context, tx pg.Tx, site SiteID) (RegistrationPolicy, error) {
	rows, err := s.queries.ReadSiteRegistration(ctx, tx, uuid.UUID(site))
	if err != nil {
		return RegistrationPolicy{}, fmt.Errorf("read the site's registration policy: %w", err)
	}
	if len(rows) == 0 {
		return RegistrationPolicy{}, ErrNotFound
	}
	r := rows[0]
	p := DefaultRegistrationPolicy()
	p.Mode = r.Registration
	if r.RegistrationHumanCheck != nil {
		p.HumanCheck = *r.RegistrationHumanCheck
	}
	if r.RegistrationHumanCheckBits != nil {
		p.HumanCheckBits = int(*r.RegistrationHumanCheckBits)
	}
	if r.RegistrationDomainBurst != nil {
		p.DomainBurst = int(*r.RegistrationDomainBurst)
	}
	if r.RegistrationDomainPerHour != nil {
		p.DomainPerHour = int(*r.RegistrationDomainPerHour)
	}
	if r.RegistrationDomainAllow != nil {
		p.DomainAllow = lowerDomains(r.RegistrationDomainAllow)
	}
	if r.RegistrationDomainDeny != nil {
		p.DomainDeny = lowerDomains(r.RegistrationDomainDeny)
	}
	return p, nil
}

// SetSiteRegistration replaces the site's registration policy and records the change, in the site's turn so the entry
// names the values it replaced. A value outside its bounds is a *SettingError, and no site ErrNotFound.
func (s *Service) SetSiteRegistration(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, set RegistrationPolicy, reason string) error {
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
	before, err := s.SiteRegistration(ctx, tx, site)
	if err != nil {
		return err
	}
	if err := s.queries.WriteSiteRegistrationMode(ctx, tx, WriteSiteRegistrationModeParams{
		SiteID: uuid.UUID(site), Registration: set.Mode,
	}); err != nil {
		return fmt.Errorf("write the site's registration mode: %w", err)
	}
	humanCheck, bits := set.HumanCheck, int32(set.HumanCheckBits)      // #nosec G115 -- Check bounds it to 24
	burst, perHour := int32(set.DomainBurst), int32(set.DomainPerHour) // #nosec G115 -- Check bounds it to 100000
	if err := s.queries.WriteSiteRegistrationSettings(ctx, tx, WriteSiteRegistrationSettingsParams{
		SiteID: uuid.UUID(site), HumanCheck: &humanCheck, HumanCheckBits: &bits,
		DomainBurst: &burst, DomainPerHour: &perHour,
		DomainAllow: set.DomainAllow, DomainDeny: set.DomainDeny,
	}); err != nil {
		return fmt.Errorf("write the site's registration settings: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "site_registration.update", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)},
		Detail: map[string]any{"before": before.detail(), "after": set.detail()},
	})
}
