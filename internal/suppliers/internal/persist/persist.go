// Package persist keeps the sync engine's state in the database and lets one node at a time write it
// (internal/suppliers/README.md「同步」「持久化」). A node takes a supplier's lease and gets a Session; the Session is
// the engine's Store and the adapter's LoginStore, and every write either of them makes names the lease's fence, so a
// node whose lease was taken over writes nothing, however late it is.
package persist

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// ErrLeaseHeld is Acquire's error when another node holds the supplier's lease. It is not a failure: that node is
// syncing the supplier.
var ErrLeaseHeld = errors.New("persist: another node holds the supplier's lease")

// Config is what a node needs to take leases.
type Config struct {
	DB *pg.DB
	// Scope is the platform scope, which only cmd/ may build: syncing sees every supplier.
	Scope pg.Scope
	Keys  *secret.Keyring
	// Holder names this node in the lease, for whoever looks at the table; the fence, not the name, decides who writes.
	Holder string
	// LeaseTTL is how long a lease lasts without a commit: every commit lengthens it by as much, and a node that stops
	// committing loses it after this long. Now is the clock; a lease runs out by the clock of the node that looks.
	LeaseTTL time.Duration
	Now      func() time.Time
}

// Node takes leases for one process.
type Node struct{ cfg Config }

// New checks the configuration, so a node that could not hold a lease does not start.
func New(cfg Config) (*Node, error) {
	switch {
	case cfg.DB == nil || cfg.Keys == nil || cfg.Now == nil:
		return nil, errors.New("persist: a database, a keyring and a clock are required")
	case cfg.Holder == "":
		return nil, errors.New("persist: the node needs a name")
	case cfg.LeaseTTL <= 0:
		return nil, errors.New("persist: the lease needs a positive duration")
	}
	return &Node{cfg}, nil
}

// Supplier is what a round needs to know of the supplier it holds, as it stood when the lease was taken.
type Supplier struct {
	ID           uuid.UUID
	Kind         adapter.Kind
	Origin, Exit string
	ConfigGen    int64
}

// Session is a held lease. It is used by one round at a time.
type Session struct {
	node     *Node
	Supplier Supplier
	fence    int64
}

// Acquire takes the lease of a supplier's sync state, making the state on the first take. It returns ErrLeaseHeld while
// another node holds the lease and its time has not run out.
func (n *Node) Acquire(ctx context.Context, supplier uuid.UUID) (*Session, error) {
	s := &Session{node: n}
	err := n.cfg.DB.Tx(ctx, n.cfg.Scope, func(tx pg.Tx) error {
		now := n.cfg.Now()
		until := now.Add(n.cfg.LeaseTTL)
		q := store.New()
		fence, err := q.AcquireSyncLease(ctx, tx, store.AcquireSyncLeaseParams{
			SupplierID: supplier, Now: now, Holder: &n.cfg.Holder, LeaseUntil: &until,
		})
		if errors.Is(err, pg.ErrNoRows) {
			return ErrLeaseHeld
		} else if err != nil {
			return err
		}
		row, err := q.GetSupplierForSync(ctx, tx, supplier)
		if err != nil {
			return fmt.Errorf("read the supplier: %w", err)
		}
		s.fence = fence
		s.Supplier = Supplier{ID: row.ID, Kind: adapter.Kind(row.Kind), Origin: row.Origin, Exit: row.MgmtEgressID, ConfigGen: row.ConfigGen}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("take the lease of supplier %s: %w", supplier, err)
	}
	return s, nil
}

// Release gives the lease back, so that the next round need not wait for it to run out. A lease that was taken over
// meanwhile is not the caller's to give back, and stays as it is.
func (s *Session) Release(ctx context.Context) error {
	return s.node.cfg.DB.Tx(ctx, s.node.cfg.Scope, func(tx pg.Tx) error {
		return store.New().ReleaseSyncLease(ctx, tx, store.ReleaseSyncLeaseParams{SupplierID: s.Supplier.ID, LeaseFence: s.fence})
	})
}

// Store is the engine's view of the session.
func (s *Session) Store() syncer.Store { return stateStore{s} }

// Logins is the adapter's view of the session.
func (s *Session) Logins() adapter.LoginStore { return loginStore{s} }
