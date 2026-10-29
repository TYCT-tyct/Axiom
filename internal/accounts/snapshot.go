package accounts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// Surface is where a request came in: the console, the model API or the admin.
type Surface string

// The surfaces, as site_domains.surface names them.
const (
	Console Surface = "console"
	API     Surface = "api"
	Admin   Surface = "admin"
)

// Site is a site as the snapshot holds it. It decides nothing about access: whether a site or domain is enabled is
// read from the database on every call (README.md「快照」).
type Site struct {
	ID           SiteID
	Main         bool
	Name         string
	KeyPrefix    string
	Registration string // invite, closed or open
	// Currency is the site's accounting currency, which the amounts its API shows are in.
	Currency platform.Currency
	// DefaultTier is the tier the site puts a new account in, its first unless an operator changed it.
	DefaultTier TierID
	// NewKeyDailyLimit is the daily spend limit a new key gets, in billionths of a credit.
	NewKeyDailyLimit int64
	// MaxKeysPerAccount and KeyCreationsPerHour bound an account's call keys: how many it may hold that are not
	// deleted, and how many it may make in an hour.
	MaxKeysPerAccount, KeyCreationsPerHour int
	// Open registration's human check and email domain policy. The check is a proof of work the page solves; the
	// lists are lower-case domains, and the deny list wins over the allow list.
	RegistrationHumanCheck     bool
	RegistrationHumanCheckBits int
	RegistrationDomainAllow    []string
	RegistrationDomainDeny     []string
	// APIBase is the origin a key's calls go to, where the site registers an API domain: the console tells the user
	// where to point a client rather than trusting the Host of the request (README.md「快照」).
	APIBase  string
	sessions map[Surface]lifetime
	limits   limits
}

// lifetime is how long a session lasts: idle since its last request, and at most from its start.
type lifetime struct{ idle, max time.Duration }

// seconds are the lifetimes as the queries that check a session against its site's now take them.
func (l lifetime) seconds() (maxAge, idle int64) {
	return int64(l.max / time.Second), int64(l.idle / time.Second)
}

// The defaults of a site that sets none: 10 credits a day for a new key; console sessions idle out after 7 days and
// last at most 30, admin sessions 30 minutes and 12 hours; 100 call keys an account may hold and 50 it may make in an
// hour, room for the integrations and the rotation a customer runs and far from the rate a script makes keys at.
const (
	defaultNewKeyDailyLimit    = 10_000_000_000
	defaultMaxKeysPerAccount   = 100
	defaultKeyCreationsPerHour = 50
)

var defaultLifetimes = map[Surface]lifetime{
	Console: {7 * 24 * time.Hour, 30 * 24 * time.Hour},
	Admin:   {30 * time.Minute, 12 * time.Hour},
}

// snapshot is an immutable view of the sites, replaced whole.
type snapshot struct {
	presets map[presetKey]string
	loaded  time.Time
	sites   map[SiteID]Site
	domains map[string]domain             // by host
	roles   map[uuid.UUID]map[string]bool // each role's permissions
	roleIDs map[string]uuid.UUID          // by name
}

type domain struct {
	site    SiteID
	surface Surface
	origin  string // what a state-changing request's Origin must be
}

var (
	// ErrUnknownHost is a request to a host no enabled domain registers.
	ErrUnknownHost = errors.New("accounts: unknown host")
	// ErrStale is the snapshot older than staleAfter: requests are refused until a reload succeeds.
	ErrStale = errors.New("accounts: the site snapshot is out of date")
)

const (
	reloadEvery = 30 * time.Second
	staleAfter  = 3 * reloadEvery
	// reloadTimeout bounds one reload, so a database that stops answering cannot hold the only reload loop: the
	// next tick tries again on a fresh connection.
	reloadTimeout = 5 * time.Second
)

// Load reads the snapshot, refusing a role with a permission the code does not declare, then reloads it every
// reloadEvery until ctx ends. A failed first load fails the start; a failed reload is logged and retried, and after
// staleAfter without one the snapshot refuses requests.
func (s *Service) Load(ctx context.Context, logger *slog.Logger) error {
	if err := s.reload(ctx); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(reloadEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.reloadWithin(ctx); err != nil && ctx.Err() == nil {
					logger.Error("reload the site snapshot", "err", err)
				}
			}
		}
	}()
	return nil
}

// reloadWithin is reload bounded by reloadTimeout.
func (s *Service) reloadWithin(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()
	return s.reload(ctx)
}

// reload reads the snapshot in one read-only transaction, so its tables agree with each other.
func (s *Service) reload(ctx context.Context) error {
	var next *snapshot
	if err := s.db.ReadTx(ctx, s.platform, func(tx pg.Tx) error {
		var err error
		next, err = s.read(ctx, tx)
		return err
	}); err != nil {
		return fmt.Errorf("load the site snapshot: %w", err)
	}
	s.snapshot.Store(next)
	return nil
}

