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
	Currency            platform.Currency
	ID, Supplier, Model uuid.UUID
	Upstream            string
	Formats             []protocol.Format
	Schedule            catalog.Schedule
	Streams, Cancels    bool
}

// Config is what a node needs to call suppliers.
type Config struct {
	DB *pg.DB
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

	// The snapshot and the schedules are built apart from the calls and published whole; a call reads them with one
	// atomic load and no lock. A receipt may name a schedule that is no longer an endpoint's, so schedules only grow.
	snap      atomic.Pointer[snapshot]
	schedules atomic.Pointer[map[uuid.UUID]*catalog.Schedule]
	reloadMu  sync.Mutex // one Reload at a time; no call takes it
	// What outlives a snapshot: credential slots and rest, and the account's minute, must not reset
	// when the snapshot is rebuilt while calls run. Each lock is held for a lookup, never while anything is parsed or
	// decrypted.
	runtimeMu sync.Mutex
	runtime   map[uuid.UUID]*credRuntime
	minutes   map[uuid.UUID]*minuteRuntime
	cancelsMu sync.RWMutex
	cancels   map[EndpointID]bool // measured by quality: whether the supplier stops when the client does
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
		minutes: map[uuid.UUID]*minuteRuntime{},
	}
	s.snap.Store(&snapshot{endpoints: map[EndpointID]*endpoint{}})
	s.schedules.Store(&map[uuid.UUID]*catalog.Schedule{})
	return s, nil
}

// credRuntime is the state of a credential that changes with calls.
type credRuntime struct {
	inflight atomic.Int32
	restUnix atomic.Int64 // unix nanoseconds until which the credential is not picked
	requests *minuteRuntime
}

// minuteRuntime is the requests one upstream account made in the rolling minute, as this node counted them. It is a
// ring of one-second buckets, each stamped with the second it counts: a bucket from before the window reads as zero,
// so no timer clears anything and memory does not grow with the ceiling. The clock is read while mu is held, so a caller
// delayed between choosing a credential and taking its slot is counted at the second it actually enters, not at the
// earlier time Acquire looked at.
type minuteRuntime struct {
	// The check, the clock read and the increment share a lock so concurrent callers cannot spend the last request
	// twice, or let an earlier caller write after a later one has moved the window.
	mu      sync.Mutex
	now     func() time.Time // the service's clock; nil is time.Now
	started bool
	epoch   time.Time // the first admission; bucket seconds are elapsed from it
	last    int64     // the highest second taken, so a clock that goes back cannot reopen a bucket
	buckets [rpmBuckets]rpmBucket
}

// rpmBucket is one second's requests. second is the runtime's monotonic second the count belongs to, so a slot reused
// by a later second reads as empty without being cleared.
type rpmBucket struct {
	second int64
	count  int32
}

// rpmBuckets is one more than the seconds a rolling minute spans: the buckets for [now-60, now] cover every rolling
// 60-second window that ends now. A request stamped in a second counts until that second is 60 behind, so admission
// under-admits by at most the part of a second before its first request; no rolling window exceeds the ceiling.
const rpmBuckets = 61

// rpmBucketCount caps one second's count. No ceiling is above MaxRequestsPerMinute, so a full bucket refuses every
// finite ceiling, and requests from unlimited credentials cannot overflow the count.
const rpmBucketCount = MaxRequestsPerMinute

// count is a hint shared by an endpoint's credentials; take still decides admission under the lock with a clock read
// at that moment.
func (rt *minuteRuntime) count(now time.Time) int64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.sum(rt.second(now))
}

// second is at in the runtime's monotonic seconds, never below the last second taken. It must be called with mu held.
// time.Time.Sub uses the monotonic reading when the clock has one, so a wall-clock step back does not reopen a bucket.
func (rt *minuteRuntime) second(at time.Time) int64 {
	if !rt.started {
		return 0
	}
	elapsed := at.Sub(rt.epoch)
	if elapsed < 0 {
		return rt.last
	}
	s := int64(elapsed / time.Second)
	if s < rt.last {
		return rt.last
	}
	return s
}

// sum is the requests in the buckets for [now-60, now]; buckets stamped with another second are zero.
func (rt *minuteRuntime) sum(now int64) int64 {
	var n int64
	for _, b := range rt.buckets {
		if b.second >= now-rpmBuckets+1 && b.second <= now {
			n += int64(b.count)
		}
	}
	return n
}

// take counts one request at the clock's current time, and says whether it was within the limit; a credential without
// one takes every request. The clock read, sum and increment are one step under mu, so two callers cannot both take the
// last request of a window, and a caller delayed after Acquire cannot count at an earlier second than one that already
// took a request (README.md「并发与速率」). A request counted here is not given back: it may have reached the supplier,
// whose own count it is in.
func (rt *minuteRuntime) take(limit int32) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	now := rt.now
	if now == nil {
		now = time.Now
	}
	at := now()
	if !rt.started {
		rt.started = true
		rt.epoch = at
	}
	second := rt.second(at)
	if second > rt.last {
		rt.last = second
	}
	if limit != 0 && rt.sum(second)+1 > int64(limit) {
		return false
	}
	b := &rt.buckets[bucketOf(second)]
	if b.second != second { // the slot held a second at least a minute ago; its count has left the window
		*b = rpmBucket{second: second}
	}
	// Unlimited keys still consume their account's quota.
	if b.count < rpmBucketCount {
		b.count++
	}
	return true
}

