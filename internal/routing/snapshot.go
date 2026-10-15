package routing

import (
	"errors"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
)

// FormatSet is a set of call formats.
type FormatSet uint8

// Of returns the set holding the formats.
func Of(formats ...protocol.Format) (s FormatSet) {
	for _, f := range formats {
		s |= 1 << f
	}
	return s
}

// Has reports whether f is in the set.
func (s FormatSet) Has(f protocol.Format) bool { return s&(1<<f) != 0 }

// Class is where an endpoint's supply comes from, which presets filter on.
type Class uint8

// The classes of supply.
const (
	ClassOfficial Class = iota // the vendor's own API
	ClassCloud                 // a cloud platform's hosting of the model
	ClassRelay                 // a relay or reseller
	numClasses
)

// ClassSet is a set of classes. The zero value allows none; presets list what they allow.
type ClassSet uint8

// AllClasses allows every class.
const AllClasses = ClassSet(1<<numClasses - 1)

// Has reports whether c is in the set.
func (s ClassSet) Has(c Class) bool { return s&(1<<c) != 0 }

// Policy is what an endpoint's supplier does with the data it is sent; a preset lists the policies it accepts.
type Policy uint8

// The data policies.
const (
	PolicyNoRetention Policy = iota // not stored, not trained on
	PolicyRetained                  // stored, not trained on
	PolicyMayTrain                  // may be trained on
	numPolicies
)

// PolicySet is a set of data policies.
type PolicySet uint8

// AllPolicies accepts every policy.
const AllPolicies = PolicySet(1<<numPolicies - 1)

// Has reports whether p is in the set.
func (s PolicySet) Has(p Policy) bool { return s&(1<<p) != 0 }

// Owner says whose an endpoint is: the platform's, a site's or one account's own. An account sees the platform's, its
// site's and its own.
type Owner struct {
	Site    SiteID
	Account AccountID
}

func (o Owner) visibleTo(a Admission) bool {
	return (o.Site == SiteID{} && o.Account == AccountID{}) || o.Site == a.Site && o.Account == AccountID{} || o.Account == a.Account
}

// Endpoint is one supplier group serving one model, as the snapshot holds it: what routing filters and ranks by.
// suppliers, quality and billing supply the values when the snapshot is built.
type Endpoint struct {
	ID       EndpointID
	Supplier SupplierID
	Model    catalog.ModelID
	Owner    Owner

	Enabled     bool // switched on
	BreakerOpen bool // quality has taken it out for now
	BalanceOK   bool // the supplier can pay for a request
	BalanceLow  bool // it can, but is below the warning line: its weight is halved
	Egresses    int  // how many egresses can reach it

	Class         Class
	Policy        Policy
	Verified      bool          // it passed quality's checks
	Quality       float64       // 0 to 1
	Confidence    float64       // 0 to 1, how much the quality score rests on
	Health        float64       // 0 to 1, the recent success rate
	ToolErrorRate float64       // 0 to 1, measured on real traffic
	Throughput    float64       // output tokens per second, measured
	Latency       time.Duration // time to first token, measured

	Native           FormatSet // formats it speaks itself
	Served           FormatSet // formats it can serve, with protocol's conversion; includes Native
	Caps             protocol.Capabilities
	Context          int64 // the most tokens a request may take, input plus output
	MaxOutput        int64 // the model's output limit here, also its default
	ImageTokens      int64 // the most input tokens one image can take
	PageTokens       int64 // the most input tokens one document page can take
	Streams          bool
	Cancels          bool      // measured: it stops generating when the client disconnects
	ReasoningVisible FormatSet // the formats in which reasoning progress shows in the stream
	MaxConcurrent    int64     // 0 for no limit at the endpoint itself

	Price PriceRef // the sale price version; a zero Version means none
}

// Override is what a request's provider parameter may change: filters only tighten a preset's, the price limits only
// lower it, the order and the sort replace it, and the performance wishes only reorder.
type Override struct {
	Only, Exclude []SupplierID
	Order         []SupplierID
	NoFallbacks   bool // allow_fallbacks false: only the suppliers in Order are tried
	Sort          *Strategy
	Classes       ClassSet  // when not zero, only these classes
	Policies      PolicySet // when not zero, only these policies
	MaxPrice      PriceLimit
	MinThroughput float64
	MaxLatency    time.Duration
}

// PriceLimit caps the sale price per billing item; an item not in Set has no limit. It compares the standard tier's
// base rate, which is what a typical request pays.
type PriceLimit struct {
	Set catalog.ItemSet
	Max [catalog.NumItems]platform.UnitPrice
}

