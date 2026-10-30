package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

// Config holds the limits and deadlines the in-process primitives and the later steps share. The defaults are the
// seeds of the back-office settings; the back office checks the relations on save and New checks them at startup,
// so a node with a bad relation does not start (internal/gateway/README.md「超时与限额」).
type Config struct {
	NodeInFlight int // requests served at once, disconnected ones still being read included

	IPInFlight int   // requests one client address may have in flight
	MaxBody    int64 // largest request body, in bytes
	KeyRate    int   // requests one key may make in RateWindow before it is refused: the platform's abuse limit
	CountRate  int   // count_tokens requests one key may make in RateWindow: counted apart from its calls

	PoolSize    int // N, gateway permits, derived from the combined database pool ceiling
	PoolReserve int // completion permits; admission permits are PoolSize - PoolReserve

	QueueMax    int // waiters in the queue
	QueuePerKey int // waiters of one key

	KeyLookup         time.Duration // an unknown key's lookup in the database
	RateWindow        time.Duration // the sliding window KeyRate counts over
	AccountGateWait   time.Duration // an admission's wait for its account's gate
	QueueWait         time.Duration // wait for a slot when every candidate endpoint is full
	StartBy           time.Duration // after admission, no new attempt starts
	MaxCall           time.Duration // longest single call
	HeaderWait        time.Duration // an upstream's wait for the response headers; then the next entry
	CompactHeaderWait time.Duration // the same for the Responses compact endpoint, which compacts a whole conversation
	IdleTimeout       time.Duration // an upstream that sends nothing new for this long: the next entry, or the end of the stream
	KeepAlive         time.Duration // a stream that has written nothing to the client for this long gets a keep-alive
	WriteTimeout      time.Duration // one write to the client; a client that does not take it in time is treated as gone
	ExtendWait        time.Duration // one extension of a reservation, gate included; then the stream is cut short
	AdmissionBudget   time.Duration // connection and transaction of an admission, from the moment it holds the account gate
	LockTimeout       time.Duration // the transaction's lock_timeout; checked, not applied until pg.Tx batches (README「入口与准入」)
	StatementTimeout  time.Duration // the transaction's statement_timeout; likewise
	HolderTTL         time.Duration // lifetime of a holder row that is not renewed
	HolderHeartbeat   time.Duration
	ReclaimGrace      time.Duration // wait after the TTL before the cleanup reclaims a holder
	SettleGrace       time.Duration // reservation deadline = admission + MaxCall + SettleGrace
	SettleRetry       time.Duration // how long a settlement is retried before the ending is written without its cost
	SettleBackground  time.Duration // how long a failed settlement is retried after SettleRetry, in the background

	SegmentOutput    int64         // output tokens a segment covers before the reservation is extended
	SegmentOvershoot time.Duration // the extension wait plus how long a cancelled upstream may keep billing
}

// Segments is billing's settings for reserving in segments, taken from this one place by routing and billing, so an
// entry is never marked segmentable by one of them with no sizes for the other to reserve it by (Issue #229).
func (c Config) Segments() billing.Segments {
	return billing.Segments{Output: c.SegmentOutput, Overshoot: c.SegmentOvershoot}
}

// DefaultConfig returns the defaults of docs/domains/gateway.md and internal/gateway/README.md.
func DefaultConfig() Config {
	return Config{
		NodeInFlight:      5000,
		IPInFlight:        500,
		MaxBody:           32 << 20,
		KeyRate:           6000,
		CountRate:         6000,
		KeyLookup:         time.Second,
		RateWindow:        time.Minute,
		PoolSize:          pg.PoolSize,
		PoolReserve:       pg.CompletionReserve,
		QueueMax:          256,
		QueuePerKey:       16,
		AccountGateWait:   5 * time.Second,
		QueueWait:         20 * time.Second,
		StartBy:           120 * time.Second,
		MaxCall:           60 * time.Minute,
		HeaderWait:        60 * time.Second,
		CompactHeaderWait: 10 * time.Minute,
		IdleTimeout:       180 * time.Second,
		KeepAlive:         15 * time.Second,
		WriteTimeout:      10 * time.Second,
		ExtendWait:        time.Second,
		AdmissionBudget:   time.Second,
		LockTimeout:       500 * time.Millisecond,
		StatementTimeout:  800 * time.Millisecond,
		HolderTTL:         60 * time.Second,
		HolderHeartbeat:   10 * time.Second,
		ReclaimGrace:      60 * time.Second,
		SettleGrace:       30 * time.Minute,
		SettleRetry:       10 * time.Second,
		SettleBackground:  20 * time.Minute,
		// A segment of 1000 output tokens at a model's speed extends every few seconds; the allowance covers one
		// ExtendWait (1s) and as long again for a cancelled upstream to stop billing. No model ships an output speed
		// yet, so nothing segments until a spec says one.
		SegmentOutput:    1000,
		SegmentOvershoot: 2 * time.Second,
	}
}

