package gateway

import (
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

	PoolSize         int // N, database connections
	PoolReserve      int // connections only the Completion class may take
	BatchConnections int // most connections one batch writer (call records, journal) holds

	QueueMax    int // waiters in the queue
	QueuePerKey int // waiters of one key

	AccountGateWait  time.Duration // an admission's wait for its account's gate
	QueueWait        time.Duration // wait for a slot when every candidate endpoint is full
	StartBy          time.Duration // after admission, no new attempt starts
	MaxCall          time.Duration // longest single call
	AdmissionBudget  time.Duration // gate, connection and transaction of an admission
	LockTimeout      time.Duration // the transaction's lock_timeout
	StatementTimeout time.Duration // the transaction's statement_timeout
	HolderTTL        time.Duration // lifetime of a holder row that is not renewed
	HolderHeartbeat  time.Duration
	ReclaimGrace     time.Duration // wait after the TTL before the cleanup reclaims a holder
	SettleGrace      time.Duration // reservation deadline = admission + MaxCall + SettleGrace
	SettleBackground time.Duration // how long a failed settlement is retried after the first 10s
}

// DefaultConfig returns the defaults of docs/domains/gateway.md and internal/gateway/README.md.
func DefaultConfig() Config {
	return Config{
		NodeInFlight:     5000,
		PoolSize:         64,
		PoolReserve:      16,
		BatchConnections: 2,
		QueueMax:         256,
		QueuePerKey:      16,
		AccountGateWait:  200 * time.Millisecond,
		QueueWait:        20 * time.Second,
		StartBy:          120 * time.Second,
		MaxCall:          60 * time.Minute,
		AdmissionBudget:  time.Second,
		LockTimeout:      500 * time.Millisecond,
		StatementTimeout: 800 * time.Millisecond,
		HolderTTL:        60 * time.Second,
		HolderHeartbeat:  10 * time.Second,
		ReclaimGrace:     60 * time.Second,
		SettleGrace:      30 * time.Minute,
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
	// Divided, not multiplied: 3*HolderHeartbeat overflows to a negative number for a huge heartbeat and would pass.
	relation(c.HolderHeartbeat <= c.HolderTTL/3, "HolderTTL %v must be at least 3 x HolderHeartbeat %v", c.HolderTTL, c.HolderHeartbeat)
	relation(c.ReclaimGrace >= c.HolderTTL, "ReclaimGrace %v must be at least HolderTTL %v", c.ReclaimGrace, c.HolderTTL)
	relation(c.SettleGrace > c.SettleBackground, "SettleGrace %v must exceed SettleBackground %v", c.SettleGrace, c.SettleBackground)
	return errors.Join(errs...)
}

// Backpressure is the node's in-process limits, built from one validated Config.
type Backpressure struct {
	InFlight *InFlight
	Funds    *Funds
	Pool     *Pool // for the connections that need no account gate: cold-key lookup, batch writers
	Queue    *Queue
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
		Queue:    newQueue(cfg.QueueWait, cfg.QueueMax, cfg.QueuePerKey),
	}, nil
}
