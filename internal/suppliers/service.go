package suppliers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
)

// EndpointID names an endpoint: a group's model at a supplier.
type EndpointID = uuid.UUID

// Routable is an endpoint as a routing snapshot needs it: the catalog model it serves, the name a call must send
// upstream, the formats it speaks, the cost schedule it is priced by — already scaled by its group's ratio — and what
// its supplier does with a stream (internal/suppliers/README.md「调用面」).
type Routable struct {
	Site, Account uuid.UUID
	Credentials   int
	// Capacity reads the endpoint's live credential capacity where a route is planned: whether any credential can take
	// another call now. A routing snapshot holds it, so a plan sees the slots as they are then and not as they were
	// when the snapshot was built.
	Capacity            *Capacity
	Funds               *Funds
	UnitValue           platform.UnitPrice
	UnitValueID         uuid.UUID
	Currency            platform.Currency
	ID, Supplier, Model uuid.UUID
	Upstream            string
	Codename            string // the group's immutable public codename
	Formats             []protocol.Format
	// Generation orders the supplier snapshots a node publishes. A routing build that read an older snapshot must
	// not publish its endpoint configuration after a newer Reload already has (docs/domains/quality.md「实测的能力与上限」).
	Generation       uint64
	Quantization     Quantization // the level an admin recorded; empty is unrecorded
	Schedule         catalog.Schedule
	Streams, Cancels bool
	// PoolLow says the group's pool has little capacity left: routing halves this endpoint's weight. Only a pool whose
	// management API reported a low reading sets it; a relay's endpoints keep false.
	PoolLow bool
	// Byok says the endpoint is the account's own provider key; routing tries it before the platform's endpoints.
	// ByokFallback says that key may still use them when it is unusable (docs/domains/routing.md「自带的供应商 key」).
	Byok, ByokFallback bool
}

// Platform reports whether the endpoint belongs to the platform rather than a tenant.
func (r Routable) Platform() bool { return r.Site == (uuid.UUID{}) && r.Account == (uuid.UUID{}) }

// Config is what a node needs to call suppliers.
type Config struct {
	DB *pg.DB
	// Nodes supplies the production core registry; nil is an isolated single-node service.
	Nodes ShareRegistry
	// Scope sees every supplier: the platform scope, which only cmd/ may build.
	Scope  pg.Scope
	Keys   *secret.Keyring
	Egress *egress.Client
	// Registry is the adapters of the program; nil is registry.Default.
	Registry *registry.Registry
	// Clock is the time; nil is time.Now. Credentials expire and cool down by it, not by a change in the database.
	Clock func() time.Time
	// CredentialCooldown is how long a credential the supplier refused rests; QuotaCooldown is how long one whose
	// allowance is used up rests when the supplier does not say when it returns.
	CredentialCooldown, QuotaCooldown time.Duration
	// MaxStale is how long the snapshot may go without a successful Reload before Routable refuses it; zero is 30
	// seconds, as the catalog's. A supplier disabled or removed while Reload keeps failing stops being routed to then.
	MaxStale time.Duration
	// Logger is told of the suppliers a Reload leaves out; nil is slog.Default.
	Logger *slog.Logger
	// Meter is where the count of those suppliers goes; nil for none.
	Meter metric.Meter
}

