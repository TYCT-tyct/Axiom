package billing

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations is this module's migrations, which cmd/axiom hands to pg.Migrate and pg.DB.Check.
func Migrations() pg.Module {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	return pg.Module{Name: "billing", Migrations: files}
}

// Config is what billing's database work needs from the site's settings (internal/gateway/README.md「超时与限额」).
type Config struct {
	HolderTTL time.Duration // how long a heartbeat keeps the holder alive
	Heartbeat time.Duration // how often Run pushes the holder's expiry out; default a third of the TTL
	ReapGrace time.Duration // how long past its expiry a holder is reaped
	Lifetime  time.Duration // a reservation's deadline after admission: the longest call plus the settlement grace
	Segments  Segments
	Logger    *slog.Logger // where alerts go; default slog.Default()
	Meter     metric.Meter // where the platform-borne counters go; nil for none
	Alerts    alert.Sink
}

// heartbeatLimit is the longest period a heartbeat may have: one heartbeat missed must still leave the holder alive
// by the database's clock, and Alive keeps a margin back on top of that (internal/gateway/config.go:118).
func (c Config) heartbeatLimit() time.Duration { return c.HolderTTL / 3 }

// Billing writes reservations under one holder, this start of the process.
type Billing struct {
	db        *pg.DB
	platform  pg.Scope // for the holder rows, which only the platform scope writes
	holder    uuid.UUID
	config    Config
	suppliers Suppliers
	logger    *slog.Logger
	beat      atomic.Int64 // unix nanoseconds of the last time the holder's expiry was known to be pushed out
	// borne counts the settlements the platform bore part of, and borneAmount what it bore, in billionths of a credit.
	borne, borneAmount metric.Int64Counter
}

// holderMargin is how much of the holder's TTL Alive keeps back: a write that starts inside it may find the holder
// expired by the time it is fenced.
const holderMargin = 5 * time.Second

// ErrTerminated is the refusal of a write whose holder was reaped or expired, or of an extension of a reservation
// that already ended. The call it belongs to must stop: cleanup owns its reservation now.
var ErrTerminated = errors.New("billing: the reservation's holder was reaped or the reservation ended")

// ErrReaped is a heartbeat finding its holder reaped: the process must stop admitting and start over with a new
// holder (Start).
var ErrReaped = errors.New("billing: the holder was reaped")

