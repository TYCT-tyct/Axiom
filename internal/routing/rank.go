package routing

import (
	"cmp"
	"math"
	"slices"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/protocol"
)

// ranked is a candidate that passed the filters, with what it is ordered by. The order is, in turn: the order the
// request or preset named, the performance wishes the request set (an endpoint short of them goes behind), the
// session's endpoint, then the strategy's score (docs/domains/routing.md「排序的先后」).
type ranked struct {
	price *candidatePrice
	*candidate
	opened    time.Time
	slowFull  bool
	order     int  // index of its supplier in the named order; after all of them when not named
	slow      bool // misses a performance wish
	notSticky bool
	weight    float64 // Balanced, and Fastest's unmeasured: the chance of being first is proportional to it
	key       float64 // a random key whose order draws without replacement by weight
	quality   float64
	byok      bool // the account's own key: it goes before the platform's endpoints at the same order
	known     bool // BestQuality: the endpoint has counted evidence, so its score is not unknown
	explore   bool // Fastest: the unmeasured endpoint this request puts first, so that it gets measured
}

// stickyMinHealth is the health below which a session no longer sticks to an endpoint.
const stickyMinHealth = 0.5

// exploreShare is the share of the requests ranked fastest first that put an unmeasured endpoint first
// (docs/domains/routing.md「排序的先后」).
const exploreShare = 0.05

// rank filters the endpoints of one model and orders those that pass. Every endpoint left out is recorded, with the
// reason, except those the account cannot see.
func (r *Router) rank(cands []*candidate, n *need, rec *Record) []ranked {
	out := make([]ranked, 0, len(cands))
	strategy := n.strategy()
	// A key that refuses shared capacity takes this model's platform endpoints out before anything else looks at them.
	// It is the account's own visible endpoint, so only the requests of that account are affected.
	noShared := slices.ContainsFunc(cands, func(c *candidate) bool {
		return c.Byok && !c.ByokFallback && c.Owner.visibleTo(n.in.Admission)
	})
	for _, c := range cands {
		price := c.priceFor(billing.Audience{Account: accounts.AccountID(n.in.Admission.Account), Tier: n.in.Admission.Tier})
		if price == nil {
			if c.Owner.visibleTo(n.in.Admission) {
				rec.Excluded = append(rec.Excluded, Excluded{c.ID, NoPrice})
			}
			continue
		}
		reason, visible := check(c, n, price, noShared)
		switch {
		case !visible:
		case reason != 0:
			rec.Excluded = append(rec.Excluded, Excluded{c.ID, reason})
		default:
			s := r.score(c, n, strategy, price)
			if c.LiveHealth != nil {
				var limit int64
				s.opened, limit = c.LiveHealth.Routing(n.now)
				s.slowFull = limit > 0 && c.state.inflight.Load() >= limit
			}
			out = append(out, s)
		}
	}
	if !n.rankAll() {
		r.sort(out, strategy)
	}
	return out
}

func (r *Router) sort(order []ranked, strategy Strategy) {
	if strategy == Fastest {
		r.explore(order)
	}
	slices.SortFunc(order, func(a, b ranked) int { return compare(&a, &b, strategy) })
}

func (n *need) order(c *candidate) int {
	if names := n.over.OrderNames; len(names) > 0 {
		if i := slices.Index(names, c.Codename); i >= 0 {
			return i
		}
		return len(names)
	}
	order := n.preset.Order
	if i := slices.Index(order, c.Supplier); i >= 0 {
		return i
	}
	return len(order)
}

func (n *need) strategy() Strategy {
	if n.over.Sort != nil {
		return *n.over.Sort
	}
	return n.preset.Strategy
}

// explore marks, in exploreShare of the requests, the unmeasured candidate with the highest key to go first: the one
// Balanced would pick among them by price. An endpoint is unmeasured while its throughput is 0.
func (r *Router) explore(out []ranked) {
	best := -1
	for i := range out {
		if out[i].Throughput == 0 && (best < 0 || out[i].key > out[best].key) {
			best = i
		}
	}
	if best >= 0 && r.cfg.Rand() < exploreShare {
		out[best].explore = true
	}
}

// score works out what c is ordered by. Balanced draws its random key here, once, so the order is a sample without
// replacement: the first is chosen with probability proportional to its weight, the second the same among the rest.
func (r *Router) score(c *candidate, n *need, strategy Strategy, price *candidatePrice) ranked {
	s := ranked{candidate: c, price: price, order: n.order(c), notSticky: !r.sticks(c, n, price), byok: c.Byok}
	s.slow = slow(c, n.over)
	s.known = c.Confidence > 0
	tools := 1.0
	if n.req.Needs.Has(protocol.Tools) && (strategy == Balanced || strategy == BestQuality) {
		tools = 1 - c.ToolErrorRate // rank by the tool-call error measured on real traffic
	}
	s.quality = c.Quality * c.Confidence * tools
	if strategy == Balanced || strategy == Fastest && c.Throughput == 0 ||
		strategy == BestQuality && !s.known || strategy == LowestLatency && c.Latency == 0 {
		s.weight = r.weight(c, n.now, price) * tools * tools
		u := 1 - r.cfg.Rand() // in (0, 1]
		s.key = math.Log(u) / math.Max(s.weight, math.SmallestNonzeroFloat64)
	}
	return s
}

