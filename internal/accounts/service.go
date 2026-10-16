package accounts

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"golang.org/x/sync/singleflight"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ErrUnknownKey is every refusal of an API key, from a malformed one to one whose account is disabled, so a client
// learns nothing from which (README.md「认出与准入」).
var ErrUnknownKey = errors.New("accounts: unknown API key")

// Config is what cmd/ hands the module.
type Config struct {
	Platform pg.Scope         // the platform scope, for the snapshot; only cmd/ can build it
	Hashes   *secret.MACKeys  // keys API keys are hashed with: at most 2 versions, the newest current
	Keyring  *secret.Keyring  // seals users' TOTP secrets
	Now      func() time.Time // the clock; time.Now when nil
	// HashConcurrency is how many password hashes the node computes at once, GOMAXPROCS when 0; one waits at most
	// HashWait for a turn, 2 seconds when 0, and is then refused (README.md「登录前的资源」).
	HashConcurrency int
	HashWait        time.Duration
	// Meter reports the module's metrics; nil for none.
	Meter metric.Meter
	// OnAccountOpened runs in the transaction of every account a registration opens; nil does nothing.
	OnAccountOpened OpenedFunc
}

// Service is the module. One per node; several share a database.
type Service struct {
	db       *pg.DB
	platform pg.Scope
	hashes   *secret.MACKeys
	keyring  *secret.Keyring
	versions []uint32 // of hashes, newest first
	now      func() time.Time
	opened   OpenedFunc
	misses   metric.Int64Counter // key lookups that had to ask the database

	snapshot atomic.Pointer[snapshot]
	// Key identities never change once made, so a hit needs no database. A miss of a key that does not exist is
	// remembered for a minute: 256 random bits are not guessed first and created later.
	known   *lru[keyHash, Caller]
	unknown *lru[keyHash, struct{}]
	lookups singleflight.Group
	guards  guards
	queries *Queries
}

// keyHash is a key on a site, by its hash under the current version.
type keyHash struct {
	site uuid.UUID
	hash [32]byte
}

const (
	cacheLimit     = 200_000
	unknownFor     = time.Minute
	maxKeyVersions = 2 // a key is looked up under each, so each more is another hash per miss
)

// NewService makes the service; Load must succeed before it serves. (New is sqlc's, for the queries.)
func NewService(db *pg.DB, cfg Config) (*Service, error) {
	versions := cfg.Hashes.Versions()
	if len(versions) > maxKeyVersions {
		return nil, fmt.Errorf("AXIOM_KEY_HASH_FILE: at most %d versions, found %d", maxKeyVersions, len(versions))
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	if cfg.Meter == nil {
		cfg.Meter = noop.NewMeterProvider().Meter("accounts")
	}
	misses, err := cfg.Meter.Int64Counter("axiom.accounts.key_cache.misses",
		metric.WithDescription("API key lookups that missed the cache and asked the database."))
	if err != nil {
		panic(err) // the instrument name is a constant
	}
	return &Service{
		misses: misses, db: db, platform: cfg.Platform, hashes: cfg.Hashes, keyring: cfg.Keyring, versions: versions, now: now, opened: cfg.OnAccountOpened,
		known: newLRU[keyHash, Caller](cacheLimit), unknown: newLRU[keyHash, struct{}](cacheLimit),
		guards: newGuards(cfg.HashConcurrency, cfg.HashWait), queries: New(),
	}, nil
}
