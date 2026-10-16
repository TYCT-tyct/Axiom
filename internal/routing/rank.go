package routing

import (
	"cmp"
	"math"
	"slices"
	"time"

	"axiom/internal/protocol"
)

// ranked is a candidate that passed the filters, with what it is ordered by. The order is, in turn: the order the
// request or preset named, the performance wishes the request set (an endpoint short of them goes behind), the
// session's endpoint, then the strategy's score (docs/domains/routing.md「排序的先后」).
type ranked struct {
	*candidate
	order     int  // index of its supplier in the named order; after all of them when not named
	slow      bool // misses a performance wish
	notSticky bool
	weight    float64 // Balanced: the chance of being first is proportional to it
	key       float64 // Balanced: a random key whose order draws without replacement by weight
	quality   float64
}

// stickyMinHealth is the health below which a session no longer sticks to an endpoint.
const stickyMinHealth = 0.5

// rank filters the endpoints of one model and orders those that pass. Every endpoint left out is recorded, with the
// reason, except those the account cannot see.
func (r *Router) rank(cands []*candidate, n *need, rec *Record) []ranked {
	out := make([]ranked, 0, len(cands))
	order := n.over.Order
	if len(order) == 0 {
		order = n.preset.Order
	}
	strategy := n.preset.Strategy
	if n.over.Sort != nil {
		strategy = *n.over.Sort
	}
	for _, c := range cands {
		reason, visible := check(c, n)
		switch {
		case !visible:
		case reason != 0:
			rec.Excluded = append(rec.Excluded, Excluded{c.ID, reason})
		default:
			out = append(out, r.score(c, n, order, strategy))
		}
	}
	slices.SortFunc(out, func(a, b ranked) int { return compare(&a, &b, strategy) })
	return out
}

// score works out what c is ordered by. Balanced draws its random key here, once, so the order is a sample without
// replacement: the first is chosen with probability proportional to its weight, the second the same among the rest.
func (r *Router) score(c *candidate, n *need, order []SupplierID, strategy Strategy) ranked {
	s := ranked{candidate: c, order: len(order), notSticky: !r.sticks(c, n)}
	for i, id := range order {
		if id == c.Supplier {
			s.order = i
			break
		}
	}
	s.slow = (n.over.MinThroughput > 0 && c.Throughput < n.over.MinThroughput) ||
		(n.over.MaxLatency > 0 && c.Latency > n.over.MaxLatency)
	tools := 1.0
	if n.req.Needs.Has(protocol.Tools) && strategy != Cheapest && strategy != Fastest {
		tools = 1 - c.ToolErrorRate // rank by the tool-call error measured on real traffic
	}
	s.quality = c.Quality * c.Confidence * tools
	if strategy == Balanced {
		s.weight = r.weight(c, n.now) * tools * tools
		u := 1 - r.cfg.Rand() // in (0, 1]
		s.key = math.Log(u) / math.Max(s.weight, math.SmallestNonzeroFloat64)
	}
	return s
}

// weight is the chance a candidate is given: cheaper is more likely, scaled by how healthy and good it is, halved when
// its supplier's balance is low, and cut for a while after a 429.
func (r *Router) weight(c *candidate, now time.Time) float64 {
	w := c.Health * math.Max(c.Quality, 0.01) * c.state.damping(now, r.cfg.Recovery, r.cfg.ThrottleFloor) / math.Max(c.blend, 1e-18)
	if c.BalanceLow {
		w /= 2
	}
	return w
}

// sticks reports whether the session's endpoint is this one and sticking still pays: only when reading the cache costs
// less than input, and the endpoint is healthy, not throttled and not full.
func (r *Router) sticks(c *candidate, n *need) bool {
	return n.in.Sticky == c.ID && n.in.Sticky != (EndpointID{}) && c.cacheCheaper && c.Health >= stickyMinHealth &&
		c.state.damping(n.now, r.cfg.Recovery, r.cfg.ThrottleFloor) == 1 && !c.full()
}

// compare orders two candidates; ties end on the endpoint id so the order is the same for the same inputs.
func compare(a, b *ranked, strategy Strategy) int {
	if c := cmp.Compare(a.order, b.order); c != 0 {
		return c
	}
	if c := compareBool(a.slow, b.slow); c != 0 {
		return c
	}
	if c := compareBool(a.notSticky, b.notSticky); c != 0 {
		return c
	}
	var c int
	switch strategy {
	case Balanced:
		c = cmp.Compare(b.key, a.key)
	case Cheapest:
		c = cmp.Compare(a.blend, b.blend)
	case Fastest:
		if c = cmp.Compare(b.Throughput, a.Throughput); c == 0 {
			c = cmp.Compare(a.Latency, b.Latency)
		}
	case BestQuality:
		c = cmp.Compare(b.quality, a.quality)
	}
	if c != 0 {
		return c
	}
	return slices.Compare(a.ID[:], b.ID[:])
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}
