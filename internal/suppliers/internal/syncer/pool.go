package syncer

import (
	"context"
	"slices"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// poolLowCapacity is where a group's effective capacity counts as low: a fifth of its accounts' full windows. At that
// point the group's weight is halved and an alert is raised; below a tenth the routes are nearly empty anyway, so
// warning earlier is what gives a person time to move traffic or top the pool up.
const poolLowCapacity = 20

// poolReadTimeout bounds the pool read within a round: it is several requests at a supplier that may be slow, and a
// round holds the supplier's lease. A read that does not finish is the task's failure like any other.
const poolReadTimeout = 2 * time.Minute

// PoolCapacity is what the management API of a pool last said of one group: its accounts, how many of them the pool
// marks failed, how many it may schedule, the mean room left in their tighter quota window, and the effective capacity
// that leaves after the accounts that cannot serve. Headroom and Capacity are nil when no account reported a window.
type PoolCapacity struct {
	GroupRef         string
	Accounts, Failed int
	Usable           int
	Headroom         *int
	Capacity         *int
	Low              bool
}

// poolCapacity derives a group's effective capacity from what the pool reported: the room left in its windows, times
// the share of its accounts that can serve. A group of accounts that all report no window has no capacity, and is not
// called low on a guess.
func poolCapacity(g adapter.GroupCapacity) PoolCapacity {
	p := PoolCapacity{GroupRef: g.GroupRef, Accounts: g.Accounts, Failed: g.Failed, Usable: g.Usable, Headroom: g.Headroom}
	if g.Headroom != nil && g.Accounts > 0 {
		capacity := *g.Headroom * g.Usable / g.Accounts
		p.Capacity = &capacity
	}
	p.Low = poolIsLow(p)
	return p
}

// poolIsLow is where capacity stops being enough to route at full weight: the windows are nearly used up, or half the
// accounts have failed. An account pool of one that failed is all of them.
func poolIsLow(p PoolCapacity) bool {
	if p.Accounts == 0 {
		return false
	}
	return p.Failed*2 >= p.Accounts || (p.Capacity != nil && *p.Capacity <= poolLowCapacity)
}

// pool reads the pool's capacity into this step's state, where the projection compares it with the stored rows. A
// supplier that is not a pool keeps what it had: the supplier may be a relay whose login never administered anything, or
// a pool whose role was taken away, and clearing observed data on that answer would un-warn a problem nobody fixed.
func (e *Engine) pool(ctx context.Context, c *Commit) error {
	p, ok := e.mgr.(adapter.Pool)
	if !ok {
		return adapter.ErrNotPool
	}
	ctx, cancel := context.WithTimeout(ctx, poolReadTimeout)
	defer cancel()
	reported, err := p.Pool(ctx)
	if err != nil {
		return err
	}
	listed := make(map[string]bool, len(c.State.Groups))
	for _, g := range c.State.Groups {
		listed[g.Ref] = true
	}
	caps := make([]PoolCapacity, 0, len(reported))
	for _, g := range reported {
		if listed[g.GroupRef] && g.Accounts > 0 {
			caps = append(caps, poolCapacity(g))
		}
	}
	slices.SortFunc(caps, func(a, b PoolCapacity) int { return cmpStr(a.GroupRef, b.GroupRef) })
	c.State.Pool = caps
	return nil
}