// bucketOf is the ring slot of a monotonic second.
func bucketOf(second int64) int {
	return int((second%rpmBuckets + rpmBuckets) % rpmBuckets)
}

// ceiling is a per-minute limit as the snapshot holds it: a column with no value is no ceiling of Axiom's, which is
// zero here. A value above MaxRequestsPerMinute is an old node's write during a rolling release; the new code reads it
// as the cap rather than letting the account exceed the bounded window.
func ceiling(n *int32) int32 {
	if n == nil {
		return 0
	}
	if *n > MaxRequestsPerMinute {
		return MaxRequestsPerMinute
	}
	return *n
}

// endpoint is what is needed to call one endpoint, and to turn its costs into money: what a unit of its supplier's
// balance is worth in the supplier's settlement currency, and the row that gave the value.
type endpoint struct {
	id, supplier, schedule uuid.UUID
	model                  uuid.UUID
	upstream               string
	formats                []protocol.Format
	unitValueID            uuid.UUID
	origin                 string
	quirks                 adapter.CallQuirks
	creds                  []*credential
	currency               platform.Currency
	unitValue              platform.UnitPrice
	// nativeUnit is the unit the supplier counts its balance in, which its prices and its charges are in: what a test's
	// estimate is read as.
	nativeUnit string
	ownerScope string
}

type credential struct {
	id        uuid.UUID
	key       secret.Secret
	max       int32
	perMinute int32 // account RPM ceiling for selecting this credential, 0 for none
	expires   *time.Time
	rt        *credRuntime
}

// snapshot is the endpoints one Reload built. confirmed is when that Reload began reading the database, so the state it
// shows is never older than that; it is zero until the first Reload succeeds.
type snapshot struct {
	endpoints map[EndpointID]*endpoint
	confirmed time.Time
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
	next := &snapshot{endpoints: map[EndpointID]*endpoint{}, confirmed: asked}
	schedules := maps.Clone(*s.schedules.Load())
	unloadable := map[uuid.UUID]error{}
	for _, r := range rows {
		if _, left := unloadable[r.SupplierID]; left {
			continue
		}
		if err := s.load(next, schedules, r); err != nil {
			unloadable[r.SupplierID] = err
		}
	}
	for id, ep := range next.endpoints {
		if _, left := unloadable[ep.supplier]; left {
			delete(next.endpoints, id)
		}
	}
	s.schedules.Store(&schedules) // first: the snapshot must never name a schedule the map lacks
	s.snap.Store(next)
	s.report(ctx, unloadable)
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
			Currency: ep.currency,
			ID:       ep.id, Supplier: ep.supplier, Model: ep.model, Upstream: ep.upstream, Formats: slices.Clone(ep.formats),
			Schedule: *schedules[ep.schedule], Streams: policy.Streams, Cancels: policy.Cancels,
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
	currency, err := platform.ParseCurrency(r.SettlementCurrency)
	if err != nil {
		return nil, fmt.Errorf("endpoint %s: its supplier's settlement currency: %w", r.EndpointID, err)
	}
	value, err := platform.ParseUnitPrice(r.UnitValue)
	if err != nil || value.Cmp(platform.UnitPrice{}) == 0 {
		return nil, fmt.Errorf("endpoint %s: its supplier's unit value %q is not above 0 and at most 1000 with at most 15 decimals", r.EndpointID, r.UnitValue)
	}
	ep := &endpoint{
		id: r.EndpointID, supplier: r.SupplierID, schedule: r.CostScheduleID, model: r.ModelID, upstream: r.UpstreamName,
		formats: formats, origin: r.Origin, quirks: a.Calls(), currency: currency, unitValue: value, unitValueID: r.UnitValueID,
		nativeUnit: r.NativeUnit, ownerScope: r.OwnerScope,
	}
	next.endpoints[r.EndpointID] = ep
	return ep, nil
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
		expires: r.ExpiresAt, rt: s.runtimeOf(r.CredentialID, r.SupplierID),
	}, nil
}

// runtimeOf is the running state of a credential, made when it is first seen and kept after.
func (s *Service) runtimeOf(id, supplier uuid.UUID) *credRuntime {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	rt, ok := s.runtime[id]
	if !ok {
		// new-api middleware/model-rate-limit.go (206e0fa5) and sub2api repository/user_rpm_cache.go (8f2cbb53)
		// count upstream accounts; another key of the same supplier must not create another quota.
		requests := s.minutes[supplier]
		if requests == nil {
			requests = &minuteRuntime{now: s.now}
			s.minutes[supplier] = requests
		}
		rt = &credRuntime{requests: requests}
		s.runtime[id] = rt
	}
	return rt
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
// it is resting, expired or at its limit. gateway skips the entry in both cases and it is not an attempt.
var (
	ErrNoEndpoint = errors.New("suppliers: the endpoint is not in the snapshot")
	ErrNoSlot     = errors.New("suppliers: no credential of the endpoint has a free slot")
	// ErrStale is the snapshot's: no Reload has confirmed it within MaxStale.
	ErrStale = errors.New("suppliers: the snapshot has not been confirmed lately")
)

const (
	// unloadableEvent is the stable name of the error log for a supplier left out of the snapshot, as the queue's
	// discarded jobs have theirs (internal/platform/pg/queue.go).
	unloadableEvent  = "suppliers.unloadable"
	unloadableMetric = "axiom.suppliers.unloadable"
)
