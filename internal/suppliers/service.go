package suppliers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
)

// EndpointID names an endpoint: a group's model at a supplier.
type EndpointID = uuid.UUID

// Config is what a node needs to call suppliers.
type Config struct {
	DB *pg.DB
	// Scope sees every supplier and the journal: the platform scope, which only cmd/ may build.
	Scope  pg.Scope
	Keys   *secret.Keyring
	Egress *egress.Client
	// Registry is the adapters of the program; nil is registry.Default.
	Registry *registry.Registry
	// Clock is the time; nil is time.Now. Credentials expire and cool down by it, not by a change in the database.
	Clock func() time.Time
	// FlushEvery is how often the journal is written; nil is a second.
	FlushEvery time.Duration
	// CredentialCooldown is how long a credential the supplier refused rests; QuotaCooldown is how long one whose
	// allowance is used up rests when the supplier does not say when it returns.
	CredentialCooldown, QuotaCooldown time.Duration
}

// Service is the call side of the suppliers module in one node: the endpoints it can call, the state of their
// credentials, the journal of attempts, and the schedules costs are read from. It does not read the database to pick a
// credential or to price an attempt.
type Service struct {
	cfg      Config
	registry *registry.Registry
	now      func() time.Time

	snap atomic.Pointer[snapshot]
	mu   sync.Mutex
	// What outlives a snapshot: the in-flight count and rest of a credential must not reset when the snapshot is rebuilt
	// while calls run, and a receipt may name a schedule that is no longer the endpoint's.
	runtime   map[uuid.UUID]*credRuntime
	schedules map[uuid.UUID]*catalog.Schedule
	cancels   map[EndpointID]bool // measured by quality: whether the supplier stops when the client does

	journal   *journal
	writeRows func(context.Context, []store.JournalRow) error // set by tests to make a write fail
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
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = time.Second
	}
	if cfg.CredentialCooldown <= 0 {
		cfg.CredentialCooldown = 5 * time.Minute
	}
	if cfg.QuotaCooldown <= 0 {
		cfg.QuotaCooldown = 15 * time.Minute
	}
	s := &Service{
		cfg: cfg, registry: r, now: cfg.Clock, runtime: map[uuid.UUID]*credRuntime{},
		schedules: map[uuid.UUID]*catalog.Schedule{}, cancels: map[EndpointID]bool{}, journal: newJournal(),
	}
	s.snap.Store(&snapshot{endpoints: map[EndpointID]*endpoint{}})
	return s, nil
}

// credRuntime is the state of a credential that changes with calls.
type credRuntime struct {
	inflight atomic.Int32
	restUnix atomic.Int64 // unix nanoseconds until which the credential is not picked
}

// endpoint is what is needed to call one endpoint.
type endpoint struct {
	id, supplier, schedule uuid.UUID
	origin, upstreamName   string
	quirks                 adapter.CallQuirks
	creds                  []*credential
}

type credential struct {
	id      uuid.UUID
	key     secret.Secret
	max     int32
	expires *time.Time
	rt      *credRuntime
}

type snapshot struct{ endpoints map[EndpointID]*endpoint }

// Reload rebuilds the snapshot from the database, decrypting the keys of the credentials in this node's memory. A key
// that does not open, or a supplier of a kind with no adapter, fails the reload and leaves the old snapshot in place:
// it is a configuration to fix, and half a snapshot would route around it silently.
func (s *Service) Reload(ctx context.Context) error {
	var rows []store.ListRoutableRow
	err := s.cfg.DB.ReadTx(ctx, s.cfg.Scope, func(tx pg.Tx) (err error) {
		rows, err = store.New().ListRoutable(ctx, tx)
		return err
	})
	if err != nil {
		return fmt.Errorf("read the endpoints: %w", err)
	}
	next := &snapshot{endpoints: map[EndpointID]*endpoint{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		ep, err := s.endpointOf(next, r)
		if err != nil {
			return err
		}
		c, err := s.credentialOf(r)
		if err != nil {
			return err
		}
		ep.creds = append(ep.creds, c)
	}
	s.snap.Store(next)
	return nil
}

func (s *Service) endpointOf(next *snapshot, r store.ListRoutableRow) (*endpoint, error) {
	if ep, ok := next.endpoints[r.EndpointID]; ok {
		return ep, nil
	}
	a, ok := s.registry.Get(adapter.Kind(r.Kind))
	if !ok {
		return nil, fmt.Errorf("endpoint %s: no adapter of kind %q", r.EndpointID, r.Kind)
	}
	if _, known := s.schedules[r.CostScheduleID]; !known {
		sched, err := catalog.ParseSchedule(r.Schedule)
		if err != nil {
			return nil, fmt.Errorf("endpoint %s: its cost schedule %s: %w", r.EndpointID, r.CostScheduleID, err)
		}
		s.schedules[r.CostScheduleID] = &sched
	}
	ep := &endpoint{
		id: r.EndpointID, supplier: r.SupplierID, schedule: r.CostScheduleID, origin: r.Origin,
		upstreamName: r.UpstreamName, quirks: a.Calls(),
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
	rt, ok := s.runtime[r.CredentialID]
	if !ok {
		rt = &credRuntime{}
		s.runtime[r.CredentialID] = rt
	}
	return &credential{id: r.CredentialID, key: key, max: r.MaxConcurrency, expires: r.ExpiresAt, rt: rt}, nil
}

// SetMeasuredCancels records what quality measured for an endpoint: whether the supplier stops generating when the
// client disconnects. It replaces the adapter's default for that endpoint from then on.
func (s *Service) SetMeasuredCancels(ep EndpointID, cancels bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels[ep] = cancels
}

// Policy is how calls to the endpoint behave: the adapter's default, with the measured cancellation over it.
func (s *Service) policy(ep *endpoint) adapter.CallPolicy {
	p := ep.quirks.Policy()
	s.mu.Lock()
	defer s.mu.Unlock()
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
)
