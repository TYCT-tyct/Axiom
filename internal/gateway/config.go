package gateway

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// Config holds the limits and deadlines the in-process primitives and the later steps share. The defaults are the
// seeds of the back-office settings; the back office checks the relations on save and New checks them at startup,
// so a node with a bad relation does not start (internal/gateway/README.md「超时与限额」).
type Config struct {
	NodeInFlight int // requests served at once, disconnected ones still being read included

	IPConnections int   // connections one client address may hold open
	IPInFlight    int   // requests one client address may have in flight
	MaxBody       int64 // largest request body, in bytes
	KeyRate       int   // requests one key may make in RateWindow before it is refused: the platform's abuse limit
	CountRate     int   // count_tokens requests one key may make in RateWindow: counted apart from its calls

	PoolSize         int // N, database connections
	PoolReserve      int // connections only the Completion class may take
	BatchConnections int // most connections one batch writer (call records, journal) holds

	QueueMax    int // waiters in the queue
	QueuePerKey int // waiters of one key

	KeyLookup        time.Duration // an unknown key's lookup in the database
	RateWindow       time.Duration // the sliding window KeyRate counts over
	AccountGateWait  time.Duration // an admission's wait for its account's gate
	QueueWait        time.Duration // wait for a slot when every candidate endpoint is full
	StartBy          time.Duration // after admission, no new attempt starts
	MaxCall          time.Duration // longest single call
	HeaderWait       time.Duration // an upstream's wait for the response headers; then the next entry
	IdleTimeout      time.Duration // an upstream that sends nothing new for this long: the next entry, or the end of the stream
	KeepAlive        time.Duration // a stream that has written nothing to the client for this long gets a keep-alive
	WriteTimeout     time.Duration // one write to the client; a client that does not take it in time is treated as gone
	ExtendWait       time.Duration // one extension of a reservation, gate included; then the stream is cut short
	AdmissionBudget  time.Duration // gate, connection and transaction of an admission
	LockTimeout      time.Duration // the transaction's lock_timeout; checked, not applied until pg.Tx batches (README「入口与准入」)
	StatementTimeout time.Duration // the transaction's statement_timeout; likewise
	HolderTTL        time.Duration // lifetime of a holder row that is not renewed
	HolderHeartbeat  time.Duration
	ReclaimGrace     time.Duration // wait after the TTL before the cleanup reclaims a holder
	SettleGrace      time.Duration // reservation deadline = admission + MaxCall + SettleGrace
	SettleRetry      time.Duration // how long a settlement is retried before the ending is written without its cost
	SettleBackground time.Duration // how long a failed settlement is retried after SettleRetry, in the background
}