// Service is the call side of the suppliers module in one node: the endpoints it can call, the state of their
// credentials, and the schedules costs are read from. It does not read the database to pick a credential or to price an
// attempt.
type Service struct {
	cfg        Config
	registry   *registry.Registry
	now        func() time.Time
	unloadable metric.Int64Counter

	// The snapshot and schedules are built apart from calls and published whole. Shared admission validates the
	// snapshot again under the account's existing lock before a membership handover can be acknowledged.
	// A receipt may name a schedule that is no longer an endpoint's, so schedules only grow.
	snap       atomic.Pointer[snapshot]
	schedules  atomic.Pointer[map[uuid.UUID]*catalog.Schedule]
	shareNodes []kv.ShareNode // guarded by reloadMu; last successful membership reading
	shareUntil time.Time
	balanceMu  sync.Mutex // serializes background balance refreshes
	reloadMu   sync.Mutex // one Reload at a time; no call takes it
	generation uint64     // guarded by reloadMu; the next snapshot's generation
	// What outlives a snapshot: credential slots and rest, and the account's minute, must not reset
	// when the snapshot is rebuilt while calls run. Each lock is held for a lookup, never while anything is parsed or
	// decrypted.
	runtimeMu sync.Mutex
	funds     map[uuid.UUID]*fundsState
	runtime   map[uuid.UUID]*credRuntime
	minutes   map[uuid.UUID]*minuteRuntime
	cancelsMu sync.RWMutex
	cancels   map[EndpointID]bool // measured by quality: whether the supplier stops when the client does

	// A key the provider refused is marked in its row, once, by a worker: the call path does not wait on a database
	// write and a key that many requests find bad writes one row (docs/domains/suppliers.md「自带的供应商 key」).
	rejected   chan rejection
	rejectOnce sync.Once
}

// rejection is the credential a provider refused, its supplier for the log, and its runtime so the in-flight mark can
// be cleared when the write ends.
type rejection struct {
	supplier   uuid.UUID
	credential uuid.UUID
	runtime    *credRuntime
}

