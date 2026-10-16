package accounts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

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
	ID        SiteID
	Main      bool
	KeyPrefix string
	// NewKeyDailyLimit is the daily spend limit a new key gets, in billionths of a credit.
	NewKeyDailyLimit int64
}

// defaultNewKeyDailyLimit is the new-key daily limit of a site that sets none: 10 credits.
const defaultNewKeyDailyLimit = 10_000_000_000

// permissions is every permission the code declares; none yet, so a role holding any stops the start, as a role
// naming a permission this program does not know would.
var permissions = map[string]bool{}

// snapshot is an immutable view of the sites, replaced whole.
type snapshot struct {
	loaded  time.Time
	sites   map[SiteID]Site
	domains map[string]domain // by host
}

type domain struct {
	site    SiteID
	surface Surface
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
	next := &snapshot{loaded: s.now(), sites: map[SiteID]Site{}, domains: map[string]domain{}}
	sites, err := s.queries.ListSites(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, r := range sites {
		site := Site{ID: SiteID(r.ID), Main: r.IsMain, KeyPrefix: r.KeyPrefix, NewKeyDailyLimit: defaultNewKeyDailyLimit}
		if r.NewKeyDailyLimit != nil {
			site.NewKeyDailyLimit = *r.NewKeyDailyLimit
		}
		next.sites[site.ID] = site
	}
	domains, err := s.queries.ListSiteDomains(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, r := range domains {
		next.domains[r.Domain] = domain{SiteID(r.SiteID), Surface(r.Surface)}
	}
	roles, err := s.queries.ListRoles(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, r := range roles {
		if unknown := slices.IndexFunc(r.Permissions, func(p string) bool { return !permissions[p] }); unknown >= 0 {
			return nil, fmt.Errorf("role %s holds %q, which this program does not declare", r.Name, r.Permissions[unknown])
		}
	}
	return next, nil
}

// SiteForHost is the site and surface of the request's Host, as written in the request: lower-cased here, with the
// port when it is not the scheme's default. It reads only the snapshot.
func (s *Service) SiteForHost(host string) (Site, Surface, error) {
	snap := s.snapshot.Load()
	if snap == nil || s.now().Sub(snap.loaded) > staleAfter {
		return Site{}, "", ErrStale
	}
	d, ok := snap.domains[strings.ToLower(host)]
	if !ok {
		return Site{}, "", ErrUnknownHost
	}
	return snap.sites[d.site], d.surface, nil
}