// Validate reports every problem at once, each naming the settings involved.
func (c Config) Validate() error {
	var errs []error
	fields := reflect.ValueOf(c)
	for i := range fields.NumField() {
		if fields.Field(i).Int() <= 0 { // every field is a count or a duration
			errs = append(errs, fmt.Errorf("%s must be positive", fields.Type().Field(i).Name))
		}
	}
	relation := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	relation(c.PoolReserve < c.PoolSize, "PoolReserve %d must be below PoolSize %d", c.PoolReserve, c.PoolSize)
	relation(c.QueuePerKey <= c.QueueMax, "QueuePerKey %d must not exceed QueueMax %d", c.QueuePerKey, c.QueueMax)
	relation(c.LockTimeout < c.AdmissionBudget && c.StatementTimeout < c.AdmissionBudget,
		"LockTimeout %v and StatementTimeout %v must be below AdmissionBudget %v", c.LockTimeout, c.StatementTimeout, c.AdmissionBudget)
	relation(c.QueueWait <= c.StartBy && c.StartBy < c.MaxCall,
		"QueueWait %v <= StartBy %v < MaxCall %v must hold", c.QueueWait, c.StartBy, c.MaxCall)
	relation(c.HeaderWait < c.MaxCall && c.CompactHeaderWait < c.MaxCall && c.IdleTimeout < c.MaxCall && c.KeepAlive < c.IdleTimeout,
		"HeaderWait %v and CompactHeaderWait %v and IdleTimeout %v must be below MaxCall %v, and KeepAlive %v below IdleTimeout",
		c.HeaderWait, c.CompactHeaderWait, c.IdleTimeout, c.MaxCall, c.KeepAlive)
	// Divided, not multiplied: 3*HolderHeartbeat overflows to a negative number for a huge heartbeat and would pass.
	relation(c.HolderHeartbeat <= c.HolderTTL/3, "HolderTTL %v must be at least 3 x HolderHeartbeat %v", c.HolderTTL, c.HolderHeartbeat)
	// The allowance has to cover the wait for an extension, or a stream cut short would have spent more than the
	// reservation set aside for it.
	relation(c.SegmentOvershoot >= c.ExtendWait, "SegmentOvershoot %v must be at least ExtendWait %v", c.SegmentOvershoot, c.ExtendWait)
	relation(c.ReclaimGrace >= c.HolderTTL, "ReclaimGrace %v must be at least HolderTTL %v", c.ReclaimGrace, c.HolderTTL)
	// Subtracted, not added: two huge positives would overflow past the grace.
	relation(c.SettleBackground < c.SettleGrace-c.SettleRetry, "SettleGrace %v must exceed SettleRetry %v + SettleBackground %v",
		c.SettleGrace, c.SettleRetry, c.SettleBackground)
	// The shared count's windows are whole milliseconds, counted from the epoch, and its keys live two windows: a window
	// that is not a whole number of milliseconds numbers and weighs by different lengths, and one over a day makes a key
	// that long-lived for no use. Compared, not multiplied, so no value overflows past the bound (internal/platform/kv/README.md「键」).
	relation(c.RateWindow >= minRateWindow && c.RateWindow <= maxRateWindow && c.RateWindow%time.Millisecond == 0,
		"RateWindow %v must be a whole number of milliseconds from %v to %v", c.RateWindow, minRateWindow, maxRateWindow)
	return errors.Join(errs...)
}