// New makes a service with no endpoints; Reload fills it.
func New(cfg Config) (*Service, error) {
	r := cfg.Registry
	if r == nil {
		var err error
		if r, err = registry.Default(); err != nil {
			return nil, fmt.Errorf("adapters: %w", err)
		}
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.CredentialCooldown <= 0 {
		cfg.CredentialCooldown = 5 * time.Minute
	}
	if cfg.QuotaCooldown <= 0 {
		cfg.QuotaCooldown = 15 * time.Minute
	}
	if cfg.MaxStale <= 0 {
		cfg.MaxStale = 30 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Meter == nil {
		cfg.Meter = noop.NewMeterProvider().Meter("suppliers")
	}
	counter, err := cfg.Meter.Int64Counter(unloadableMetric, metric.WithDescription("Suppliers a reload left out of the snapshot because their data could not be loaded, by supplier."))
	if err != nil {
		return nil, fmt.Errorf("meter: %w", err)
	}
	s := &Service{
		unloadable: counter, cfg: cfg, registry: r, now: cfg.Clock, runtime: map[uuid.UUID]*credRuntime{}, cancels: map[EndpointID]bool{},
		minutes: map[uuid.UUID]*minuteRuntime{}, funds: map[uuid.UUID]*fundsState{}, rejected: make(chan rejection, 64),
	}
	s.snap.Store(&snapshot{endpoints: map[EndpointID]*endpoint{}})
	s.schedules.Store(&map[uuid.UUID]*catalog.Schedule{})
	return s, nil
}

// credRuntime is the state of a credential that changes with calls.
// endpoint is what is needed to call one endpoint, and to turn its costs into money: what a unit of its supplier's
// balance is worth in the supplier's settlement currency, and the row that gave the value.
type endpoint struct {
	id, supplier, schedule uuid.UUID
	model                  uuid.UUID
	upstream               string
	codename               string
	formats                []protocol.Format
	quantization           Quantization
	unitValueID            uuid.UUID
	origin                 string
	quirks                 adapter.CallQuirks
	creds                  []*credential
	currency               platform.Currency
	unitValue              platform.UnitPrice
	funds                  *Funds
	// nativeUnit is the unit the supplier counts its balance in, which its prices and its charges are in: what a test's
	// estimate is read as.
	nativeUnit              string
	ownerScope              string
	ownerSite, ownerAccount uuid.UUID
	poolLow                 bool
	byok, byokFallback      bool
}

type credential struct {
	id                              uuid.UUID
	key                             secret.Secret
	configuredMax, configuredMinute int32
	max                             int32
	perMinute                       int32 // account RPM ceiling for selecting this credential, 0 for none
	expires                         *time.Time
	rt                              *credRuntime
}

// snapshot is the endpoints one Reload built. confirmed is when that Reload began reading the database, so the state it
// shows is never older than that; it is zero until the first Reload succeeds.
type snapshot struct {
	endpoints  map[EndpointID]*endpoint
	confirmed  time.Time
	generation uint64    // the Reload that built this snapshot; monotonic within a Service
	until      time.Time // zero for isolated services; shared admission stops before its registry lease expires
}

// Reload rebuilds the snapshot from the database, decrypting the keys of the credentials in this node's memory. A
// supplier whose rows cannot be loaded (a key that does not open, a kind with no adapter, a schedule that does not parse)
// is left out of this node's snapshot whole, logged and counted (unloadableEvent, unloadableMetric); every other supplier
// loads as usual, so one bad row does not take the node down. Nothing is written to the database: the cause may be this
// node's alone (a key version only it lacks), and a status written from a reload could overwrite what an administrator
// just fixed. A Reload that fails altogether (the database cannot be read) leaves the old snapshot
// and its confirmation time in place, and Routable refuses the snapshot once that time is older than MaxStale. The new
// snapshot is built on its own, with no lock the calls use, and replaces the old one in a single step.
func (s *Service) Reload(ctx context.Context) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	asked := s.now()
	var rows []store.ListRoutableRow
	err := s.cfg.DB.ReadTx(ctx, s.cfg.Scope, func(tx pg.Tx) (err error) {
		rows, err = store.New().ListRoutable(ctx, tx)
		return err
	})
	if err != nil {
		return fmt.Errorf("read the endpoints: %w", err)
	}
	s.generation++
	next := &snapshot{endpoints: map[EndpointID]*endpoint{}, confirmed: asked, generation: s.generation}
	schedules := maps.Clone(*s.schedules.Load())
	unloadable := map[uuid.UUID]error{}
	unsupported := map[uuid.UUID]error{}
	for _, r := range rows {
		if _, left := unloadable[r.SupplierID]; left {
			continue
		}
		if err := s.load(next, schedules, r); err != nil {
			if errors.Is(err, store.ErrUnsupportedFormat) {
				// A newer node wrote a format this one cannot serve. Leave that endpoint out, not its supplier: the
				// supplier's other endpoints still serve, and a rolling back release must not lose the whole provider.
				unsupported[r.EndpointID] = err
				continue
			}
			unloadable[r.SupplierID] = err
		}
	}
	for id, ep := range next.endpoints {
		if _, left := unloadable[ep.supplier]; left {
			delete(next.endpoints, id)
		}
	}
	s.schedules.Store(&schedules) // first: the snapshot must never name a schedule the map lacks
	s.applyShares(next)
	s.snap.Store(next)
	s.report(ctx, unloadable)
	for id, cause := range unsupported {
		s.cfg.Logger.Warn("suppliers: an endpoint with a format this node does not know is left out",
			"event", unsupportedFormatEvent, "endpoint", id, "err", cause)
	}
	return nil
}

// load adds the endpoint a row belongs to, if it is not in next yet, and the row's credential to it.
func (s *Service) load(next *snapshot, schedules map[uuid.UUID]*catalog.Schedule, r store.ListRoutableRow) error {
	ep, err := s.endpointOf(next, schedules, r)
	if err != nil {
		return err
	}
	c, err := s.credentialOf(r)
	if err != nil {
		return err
	}
	ep.creds = append(ep.creds, c)
	return nil
}

// report tells each supplier a Reload left out, for alerting: the error log carries a stable event name, and the counter
// rises by one per Reload that leaves the supplier out, so an alert watches a rise.
func (s *Service) report(ctx context.Context, unloadable map[uuid.UUID]error) {
	for id, cause := range unloadable {
		s.cfg.Logger.Error("suppliers: a supplier could not be loaded and is left out of this node's snapshot",
			"event", unloadableEvent, "supplier", id, "err", cause)
		s.unloadable.Add(ctx, 1, metric.WithAttributes(attribute.String("supplier", id.String())))
	}
}

// Routable returns the snapshot's endpoints in id order: those ListRoutable holds, which are the endpoints a call may be
// routed to. A Reload replaces the set whole, so a caller that builds a routing snapshot from this sees one moment of
// the database, never half of a reload. It returns ErrStale when no Reload has succeeded within MaxStale: the endpoints a
// node cannot confirm are not ones it keeps calling.
func (s *Service) Routable() ([]Routable, error) {
	snap, schedules := s.snap.Load(), *s.schedules.Load() // the snapshot first: it never names a schedule the map lacks
	if s.now().Sub(snap.confirmed) > s.cfg.MaxStale {
		return nil, ErrStale
	}
	out := make([]Routable, 0, len(snap.endpoints))
	for _, ep := range snap.endpoints {
		policy := s.policy(ep)
		out = append(out, Routable{
			Site: ep.ownerSite, Account: ep.ownerAccount,
			Credentials: s.availableCredentials(ep), Capacity: &Capacity{s: s, id: ep.id},
			Funds: ep.funds, Currency: ep.currency, UnitValue: ep.unitValue, UnitValueID: ep.unitValueID,
			ID: ep.id, Supplier: ep.supplier, Model: ep.model, Upstream: ep.upstream, Codename: ep.codename, Formats: slices.Clone(ep.formats),
			Generation: snap.generation, Quantization: ep.quantization,
			Schedule: *schedules[ep.schedule], Streams: policy.Streams, Cancels: policy.Cancels, PoolLow: ep.poolLow,
			Byok: ep.byok, ByokFallback: ep.byokFallback,
		})
	}
	slices.SortFunc(out, func(a, b Routable) int { return a.ID.Compare(b.ID) })
	return out, nil
}

func (s *Service) endpointOf(next *snapshot, schedules map[uuid.UUID]*catalog.Schedule, r store.ListRoutableRow) (*endpoint, error) {
	if ep, ok := next.endpoints[r.EndpointID]; ok {
		return ep, nil
	}
	a, ok := s.registry.Get(adapter.Kind(r.Kind))
	if !ok {
		return nil, fmt.Errorf("endpoint %s: no adapter of kind %q", r.EndpointID, r.Kind)
	}
	if _, known := schedules[r.CostScheduleID]; !known {
		sched, err := catalog.ParseSchedule(r.Schedule)
		if err != nil {
			return nil, fmt.Errorf("endpoint %s: its cost schedule %s: %w", r.EndpointID, r.CostScheduleID, err)
		}
		schedules[r.CostScheduleID] = &sched
	}
	formats, err := store.ParseFormats(r.Formats)
	if err != nil {
		return nil, fmt.Errorf("endpoint %s: %w", r.EndpointID, err)
	}
	quantization, err := optionalQuantization(r)
	if err != nil {
		return nil, err
	}
	currency, err := platform.ParseCurrency(r.SettlementCurrency)
	if err != nil {
		return nil, fmt.Errorf("endpoint %s: its supplier's settlement currency: %w", r.EndpointID, err)
	}
	value, err := platform.ParseUnitPrice(r.UnitValue)
	if err != nil || value.Cmp(platform.UnitPrice{}) == 0 {
		return nil, fmt.Errorf("endpoint %s: its supplier's unit value %q is not above 0 and at most 1000 with at most 15 decimals", r.EndpointID, r.UnitValue)
	}
	balance, err := platform.ParseExact(r.BalanceNative)
	if err != nil {
		return nil, fmt.Errorf("endpoint %s: its supplier's native balance: %w", r.EndpointID, err)
	}
	ep := &endpoint{
		id: r.EndpointID, supplier: r.SupplierID, schedule: r.CostScheduleID, model: r.ModelID, upstream: r.UpstreamName, codename: *r.Codename,
		formats: formats, quantization: quantization, origin: r.Origin, quirks: a.Calls(), currency: currency, unitValue: value, unitValueID: r.UnitValueID,
		nativeUnit: r.NativeUnit, ownerScope: r.OwnerScope, funds: s.fundsOf(r.SupplierID, balance, r.BalanceObservedAt),
		poolLow: r.PoolLow.Bool, byok: r.Status == "byok", byokFallback: r.ByokFallback,
	}
	ep.ownerSite, ep.ownerAccount = owner(r.SiteID), owner(r.AccountID)
	next.endpoints[r.EndpointID] = ep
	return ep, nil
}

// owner is the tenant a nullable column names, the zero id for an endpoint of the platform's own.
func owner(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.UUID{}
	}
	return *id
}

