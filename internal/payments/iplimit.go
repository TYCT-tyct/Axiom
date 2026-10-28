package payments

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
	"uuid"

	"axiom/internal/platform/clientip"
	"axiom/internal/platform/pg"
)

// ipWindow is how long the per-IP counters of a site count (docs/domains/payments.md「防欺诈」).
const ipWindow = time.Hour

var (
	// ErrTooManyIPOrders is an IP group that placed as many orders as the site allows in the window.
	ErrTooManyIPOrders = errors.New("payments: too many top-ups were started from this network lately; try again later")
	// ErrTooManyIPFailures is an IP group whose orders failed as often as the site allows in the window.
	ErrTooManyIPFailures = errors.New("payments: too many top-ups from this network failed lately; try again later")
)

// Wait is a refusal that waiting lifts: the cause, and how long until the request would be let through, or an upper
// bound of it.
type Wait struct {
	Err   error
	After time.Duration
}

func (w *Wait) Error() string { return w.Err.Error() }
func (w *Wait) Unwrap() error { return w.Err }

// spendIPOrder spends one of the order places of ip's group on the site, in the order's own transaction: the counter row
// is the serialization point of a limit that spans accounts, which this scope cannot read from orders
// (README.md「决策与理由」). It refuses when the group's orders or failures are at their limit, and a counter that
// cannot be taken refuses too (the error is returned and the order is not placed): an abuse limit must not open when
// its counting breaks.
func spendIPOrder(ctx context.Context, tx pg.Tx, site uuid.UUID, ip netip.Addr, s settings) error {
	group := clientip.Group(ip)
	window := int64(ipWindow / time.Second)
	taken, err := queries.TakeIPOrder(ctx, tx, TakeIPOrderParams{
		SiteID: site, Ip: group, WindowSeconds: window, OrderLimit: s.ipOrders, FailureLimit: s.ipFailures,
	})
	if err != nil {
		return fmt.Errorf("payments: count the IP group's orders: %w", err)
	}
	if len(taken) > 0 {
		return nil
	}
	used, err := queries.IPUsage(ctx, tx, IPUsageParams{SiteID: site, Ip: group, WindowSeconds: window})
	if err != nil {
		return fmt.Errorf("payments: read the IP group's counters: %w", err)
	}
	cause := ErrTooManyIPFailures
	if used.Orders >= s.ipOrders {
		cause = ErrTooManyIPOrders
	}
	return &Wait{cause, time.Duration(used.WaitSeconds) * time.Second}
}

// countIPFailure counts a failed order against ip's group. It is taken before the order's move to failed, which takes
// the merchant's usage row: every transaction that holds both locks takes the IP row first.
func countIPFailure(ctx context.Context, tx pg.Tx, site uuid.UUID, ip netip.Addr) error {
	err := queries.AddIPFailure(ctx, tx, AddIPFailureParams{SiteID: site, Ip: clientip.Group(ip), WindowSeconds: int64(ipWindow / time.Second)})
	if err != nil {
		return fmt.Errorf("payments: count the IP group's failure: %w", err)
	}
	return nil
}