// DefaultConfig returns the defaults of docs/domains/gateway.md and internal/gateway/README.md.
func DefaultConfig() Config {
	return Config{
		NodeInFlight:     5000,
		IPConnections:    1000,
		IPInFlight:       500,
		MaxBody:          32 << 20,
		KeyRate:          6000,
		CountRate:        6000,
		KeyLookup:        time.Second,
		RateWindow:       time.Minute,
		PoolSize:         64,
		PoolReserve:      16,
		BatchConnections: 2,
		QueueMax:         256,
		QueuePerKey:      16,
		AccountGateWait:  200 * time.Millisecond,
		QueueWait:        20 * time.Second,
		StartBy:          120 * time.Second,
		MaxCall:          60 * time.Minute,
		HeaderWait:       60 * time.Second,
		IdleTimeout:      180 * time.Second,
		KeepAlive:        15 * time.Second,
		WriteTimeout:     10 * time.Second,
		ExtendWait:       time.Second,
		AdmissionBudget:  time.Second,
		LockTimeout:      500 * time.Millisecond,
		StatementTimeout: 800 * time.Millisecond,
		HolderTTL:        60 * time.Second,
		HolderHeartbeat:  10 * time.Second,
		ReclaimGrace:     60 * time.Second,
		SettleGrace:      30 * time.Minute,
		SettleRetry:      10 * time.Second,
		SettleBackground: 20 * time.Minute,
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
	// A batch writer that could never be granted would wait forever.
	relation(c.PoolSize-c.PoolReserve >= c.BatchConnections, "PoolSize %d - PoolReserve %d must leave room for BatchConnections %d", c.PoolSize, c.PoolReserve, c.BatchConnections)
	relation(c.QueuePerKey <= c.QueueMax, "QueuePerKey %d must not exceed QueueMax %d", c.QueuePerKey, c.QueueMax)
	relation(c.LockTimeout < c.AdmissionBudget && c.StatementTimeout < c.AdmissionBudget,
		"LockTimeout %v and StatementTimeout %v must be below AdmissionBudget %v", c.LockTimeout, c.StatementTimeout, c.AdmissionBudget)
	relation(c.QueueWait <= c.StartBy && c.StartBy < c.MaxCall,
		"QueueWait %v <= StartBy %v < MaxCall %v must hold", c.QueueWait, c.StartBy, c.MaxCall)
	relation(c.HeaderWait < c.MaxCall && c.IdleTimeout < c.MaxCall && c.KeepAlive < c.IdleTimeout,
		"HeaderWait %v and IdleTimeout %v must be below MaxCall %v, and KeepAlive %v below IdleTimeout", c.HeaderWait, c.IdleTimeout, c.MaxCall, c.KeepAlive)
	// Divided, not multiplied: 3*HolderHeartbeat overflows to a negative number for a huge heartbeat and would pass.
	relation(c.HolderHeartbeat <= c.HolderTTL/3, "HolderTTL %v must be at least 3 x HolderHeartbeat %v", c.HolderTTL, c.HolderHeartbeat)
	relation(c.ReclaimGrace >= c.HolderTTL, "ReclaimGrace %v must be at least HolderTTL %v", c.ReclaimGrace, c.HolderTTL)
	// Subtracted, not added: two huge positives would overflow past the grace.
	relation(c.SettleBackground < c.SettleGrace-c.SettleRetry, "SettleGrace %v must exceed SettleRetry %v + SettleBackground %v",
		c.SettleGrace, c.SettleRetry, c.SettleBackground)
	return errors.Join(errs...)
}

// KeyLookupSlot is accounts.Config.LookupSlot: a lookup of a key that is not cached uses a database connection like an
// admission does, so it takes one of the admission class, for as long as the lookup runs (internal/gateway/README.md
// 「并发、背压与共享状态」). NewHandler refuses an accounts service that was not given it.
func (b *Backpressure) KeyLookupSlot(ctx context.Context) (release func(), err error) {
	return b.Pool.Acquire(ctx, Admission)
}

// Backpressure is the node's in-process limits, built from one validated Config.
type Backpressure struct {
	InFlight *InFlight
	Funds    *Funds
	Pool     *Pool // for the connections that need no account gate: cold-key lookup, batch writers
	Queue    *Queue
	Rate     *RateCounter
	IPConns  *Limiter // connections per client address
	IPCalls  *Limiter // requests in flight per client address
	cfg      Config
}

// New validates cfg and builds the limits; it is the only way to get them, so no limit runs on a bad relation.
func New(cfg Config) (*Backpressure, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("gateway config: %w", err)
	}
	pool := newPool(cfg.PoolSize, cfg.PoolReserve, cfg.BatchConnections)
	return &Backpressure{
		InFlight: &InFlight{node: NewLimiter(ErrOverloaded), accounts: NewLimiter(ErrRateLimited), nodeMax: cfg.NodeInFlight},
		Funds:    &Funds{gate: newAccountGate(cfg.AccountGateWait), pool: pool},
		Pool:     pool,
		Queue:    newQueue(cfg.QueueMax, cfg.QueuePerKey),
		Rate:     NewRateCounter(cfg.RateWindow),
		IPConns:  NewLimiter(ErrRateLimited),
		IPCalls:  NewLimiter(ErrRateLimited),
		cfg:      cfg,
	}, nil
}