// slow reports whether c misses a performance wish of the request. An unmeasured endpoint (0) misses neither: it is not
// known to be slow, and it is to be measured. A number is compared with the moving average; a percentile cutoff with the
// same percentile of the endpoint's rolling window, and every cutoff the request named has to hold.
func slow(c *candidate, o *Override) bool {
	return (o.MinThroughput > 0 && c.Throughput > 0 && c.Throughput < o.MinThroughput) ||
		(o.MaxLatency > 0 && c.Latency > o.MaxLatency) ||
		below(c.ThroughputPercentiles, o.MinThroughputPercentiles) ||
		above(c.LatencyPercentiles, o.MaxLatencyPercentiles)
}

// below reports whether a measured throughput percentile is under a cutoff the request set; an unmeasured one (0) is not.
func below(measured [4]float64, cut Cutoffs) bool {
	if cut.Set == 0 {
		return false
	}
	for i, v := range measured {
		if cut.Has(i) && v > 0 && v < cut.Values[i] {
			return true
		}
	}
	return false
}

// above is below for a latency cutoff, which is in seconds while the measure is a duration.
func above(measured [4]time.Duration, cut Cutoffs) bool {
	if cut.Set == 0 {
		return false
	}
	for i, v := range measured {
		if cut.Has(i) && v > 0 && v.Seconds() > cut.Values[i] {
			return true
		}
	}
	return false
}

// weight is the chance a candidate is given: cheaper is more likely, scaled by the quality score—which already includes
// the endpoint's health—halved when its supplier's balance is low or its group's pool nearly used up, and cut for a
// while after a 429.
func (r *Router) weight(c *candidate, now time.Time, price *candidatePrice) float64 {
	w := math.Max(c.Quality, 0.01) * c.state.damping(now, r.cfg.Recovery, r.cfg.ThrottleFloor) / math.Max(price.blend, 1e-18)
	if c.BalanceLow || c.PoolLow {
		w /= 2
	}
	return w
}

// sticks reports whether the session's endpoint is this one and sticking still pays: only when reading the cache costs
// less than input, and the endpoint is healthy, not throttled and not full.
func (r *Router) sticks(c *candidate, n *need, price *candidatePrice) bool {
	return n.in.Sticky == c.ID && n.in.Sticky != (EndpointID{}) && price.cacheCheaper && c.Health >= stickyMinHealth &&
		c.state.damping(n.now, r.cfg.Recovery, r.cfg.ThrottleFloor) == 1 && !c.full()
}

// compare orders two candidates; ties end on the endpoint id so the order is the same for the same inputs.
func compare(a, b *ranked, strategy Strategy) int {
	if c := cmp.Compare(a.order, b.order); c != 0 {
		return c
	}
	if c := compareBool(!a.byok, !b.byok); c != 0 {
		return c
	}
	if c := compareBool(a.slow, b.slow); c != 0 {
		return c
	}
	if c := compareBool(a.notSticky, b.notSticky); c != 0 {
		return c
	}
	if c := compareBy(a, b, strategy); c != 0 {
		return c
	}
	return slices.Compare(a.ID[:], b.ID[:])
}

// compareBy is the strategy's own order, once the order, the performance wishes and stickiness agree.
func compareBy(a, b *ranked, strategy Strategy) int {
	switch strategy {
	case Balanced:
		return cmp.Compare(b.key, a.key)
	case Cheapest:
		return cmp.Compare(a.price.blend, b.price.blend)
	case Fastest:
		return compareFastest(a, b)
	case LowestLatency:
		if a.Latency == 0 || b.Latency == 0 {
			if a.Latency != b.Latency {
				return compareBool(a.Latency == 0, b.Latency == 0)
			}
			return cmp.Compare(b.key, a.key)
		}
		return cmp.Compare(a.Latency, b.Latency)
	case BestQuality:
		if c := compareBool(!a.known, !b.known); c != 0 {
			return c
		}
		if !a.known {
			return cmp.Compare(b.key, a.key) // unknown: balanced's price-weighted draw, never the endpoint's ID
		}
		return cmp.Compare(b.quality, a.quality)
	}
	return 0
}

// compareFastest puts the explored candidate first, then the measured ones by throughput and time to first token, then
// the unmeasured by their price-weighted keys: an unmeasured endpoint is neither the slowest, which would never get
// measured, nor the fastest, which would send traffic to the unknown, and never ordered by its ID.
func compareFastest(a, b *ranked) int {
	if c := compareBool(!a.explore, !b.explore); c != 0 {
		return c
	}
	if c := compareBool(a.Throughput == 0, b.Throughput == 0); c != 0 {
		return c
	}
	if a.Throughput == 0 {
		return cmp.Compare(b.key, a.key)
	}
	if c := cmp.Compare(b.Throughput, a.Throughput); c != 0 {
		return c
	}
	return cmp.Compare(a.Latency, b.Latency)
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
