package billing

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sync/atomic"
	"time"
	"uuid"

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
	ReapGrace time.Duration // how long past its expiry a holder is reaped
	Lifetime  time.Duration // a reservation's deadline after admission: the longest call plus the settlement grace
	Segments  Segments
	// CorrectionWindow is how long an estimated or cleaned-up charge may be corrected, holding the rest of its
	// reservation until then (an hour, docs/domains/billing.md「用量来源与暂估」).
	CorrectionWindow time.Duration
	WaiverCap        int64 // what an account's waived calls may cost the platform in a UTC day, in billionths
}

// Billing writes reservations under one holder, this start of the process.
type Billing struct {
	db        *pg.DB
	platform  pg.Scope // for the holder rows, which only the platform scope writes
	holder    uuid.UUID
	config    Config
	suppliers Suppliers
	beat      atomic.Int64 // unix nanoseconds of the last time the holder's expiry was known to be pushed out
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

// Start registers a new holder. platform is the platform scope, which only cmd/ builds.
func Start(ctx context.Context, db *pg.DB, platform pg.Scope, config Config, s Suppliers) (*Billing, error) {
	b := &Billing{db: db, platform: platform, holder: uuid.NewV7(), config: config, suppliers: s}
	registered := time.Now()
	err := db.Tx(ctx, platform, func(tx pg.Tx) error {
		return queries.InsertHolder(ctx, tx, InsertHolderParams{ID: b.holder, TtlSeconds: config.HolderTTL.Seconds()})
	})
	if err != nil {
		return nil, fmt.Errorf("billing: register the holder: %w", err)
	}
	b.beat.Store(registered.UnixNano())
	return b, nil
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
	if _, err := queries.FenceHolder(ctx, tx, b.holder); errors.Is(err, pg.ErrNoRows) {
		return ErrTerminated
	} else if err != nil {
		return fmt.Errorf("billing: fence the holder: %w", err)
	}
	return nil
}

var queries = New()