// optionalQuantization parses the level an admin recorded, or the empty level when none is recorded: absence is a level
// no request matches, not a synonym for the level OpenRouter names 'unknown'.
func optionalQuantization(r store.ListRoutableRow) (Quantization, error) {
	if r.Quantization == nil {
		return "", nil
	}
	quantization, err := ParseQuantization(*r.Quantization)
	if err != nil {
		return "", fmt.Errorf("endpoint %s: %w", r.EndpointID, err)
	}
	return quantization, nil
}

func (s *Service) credentialOf(r store.ListRoutableRow) (*credential, error) {
	if r.KeyVersion == nil {
		return nil, fmt.Errorf("credential %s is active without a key", r.CredentialID)
	}
	at := secret.Location{Table: "suppliers.credentials", RowID: r.CredentialID.String(), Purpose: "key"}
	key, err := s.cfg.Keys.Open(secret.Sealed{
		Ciphertext: r.Ciphertext, Nonce: r.Nonce, WrappedDEK: r.WrappedDek,
		KeyVersion: uint32(*r.KeyVersion), //nolint:gosec // the column holds 1 to 2³²-1
	}, at)
	if err != nil {
		return nil, fmt.Errorf("credential %s: its key does not open: %w", r.CredentialID, err)
	}
	return &credential{
		id: r.CredentialID, key: key, max: r.MaxConcurrency, perMinute: ceiling(r.RequestsPerMinute),
		configuredMax: r.MaxConcurrency, configuredMinute: ceiling(r.RequestsPerMinute),
		expires: r.ExpiresAt, rt: s.runtimeOf(r.CredentialID, r.SupplierID, r.MaxConcurrency, ceiling(r.RequestsPerMinute)),
	}, nil
}