// The shortest and longest RateWindow: a window under a second is a count of a few requests that the clock's own
// granularity decides, and over a day is a key that lives two days.
const (
	minRateWindow = time.Second
	maxRateWindow = 24 * time.Hour
)

// KeyLookupSlot is accounts.Config.LookupSlot: a lookup of a key that is not cached uses a database connection like an
// admission does, so it takes one of the admission class, for as long as the lookup runs (internal/gateway/README.md
// 「并发、背压与共享状态」). NewHandler refuses an accounts service that was not given it.
func (b *Backpressure) KeyLookupSlot(ctx context.Context) (release func(), err error) {
	return b.Pool.Acquire(ctx, Admission)
}

// Backpressure is the node's in-process limits, built from one validated Config.
type Backpressure struct {
	rateLimited metric.Int64Counter           // the 429s answered, by Refusal.Reason
	byReason    map[string][]metric.AddOption // one slice per reason, built once: options passed one by one make a slice for each call
	InFlight    *InFlight
	Funds       *Funds
	Pool        *Pool // for the connections that need no account gate: cold-key lookup
	Queue       *Queue
	AccountRate *RateCounter
	Rate        *RateCounter
	IPCalls     *Limiter // requests in flight per client address
	cfg         Config
	rateCaps    [2]string
	rateTTL     string // how long a rate count lives in the shared cache, in milliseconds as the script takes it: two windows
}

// New validates cfg and builds the limits; it is the only way to get them, so no limit runs on a bad relation. The
// 429s the node answers are counted in meter; nil counts nowhere.
func New(cfg Config, meter metric.Meter) (*Backpressure, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("gateway config: %w", err)
	}
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("gateway")
	}
	rateLimited, err := meter.Int64Counter("axiom.gateway.rate_limited",
		metric.WithDescription("Requests answered 429, by the limit that turned them away (docs/domains/gateway.md「错误」)."))
	if err != nil {
		panic(err) // the instrument name is a constant
	}
	byReason := make(map[string][]metric.AddOption, len(rateLimits))
	for _, r := range rateLimits {
		byReason[r.Reason] = []metric.AddOption{metric.WithAttributes(attribute.String("reason", r.Reason))}
	}
	pool := newPool(cfg.PoolSize, cfg.PoolReserve)
	return &Backpressure{
		rateLimited: rateLimited, byReason: byReason,
		InFlight:    &InFlight{node: NewLimiter(ErrOverloaded), accounts: NewLimiter(errAccountInFlight), nodeMax: cfg.NodeInFlight},
		Funds:       &Funds{gate: newBatchGate(cfg.AccountGateWait), pool: pool, budget: cfg.AdmissionBudget},
		Pool:        pool,
		Queue:       newQueue(cfg.QueueMax, cfg.QueuePerKey),
		Rate:        NewRateCounter(cfg.RateWindow),
		AccountRate: NewRateCounter(cfg.RateWindow),
		IPCalls:     NewLimiter(errIPInFlight),
		cfg:         cfg,
		rateCaps:    [2]string{strconv.Itoa(cfg.KeyRate), strconv.Itoa(cfg.CountRate)},
		rateTTL:     strconv.FormatInt((2 * cfg.RateWindow).Milliseconds(), 10),
	}, nil
}

// refused counts a 429 about to be answered. Every 429 of the node goes through here, and a refusal without a reason is
// a limit that was added and not told apart, which the counter makes visible as reason "".
func (b *Backpressure) refused(ctx context.Context, r *Refusal) {
	if r.Status == http.StatusTooManyRequests {
		opts, ok := b.byReason[r.Reason]
		if !ok { // a limit added without being listed in rateLimits: counted all the same, at the cost of an allocation
			opts = []metric.AddOption{metric.WithAttributes(attribute.String("reason", r.Reason))}
		}
		b.rateLimited.Add(ctx, 1, opts...)
	}
}