// within reports whether the schedule's base rates are within the limit. An item the schedule does not price cannot
// be compared and passes: billing prices what has no price at its fallback.
func (p PriceLimit) within(s catalog.Schedule) bool {
	for it := range catalog.NumItems {
		if !p.Set.Has(it) {
			continue
		}
		if rate, ok := s.BaseRate(it); ok && rate.Cmp(p.Max[it]) > 0 {
			return false
		}
	}
	return true
}

// Strategy is how candidates are ranked once the order and the session have had their say.
type Strategy uint8

// The strategies (docs/domains/routing.md「排序的先后」).
const (
	Balanced Strategy = iota // price-weighted random, the default
	Cheapest
	Fastest
	BestQuality
)

// Preset is the configuration a request is routed under. Its contents come from the snapshot; admission names which
// one applies. The zero value of a limit means none; the zero Classes and Policies allow nothing, so a preset says
// AllClasses and AllPolicies to allow all.
type Preset struct {
	ID            PresetID
	Strategy      Strategy
	Classes       ClassSet
	Policies      PolicySet
	MinQuality    float64
	MinConfidence float64
	VerifiedOnly  bool
	MaxPrice      PriceLimit
	Order         []SupplierID
	NoFallbacks   bool
	StickyTTL     time.Duration // 0 uses the router's default
	MaxAttempts   int           // 0 uses the router's default
	AttemptWindow time.Duration // 0 uses the router's default
}

// Snapshot is the routing view of the world, built whole in the background and replaced whole: endpoints grouped by
// model, and the presets. It never changes after it is built.
type Snapshot struct {
	built   time.Time
	byModel map[catalog.ModelID][]*candidate
	presets map[PresetID]Preset
}

// candidate is an endpoint with what is worked out once at build: its price as a number for weighting, and its
// live state, which outlives the snapshot.
type candidate struct {
	Endpoint
	blend        float64 // input plus output base rate, as a number; only for weighting
	cacheCheaper bool    // reading the cache costs less than input, which is when sticking pays
	state        *epState
}

// NewSnapshot groups the endpoints by model and checks them: an endpoint twice, or a preset twice, is a mistake in
// the data that would otherwise surface as a wrong choice.
func (r *Router) NewSnapshot(built time.Time, endpoints []Endpoint, presets []Preset) (*Snapshot, error) {
	s := &Snapshot{built: built, byModel: map[catalog.ModelID][]*candidate{}, presets: make(map[PresetID]Preset, len(presets))}
	seen := make(map[EndpointID]struct{}, len(endpoints))
	for _, e := range endpoints {
		if _, dup := seen[e.ID]; dup {
			return nil, fmt.Errorf("routing snapshot: endpoint %s twice", uuid.UUID(e.ID))
		}
		seen[e.ID] = struct{}{}
		c, err := r.candidate(e)
		if err != nil {
			return nil, err
		}
		s.byModel[e.Model] = append(s.byModel[e.Model], c)
	}
	for _, p := range presets {
		if _, dup := s.presets[p.ID]; dup {
			return nil, fmt.Errorf("routing snapshot: preset %s twice", uuid.UUID(p.ID))
		}
		if p.Classes == 0 || p.Policies == 0 {
			return nil, fmt.Errorf("routing snapshot: preset %s allows no class or no data policy, which would filter out everything", uuid.UUID(p.ID))
		}
		s.presets[p.ID] = p
	}
	return s, nil
}

func (r *Router) candidate(e Endpoint) (*candidate, error) {
	if e.Served&e.Native != e.Native {
		return nil, fmt.Errorf("routing snapshot: endpoint %s serves less than it speaks", uuid.UUID(e.ID))
	}
	c := &candidate{Endpoint: e, state: r.state.of(e.ID)}
	if e.Price.Version == (uuid.UUID{}) {
		return c, nil
	}
	in, hasIn := e.Price.Schedule.BaseRate(catalog.ItemInput)
	out, hasOut := e.Price.Schedule.BaseRate(catalog.ItemOutput)
	if !hasIn || !hasOut {
		return nil, errors.New("routing snapshot: an endpoint's price schedule lacks input or output")
	}
	c.blend = number(in) + number(out)
	read, hasRead := e.Price.Schedule.BaseRate(catalog.ItemCacheRead)
	c.cacheCheaper = hasRead && read.Cmp(in) < 0
	return c, nil
}

// number is a unit price as a float, for weighting only; money is never computed with it. It allocates, so it runs
// at build, not per request.
func number(p platform.UnitPrice) float64 {
	f, _ := p.Mul(1).Rat().Float64()
	return f
}