// SetMeasuredCancels records what quality measured for an endpoint: whether the supplier stops generating when the
// client disconnects. It replaces the adapter's default for that endpoint from then on.
func (s *Service) SetMeasuredCancels(ep EndpointID, cancels bool) {
	s.cancelsMu.Lock()
	defer s.cancelsMu.Unlock()
	s.cancels[ep] = cancels
}

// Policy is how calls to the endpoint behave: the adapter's default, with the measured cancellation over it.
func (s *Service) policy(ep *endpoint) adapter.CallPolicy {
	p := ep.quirks.Policy()
	s.cancelsMu.RLock()
	defer s.cancelsMu.RUnlock()
	if measured, ok := s.cancels[ep.id]; ok {
		p.Cancels = measured
	}
	return p
}

// ErrNoEndpoint is the error for an endpoint the snapshot does not hold; ErrNoSlot is the one when every credential of
// it is resting, expired or at its limit. ErrSupplierResting is an account-wide balance refusal.
// gateway skips the entry in each case and it is not an attempt.
var (
	ErrNoEndpoint      = errors.New("suppliers: the endpoint is not in the snapshot")
	ErrSupplierResting = errors.New("suppliers: the supplier is resting after a balance refusal")
	ErrNoSlot          = errors.New("suppliers: no credential of the endpoint has a free slot")
	// ErrStale is the snapshot's: no Reload has confirmed it within MaxStale.
	ErrStale = errors.New("suppliers: the snapshot has not been confirmed lately")
)

const (
	// unloadableEvent is the stable name of the error log for a supplier left out of the snapshot, as the queue's
	// discarded jobs have theirs (internal/platform/pg/queue.go).
	unloadableEvent        = "suppliers.unloadable"
	unsupportedFormatEvent = "suppliers.unsupported_format"
	unloadableMetric       = "axiom.suppliers.unloadable"
)

// availableCredentials counts eligible keys without consuming a concurrency or RPM slot.
func (s *Service) availableCredentials(ep *endpoint) int {
	now := s.now()
	n := 0
	for _, c := range ep.creds {
		if (c.expires == nil || now.Before(*c.expires)) && now.UnixNano() >= c.rt.restUnix.Load() {
			n++
		}
	}
	return n
}
