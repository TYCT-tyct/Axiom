// Package payments takes top-ups: merchant accounts on payment channels, quotes, orders, callbacks, refunds and
// reconciliation. README.md holds its interfaces, invariants and data; docs/domains/payments.md the behavior.
package payments

import (
	"container/list"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations is this module's migrations, which cmd/axiom hands to pg.Migrate and pg.DB.Check.
func Migrations() pg.Module {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	return pg.Module{Name: "payments", Migrations: files}
}

var queries = New()

// Config is what Open needs.
type Config struct {
	DB       *pg.DB
	Platform pg.Scope // for the reference exchange rates, which only the platform scope reads
	Keyring  *secret.Keyring
	Client   *egress.Client // what channels reach their gateways through
	Plugins  []Plugin       // one per kind, compiled in
	Logger   *slog.Logger   // where alerts go; default slog.Default()
	Proxies  []netip.Prefix // only these peers may supply a callback's source address
}

// Payments is the module's service.
type Payments struct {
	db          *pg.DB
	platform    pg.Scope
	keyring     *secret.Keyring
	client      *egress.Client
	plugins     map[Kind]Plugin
	logger      *slog.Logger
	turns       sync.Map // turnKey to *atomic.Uint64: the round-robin positions, per process as in sub2api
	mu          sync.Mutex
	valid       map[uuid.UUID]validity // merchants whose configuration was checked, by ID (usable)
	channels    map[MerchantID]channelBinding
	proxies     []netip.Prefix
	callbacks   map[callbackKey]*list.Element
	callbackLRU list.List
}

// Open checks the plugins: two plugins of one kind are a mistake in assembling the program and fail the start.
func Open(c Config) (*Payments, error) {
	if c.DB == nil || c.Keyring == nil {
		return nil, errors.New("payments: a database and a keyring are required")
	}
	p := &Payments{db: c.DB, platform: c.Platform, keyring: c.Keyring, client: c.Client, plugins: map[Kind]Plugin{}, logger: c.Logger, valid: map[uuid.UUID]validity{}}
	p.channels = make(map[MerchantID]channelBinding)
	p.proxies = slices.Clone(c.Proxies)
	p.callbacks = make(map[callbackKey]*list.Element)
	if p.logger == nil {
		p.logger = slog.Default()
	}
	for _, plugin := range c.Plugins {
		if _, ok := p.plugins[plugin.Kind()]; ok {
			return nil, fmt.Errorf("payments: two plugins of kind %q", plugin.Kind())
		}
		p.plugins[plugin.Kind()] = plugin
	}
	return p, nil
}
