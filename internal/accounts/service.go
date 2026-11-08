package accounts

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync/atomic"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"golang.org/x/sync/singleflight"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ErrAccountSuspended is an otherwise usable key whose account has paused calls.
var ErrAccountSuspended = errors.New("accounts: account calls suspended")

// ErrUnknownKey is an invalid API key; the refusal never reveals which credential check failed.
var ErrUnknownKey = errors.New("accounts: unknown API key")

// Config is what cmd/ hands the module.
type Config struct {
	// Recovery queues reset requests and credential-change notices; nil disables these flows.
	Recovery *PasswordRecovery
	// Registration queues open-registration verification links; nil disables open registration.
	Registration *Registration
	Platform     pg.Scope         // the platform scope, for the snapshot; only cmd/ can build it
	Hashes       *secret.MACKeys  // keys API keys are hashed with: at most 2 versions, the newest current
	Keyring      *secret.Keyring  // seals users' TOTP secrets
	Now          func() time.Time // the clock; time.Now when nil
	// LookupSlot, when set, is asked for a slot before each key lookup or management request, and the slot is given back
	// when that work ends. The lookup is shared by every request waiting for the key and outlives the one that started
	// it, so it is the lookup that holds the slot, once. The gateway passes a connection of its admission class. Nil
	// leaves lookups unbounded, for a service with no gateway in front of it.
	LookupSlot func(ctx context.Context) (release func(), err error)
	// HashConcurrency is how many password hashes the node computes at once, GOMAXPROCS when 0; one waits at most
	// HashWait for a turn, 2 seconds when 0, and is then refused (README.md「登录前的资源」).
	HashConcurrency int
	HashWait        time.Duration
	// Meter reports the module's metrics; nil for none.
	Meter metric.Meter
	// Cache is the shared store the attempts before sign-in are counted in; nil, or a cache that is turned off, leaves
	// them in this node's buckets (README.md「登录前的资源」).
	Cache *kv.Client
	// LoginKey is the HMAC key of the login subjects, which secret derives from the master key file's highest
	// distributed version: the addresses and emails in the cache's keys appear only as its HMAC
	// (internal/platform/kv/README.md「键」). Cache needs it.
	LoginKey secret.LoginKey
	// OnAccountOpened runs in the transaction of every account a registration opens; nil does nothing.
	OnAccountOpened OpenedFunc
	// KeyPreset binds each new call key, and a key whose preset is changed, to a routing preset; nil leaves new keys
	// without one, routed by the system preset, and refuses a chosen preset (ErrKeyPreset).
	KeyPreset KeyPresetFunc
	// Egress makes the provider requests of third-party sign-in; nil uses the production exit. Tests pass a client
	// that reaches their fake provider.
	Egress *egress.Client
}

// Service is the module. One per node; several share a database.
type Service struct {
	db           *pg.DB
	platform     pg.Scope
	hashes       *secret.MACKeys
	keyring      *secret.Keyring
	versions     []uint32 // of hashes, newest first
	now          func() time.Time
	opened       OpenedFunc
	keyPreset    KeyPresetFunc
	recovery     *PasswordRecovery
	registration *Registration
	exit         *egress.Client
	providers    map[string]oauthProvider
	slot         func(ctx context.Context) (func(), error) // the lookups' slot; never nil
	gated        bool                                      // whether Config.LookupSlot was set
	misses       metric.Int64Counter                       // key lookups that had to ask the database

	snapshot atomic.Pointer[snapshot]
	// Key identities never change once made, so a hit needs no database. A miss of a key that does not exist is
	// remembered for a minute: 256 random bits are not guessed first and created later.
	known   *lru[keyHash, Caller]
	unknown *lru[keyHash, struct{}]
	lookups singleflight.Group
	// agentRequests spends each operations credential's requests on this node (AuthenticateAgent).
	agentRequests *buckets[AgentID]
	// agentMoney and agentMoneyHours spend each credential's money writes on this node (SpendAgentMoney), agentAlerts
	// its alerts (SpendAgentAlert).
	agentMoney, agentMoneyHours, agentAlerts *buckets[AgentID]
	// lookupJoined, when set by a test, is called after a request joined the shared lookup, before it waits for the
	// result; it is how a timing test observes every waiter instead of sleeping.
	lookupJoined func()
	guards       guards
	queries      *Queries
	// cache is where the attempts before sign-in are counted, and loginKey the HMAC key of their subjects.
	cache    *kv.Client
	loginKey secret.LoginKey
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
	slot := cfg.LookupSlot
	if slot == nil {
		slot = func(context.Context) (func(), error) { return func() {}, nil }
	}
	if cfg.Meter == nil {
		cfg.Meter = noop.NewMeterProvider().Meter("accounts")
	}
	misses, err := cfg.Meter.Int64Counter("axiom.accounts.key_cache.misses",
		metric.WithDescription("API key lookups that missed the cache and asked the database."))
	if err != nil {
		panic(err) // the instrument name is a constant
	}
	exit := cfg.Egress
	if exit == nil {
		exit = egress.NewDirect()
	}
	return &Service{
		recovery: cfg.Recovery, registration: cfg.Registration, misses: misses, db: db, platform: cfg.Platform, hashes: cfg.Hashes, keyring: cfg.Keyring, versions: versions, now: now, opened: cfg.OnAccountOpened, keyPreset: cfg.KeyPreset,
		exit: exit, providers: maps.Clone(oauthProviders),
		slot: slot, gated: cfg.LookupSlot != nil,
		known: newLRU[keyHash, Caller](cacheLimit), unknown: newLRU[keyHash, struct{}](cacheLimit), agentRequests: newBuckets[AgentID](),
		agentMoney: newBuckets[AgentID](), agentMoneyHours: newBuckets[AgentID](), agentAlerts: newBuckets[AgentID](),
		guards: newGuards(cfg.HashConcurrency, cfg.HashWait), queries: New(),
		cache: cfg.Cache, loginKey: cfg.LoginKey,
	}, nil
}

// GatesLookups reports whether key lookups take a slot (Config.LookupSlot), which a node serving the model API must
// have: without it a flood of unknown keys is bounded by nothing but the database's own connection limit.
func (s *Service) GatesLookups() bool { return s.gated }