func (s *Service) read(ctx context.Context, tx pg.Tx) (*snapshot, error) {
	next := &snapshot{loaded: s.now(), sites: map[SiteID]Site{}, domains: map[string]domain{}, roles: map[uuid.UUID]map[string]bool{}, roleIDs: map[string]uuid.UUID{}}
	sites, err := s.queries.ListSites(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, r := range sites {
		site := Site{
			ID: SiteID(r.ID), Main: r.IsMain, Name: r.Name, KeyPrefix: r.KeyPrefix, Registration: r.Registration,
			DefaultTier: TierID(r.DefaultTierID), NewKeyDailyLimit: defaultNewKeyDailyLimit,
			sessions: map[Surface]lifetime{
				Console: {seconds(r.ConsoleIdleSeconds, defaultLifetimes[Console].idle), seconds(r.ConsoleMaxAgeSeconds, defaultLifetimes[Console].max)},
				Admin:   {seconds(r.AdminIdleSeconds, defaultLifetimes[Admin].idle), seconds(r.AdminMaxAgeSeconds, defaultLifetimes[Admin].max)},
			},
		}
		currency, err := platform.ParseCurrency(r.Currency)
		if err != nil {
			return nil, fmt.Errorf("site %s: %w", site.ID.TypeID(), err)
		}
		site.Currency = currency
		if r.NewKeyDailyLimit != nil {
			site.NewKeyDailyLimit = *r.NewKeyDailyLimit
		}
		site.MaxKeysPerAccount = setting(r.MaxKeysPerAccount, defaultMaxKeysPerAccount)
		site.KeyCreationsPerHour = setting(r.KeyCreationsPerHour, defaultKeyCreationsPerHour)
		site.RegistrationHumanCheck = boolSetting(r.RegistrationHumanCheck, defaultHumanCheck)
		site.RegistrationHumanCheckBits = setting(r.RegistrationHumanCheckBits, defaultHumanCheckBits)
		site.RegistrationDomainAllow = lowerDomains(r.RegistrationDomainAllow)
		site.RegistrationDomainDeny = lowerDomains(r.RegistrationDomainDeny)
		site.limits = limits{
			ipBurst: setting(r.IpAttemptBurst, defaultLimits.ipBurst), ipPerMinute: setting(r.IpAttemptsPerMinute, defaultLimits.ipPerMinute),
			emailFailures: setting(r.EmailSignInFailures, defaultLimits.emailFailures), codeFailures: setting(r.UserCodeFailures, defaultLimits.codeFailures),
			challengeAttempts: setting(r.ChallengeCodeAttempts, defaultLimits.challengeAttempts),
			domainBurst:       setting(r.RegistrationDomainBurst, defaultLimits.domainBurst),
			domainPerHour:     setting(r.RegistrationDomainPerHour, defaultLimits.domainPerHour),
		}
		next.sites[site.ID] = site
	}
	domains, err := s.queries.ListSiteDomains(ctx, tx)
	if err != nil {
		return nil, err
	}
	readDomains(next, domains)
	roles, err := s.queries.ListRoles(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, r := range roles {
		next.roleIDs[r.Name] = r.ID
		next.roles[r.ID] = map[string]bool{}
		for _, p := range r.Permissions {
			if _, declared := permissions[p]; !declared {
				slog.WarnContext(ctx, "ignoring undeclared role permission", "role", r.Name, "permission", p)
				continue
			}
			next.roles[r.ID][p] = true
		}
	}
	next.presets, err = readPresets(ctx, tx)
	if err != nil {
		return nil, err
	}
	return next, nil
}

// readDomains indexes the site domains by host, and records each site's API base: the origin of its API domain, the
// host a key's calls are made to, which the console shows the user instead of a URL built from the request's Host
// (README.md「快照」). Prefer HTTPS to avoid sending a key in cleartext when both schemes are registered. Break ties by
// origin so the query's unordered results cannot change the address between reloads.
func readDomains(next *snapshot, domains []ListSiteDomainsRow) {
	for _, r := range domains {
		next.domains[r.Domain] = domain{SiteID(r.SiteID), Surface(r.Surface), r.Origin}
		if site, ok := next.sites[SiteID(r.SiteID)]; ok && Surface(r.Surface) == API &&
			preferAPIOrigin(r.Origin, site.APIBase) {
			site.APIBase = r.Origin
			next.sites[site.ID] = site
		}
	}
}

func preferAPIOrigin(candidate, current string) bool {
	secure, currentSecure := strings.HasPrefix(candidate, "https://"), strings.HasPrefix(current, "https://")
	if secure != currentSecure {
		return secure
	}
	return current == "" || candidate < current
}

// seconds is a session length in seconds as a duration, or fallback when it is unset (0).
func seconds(v int64, fallback time.Duration) time.Duration {
	if v == 0 {
		return fallback
	}
	return time.Duration(v) * time.Second
}

// setting is a site's limit, or fallback when it is unset; the table's bounds keep a set one at 1 or more.
func setting(v *int32, fallback int) int {
	if v == nil {
		return fallback
	}
	return int(*v)
}

// boolSetting is a site's switch, or fallback when it is unset.
func boolSetting(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

// SiteForHost is the site and surface of the request's Host, as written in the request: lower-cased here, with the
// port when it is not the scheme's default. It reads only the snapshot.
func (s *Service) SiteForHost(host string) (Site, Surface, error) {
	d, snap, err := s.domain(host)
	if err != nil {
		return Site{}, "", err
	}
	return snap.sites[d.site], d.surface, nil
}

// Origin is the origin registered for the request's Host: where a page of that domain lives, which a link the server
// builds for it (a payment's return address) must start with, never the Host or Origin header as the request wrote it.
func (s *Service) Origin(host string) (string, error) {
	d, _, err := s.domain(host)
	return d.origin, err
}

// domain is host's registered domain in a snapshot fresh enough to use.
func (s *Service) domain(host string) (domain, *snapshot, error) {
	snap := s.snapshot.Load()
	if snap == nil || s.now().Sub(snap.loaded) > staleAfter {
		return domain{}, nil, ErrStale
	}
	d, ok := snap.domains[strings.ToLower(host)]
	if !ok {
		return domain{}, nil, ErrUnknownHost
	}
	return d, snap, nil
}
