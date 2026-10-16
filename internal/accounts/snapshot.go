package accounts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	Registration string // invite or closed
	// Currency is the site's accounting currency, which the amounts its API shows are in.
	Currency platform.Currency
	// NewKeyDailyLimit is the daily spend limit a new key gets, in billionths of a credit.
	NewKeyDailyLimit int64
	sessions         map[Surface]lifetime
	limits           limits
}

// lifetime is how long a session lasts: idle since its last request, and at most from its start.
type lifetime struct{ idle, max time.Duration }

// The defaults of a site that sets none: 10 credits a day for a new key; console sessions idle out after 7 days and
// last at most 30, admin sessions 30 minutes and 12 hours.
const defaultNewKeyDailyLimit = 10_000_000_000

var defaultLifetimes = map[Surface]lifetime{
	Console: {7 * 24 * time.Hour, 30 * 24 * time.Hour},
	Admin:   {30 * time.Minute, 12 * time.Hour},
}

// snapshot is an immutable view of the sites, replaced whole.
type snapshot struct {
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
			NewKeyDailyLimit: defaultNewKeyDailyLimit,
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
		site.limits = limits{
			ipBurst: setting(r.IpAttemptBurst, defaultLimits.ipBurst), ipPerMinute: setting(r.IpAttemptsPerMinute, defaultLimits.ipPerMinute),
			emailFailures: setting(r.EmailSignInFailures, defaultLimits.emailFailures), codeFailures: setting(r.UserCodeFailures, defaultLimits.codeFailures),
			challengeAttempts: setting(r.ChallengeCodeAttempts, defaultLimits.challengeAttempts),
		}
		next.sites[site.ID] = site
	}
	domains, err := s.queries.ListSiteDomains(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, r := range domains {
		next.domains[r.Domain] = domain{SiteID(r.SiteID), Surface(r.Surface), r.Origin}
	}
	roles, err := s.queries.ListRoles(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, r := range roles {
		if unknown := slices.IndexFunc(r.Permissions, func(p string) bool { _, declared := permissions[p]; return !declared }); unknown >= 0 {
			return nil, fmt.Errorf("role %s holds %q, which this program does not declare", r.Name, r.Permissions[unknown])
		}
		next.roleIDs[r.Name] = r.ID
		next.roles[r.ID] = map[string]bool{}
		for _, p := range r.Permissions {
			next.roles[r.ID][p] = true
		}
	}
	return next, nil
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

// SiteForHost is the site and surface of the request's Host, as written in the request: lower-cased here, with the
// port when it is not the scheme's default. It reads only the snapshot.
func (s *Service) SiteForHost(host string) (Site, Surface, error) {
	d, snap, err := s.domain(host)
	if err != nil {
		return Site{}, "", err
	}
	return snap.sites[d.site], d.surface, nil
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