// Start checks the fallback prices, without valid ones billing does not run (docs/domains/billing.md「价目表缺项」), and
// registers a new holder. platform is the platform scope, which only cmd/ builds.
func Start(ctx context.Context, db *pg.DB, platform pg.Scope, config Config, s Suppliers) (*Billing, error) {
	config.Heartbeat = cmp.Or(config.Heartbeat, config.heartbeatLimit())
	if config.Heartbeat <= 0 || config.Heartbeat > config.heartbeatLimit() {
		return nil, fmt.Errorf("billing: the heartbeat %v must be above 0 and at most a third of the holder's TTL %v",
			config.Heartbeat, config.HolderTTL)
	}
	b := &Billing{
		db: db, platform: platform, holder: uuid.NewV7(), config: config, suppliers: s,
		logger: cmp.Or(config.Logger, slog.Default()),
	}
	meter := config.Meter
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("billing")
	}
	b.borne = counter(meter, "axiom.billing.platform_borne", "Settlements whose price the account's floor kept it from paying in full.")
	b.borneAmount = counter(meter, "axiom.billing.platform_borne.amount", "What the platform bore of those settlements, in billionths of a credit.")
	var registered time.Time
	err := db.Tx(ctx, platform, func(tx pg.Tx) error {
		if err := CheckFallbacks(ctx, tx); err != nil {
			return err
		}
		if _, err := initializeSettlementAlerts(ctx, tx); err != nil {
			return err
		}
		registered = time.Now()
		if err := queries.InsertHolder(ctx, tx, InsertHolderParams{ID: b.holder, TtlSeconds: config.HolderTTL.Seconds()}); err != nil {
			return fmt.Errorf("billing: register the holder: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	b.beat.Store(registered.UnixNano())
	return b, nil
}

// alert reports each unknown item seen for the first time: its model has left routing and needs an administrator.
func (b *Billing) alert(seen []UnknownItem) {
	for _, u := range seen {
		b.logger.Error("billing: an unknown billing item appeared; the model takes no new requests until it is priced",
			"model", u.Model.String(), "field", u.Field, "request", u.Request.String())
		b.signal("unknown_items", map[string]string{"model": u.Model.String(), "field": u.Field})
	}
}

func (b *Billing) signal(key string, fields map[string]string) {
	if b.config.Alerts != nil {
		if err := b.config.Alerts.Raise("billing.settlement_anomaly", key, "critical", fields); err != nil {
			b.logger.Error("billing: reporting settlement anomaly failed", "err", err)
		}
	}
}

// counter makes a counter, whose name is a constant.
func counter(m metric.Meter, name, description string) metric.Int64Counter {
	c, err := m.Int64Counter(name, metric.WithDescription(description))
	if err != nil {
		panic(err) // the instrument name is a constant
	}
	return c
}

// bear reports what the platform bore of a settlement that has committed: the part of the price the account's floor did
// not let it pay (docs/domains/billing.md「红线」). It is the platform's loss and nobody else's signal, so it is logged at
// error and counted; the amounts are in billionths of a credit.
func (b *Billing) bear(ctx context.Context, settlement event, price, charged int64) {
	if price <= charged {
		return
	}
	borne := price - charged
	b.logger.Error("billing: the platform bears part of a settlement; the account could not pay it in full",
		"request", settlement.request.String(), "price", price, "charged", charged, "borne", borne)
	b.borne.Add(ctx, 1)
	b.borneAmount.Add(ctx, borne)
	if b.config.Alerts != nil {
		if err := b.config.Alerts.RaiseOnce(settlementAlertSource(settlement), "billing.settlement_anomaly", "platform_borne", "critical", map[string]string{"request": settlement.request.String(), "borne": fmt.Sprint(borne)}); err != nil {
			b.logger.Error("billing: reporting settlement anomaly failed", "err", err)
		}
	}
}

// Heartbeat extends the holder's life by the TTL from now, by the database's clock.
func (b *Billing) Heartbeat(ctx context.Context) error {
	sent := time.Now() // before the write: the expiry it sets is at least this far out
	err := b.db.Tx(ctx, b.platform, func(tx pg.Tx) error {
		n, err := queries.Heartbeat(ctx, tx, HeartbeatParams{ID: b.holder, TtlSeconds: b.config.HolderTTL.Seconds()})
		if err != nil {
			return fmt.Errorf("billing: heartbeat: %w", err)
		}
		if n == 0 {
			return ErrReaped
		}
		return nil
	})
	if err == nil {
		b.beat.Store(sent.UnixNano())
	}
	return err
}

// Alive reports, from memory, whether this process's holder can still be taken to be alive: the last successful
// heartbeat, or the registration, is less than the TTL minus a margin ago. A call checks it before it starts an
// attempt, so that a process cut off from the database stops spending money its reservations may no longer cover
// (internal/gateway/README.md「尝试」). False is not a verdict that the holder was reaped: only a heartbeat finding
// that says so (ErrReaped).
func (b *Billing) Alive() bool {
	return time.Now().UnixNano() < b.beat.Load()+int64(b.config.HolderTTL-holderMargin)
}

// Reap marks every holder that expired more than the grace ago as reaped and returns how many. Its FOR UPDATE waits
// for their fenced writes in flight to finish, so once it commits none of them can add or extend a reservation, and
// cleanup sees all they committed.
func (b *Billing) Reap(ctx context.Context) (reaped int, err error) {
	err = b.db.Tx(ctx, b.platform, func(tx pg.Tx) error {
		ids, err := queries.LockExpiredHolders(ctx, tx, b.config.ReapGrace.Seconds())
		if err != nil || len(ids) == 0 {
			return err
		}
		reaped = len(ids)
		return queries.ReapHolders(ctx, tx, ids)
	})
	if err != nil {
		return 0, fmt.Errorf("billing: reap holders: %w", err)
	}
	return reaped, nil
}

// fence locks the holder row for the rest of tx if the holder is still alive by the database's clock.
func (b *Billing) fence(ctx context.Context, tx pg.Tx) error {
	_, err := queries.FenceHolder(ctx, tx, b.holder)
	return fenceErr(err)
}

func fenceErr(err error) error {
	if errors.Is(err, pg.ErrNoRows) {
		return ErrTerminated
	}
	if err != nil {
		return fmt.Errorf("billing: fence the holder: %w", err)
	}
	return nil
}

// The cleanup's pace: a sweep every 30 seconds takes over at most cleanupBatch reservations in each of at most
// cleanupRounds batches. The work of a sweep is bounded so that a backlog drains over the sweeps that follow instead of
// one transaction holding locks for minutes.
const (
	cleanupEvery  = 30 * time.Second
	cleanupBatch  = 200
	cleanupRounds = 5
)

// Run keeps this process's holder alive and lets cleanup take over what no Settle will, until ctx ends: a sweep at
// once and then every 30 seconds, a heartbeat every Config.Heartbeat. The two run apart, so a sweep stuck behind a
// lock cannot stop the heartbeat and a heartbeat on a hung connection cannot stop the sweeps; each heartbeat has the
// period for its own deadline. A heartbeat finding the holder reaped ends Run with ErrReaped: from then on nothing this
// process writes is fenced in, so it must stop serving and start over with a new holder (cmd/axiom). Any other failure
// is logged and retried on the next beat; meanwhile Alive turns false, which stops new attempts
// (internal/gateway/README.md「尝试」).
//
// The heartbeat still shares the database pool with the calls; a connection of its own, which a pool exhausted by
// calls cannot starve, is not done yet (see internal/billing/README.md「持有者」).
func (b *Billing) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reaped := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() { reaped <- b.beats(ctx) })
	wg.Go(func() { b.sweeps(ctx) })
	err := <-reaped
	cancel()
	wg.Wait()
	return err
}

// beats pushes the holder's expiry out every Config.Heartbeat until ctx ends (nil) or the holder is found reaped.
func (b *Billing) beats(ctx context.Context) error {
	tick := time.NewTicker(b.config.Heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			beat, cancel := context.WithTimeout(ctx, b.config.Heartbeat)
			err := b.Heartbeat(beat)
			cancel()
			if errors.Is(err, ErrReaped) {
				b.logger.Error("billing: the holder was reaped; this node stops taking requests")
				return err
			}
			if err != nil && ctx.Err() == nil {
				b.logger.Error("billing: the heartbeat failed; no attempt starts until it succeeds", "err", err)
			}
		}
	}
}

// sweeps runs a sweep at once, for the predecessor's leftovers, and then every cleanupEvery until ctx ends. The cleanup's
// cursor lives here: a sweep that did not reach the end of the due set goes on from where it stopped.
func (b *Billing) sweeps(ctx context.Context) {
	tick := time.NewTicker(cleanupEvery)
	defer tick.Stop()
	var after uuid.UUID
	for {
		after = b.sweep(ctx, after)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// sweep reaps the holders that expired, then takes over what no settlement will, from the cursor after, and returns the
// cursor for the next sweep. The reap comes first, so a reservation whose holder has just gone is cleaned in the same
// sweep.
func (b *Billing) sweep(ctx context.Context, after uuid.UUID) uuid.UUID {
	if n, err := b.Reap(ctx); err != nil {
		b.logger.Error("billing: reaping the holders failed", "err", err)
	} else if n > 0 {
		b.logger.Warn("billing: reaped the holders of nodes that stopped heartbeating", "holders", n)
	}
	cleaned := 0
	for range cleanupRounds {
		n, next, done, err := b.cleanFrom(ctx, after, cleanupBatch)
		cleaned += n
		after = next
		if err != nil {
			b.logger.Error("billing: the cleanup failed", "cleaned", cleaned, "err", err)
		}
		if done || ctx.Err() != nil {
			return after
		}
	}
	b.logger.Warn("billing: more reservations are due than one sweep takes over; the next sweep goes on from here", "cleaned", cleaned)
	return after
}

var queries = New()
