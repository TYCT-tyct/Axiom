package routing

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/suppliers"
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

// Platform reports whether the endpoint has no tenant owner.
func (o Owner) Platform() bool { return o.Site == (SiteID{}) && o.Account == (AccountID{}) }

func (o Owner) visibleTo(a Admission) bool {
	return o.Platform() || o.Site == a.Site && o.Account == AccountID{} || o.Account == a.Account
}

// Capacity reports whether an endpoint's credentials can take another call now: any key with a free concurrency slot
// and room in its supplier account's rolling minute. Plan reads it before freezing a route, so a request is not sent to
// an endpoint that would refuse it; the slot itself is still taken atomically when the attempt begins. suppliers'
// Capacity is the production one; nil means the capacity is not known, which is not full.
type Capacity interface{ Full() bool }

// Endpoint is one supplier group serving one model, as the snapshot holds it: what routing filters and ranks by.
// suppliers, quality and billing supply the values when the snapshot is built.
type Endpoint struct {
	Exchange  platform.ExchangeRate
	ID        EndpointID
	Supplier  SupplierID
	Codename  string // the supplier group's public codename; never its real name
	Model     catalog.ModelID
	ModelSlug string // the model's catalog slug, frozen with the snapshot so a response names what the route chose
	Free      bool   // the model is a :free catalog entry, the only kind openrouter/free may choose
	Owner     Owner

	LiveHealth  *quality.State
	Capacity    Capacity
	Enabled     bool // switched on
	BreakerOpen bool // quality has taken it out for now
	BalanceOK   bool // the supplier has a synced positive balance
	BalanceLow  bool // it can, but is below the warning line: its weight is halved
	PoolLow     bool // its group's pool has little capacity left: its weight is halved
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
	// The same two measures over a rolling five-minute window, p50/p75/p90/p99, 0 when not measured. They are copied
	// from quality when the snapshot is built so the routing path reads no lock.
	LatencyPercentiles    [4]time.Duration
	ThroughputPercentiles [4]float64
	Quantization          suppliers.Quantization // the level an admin recorded for this endpoint; empty is unrecorded

	ServiceTiers     catalog.ServiceTierSet // the service tiers it offers; standard is one of them unless it offers only others
	Native           FormatSet              // formats it speaks itself
	Served           FormatSet              // formats it can serve, with protocol's conversion; includes Native
	Caps             protocol.Capabilities
	Parameters       protocol.ParameterSet  // the request parameters its spec declares it takes; provider.require_parameters reads it
	Context          int64                  // the most tokens a request may take, input plus output
	MaxOutput        int64                  // the model's output limit here, also its default
	OutputSpeed      int64                  // the fastest it outputs, in tokens per second; 0 when not known, which rules out segmenting
	Reports          catalog.ItemSet        // the billing items its upstream may report (catalog.Reportable)
	ImageTokens      int64                  // the most input tokens one image can take
	PageTokens       int64                  // the most input tokens one document page can take
	FilePages        int64                  // the most pages one file can have; 0 when no limit is known
	ToolUses         map[catalog.Item]int64 // each server tool's default most uses; the snapshot keeps a copy
	Streams          bool
	Cancels          bool      // measured: it stops generating when the client disconnects
	ReasoningVisible FormatSet // the formats in which reasoning progress shows in the stream, of a model that reasons
	MaxConcurrent    int64     // 0 for no limit at the endpoint itself

	Funds          *suppliers.Funds // synced native balance and live supplier-wide refusal
	Cost           catalog.Schedule // in the same native units as Funds
	AudiencePrices *billing.AudiencePrices
	Price          PriceRef // the sale price version; a zero Version means none

	// UnknownItem: billing met a usage field on the model it cannot price (billing.UnknownItems), so the model takes no
	// new requests until an admin prices it (docs/domains/billing.md「价目表缺项」).
	UnknownItem bool

	// Target is how the endpoint is called: its name for the model and what the model's spec says of how it is asked.
	// Its Format and MaxOutput are left out: Plan chooses the format a request is called in (callFormat), and the cap a
	// request that sets none is sent with, which is the one it is quoted at (Entry.MaxOutput).
	Target protocol.Target
}

// Override is what a request's provider parameter may change: filters only tighten a preset's, the price limits only
// lower it, the order and the sort replace it, and the performance wishes only reorder.
type Override struct {
	OnlyNames, ExcludeNames, OrderNames []string
	SortGlobal                          bool // sort.partition none: rank across models
	NoFallbacks                         bool // with an order, restrict to it; without one, retain one supplier per model
	Sort                                *Strategy
	// VerifiedOnly keeps only endpoints quality verified as the model they claim; a request's :exacto sets it, and
	// only an explicit provider.sort replaces it (docs/domains/quality.md「真伪检测」).
	VerifiedOnly  bool
	Classes       ClassSet  // when not zero, only these classes
	Policies      PolicySet // when not zero, only these policies
	MaxPrice      PriceLimit
	MinThroughput float64
	MaxLatency    time.Duration
	// The percentile forms of the same two preferences: a number applies to the moving average, while each cutoff here
	// applies to that percentile of the endpoint's rolling five-minute window (OpenRouter《Provider Routing》
	// 「Performance Thresholds」). Cutoffs of 0 are thresholds, so presence is a bit.
	MinThroughputPercentiles Cutoffs
	MaxLatencyPercentiles    Cutoffs
	// Quantizations keeps only endpoints whose admin-recorded level is one of these. An endpoint with no level recorded
	// never matches: a missing record is not fp16 and not the level named 'unknown'.
	Quantizations []suppliers.Quantization
	// RequireParameters is provider.require_parameters: every parameter in RequiredParameters must be declared by the
	// endpoint. The gateway fills both only when the member is true.
	RequireParameters  bool
	RequiredParameters protocol.ParameterSet
}

// The percentiles a Cutoffs may set, in the order their values are stored.
const (
	P50 = iota
	P75
	P90
	P99
)

// Cutoffs are percentile thresholds in the measure's own unit (tokens a second, or seconds), p50, p75, p90 and p99 in
// that order. Set has a bit per percentile the request named.
type Cutoffs struct {
	Values [4]float64
	Set    uint8
}

// Has reports whether the request set the cutoff of percentile i.
func (c Cutoffs) Has(i int) bool { return c.Set&(1<<i) != 0 }

// PriceLimit caps the sale price per billing item; an item not in Set has no limit. It compares the standard tier's
// base rate, which is what a typical request pays.
type PriceLimit struct {
	Set catalog.ItemSet
	Max [catalog.NumItems]platform.UnitPrice
}

// tokenItems are the billing items counted in tokens; a missing one is charged at the highest token price. A request,
// a call of another unit, has its own highest price and is never compared with a token price.
const tokenItems = catalog.TokenItems

// allTiers are every service tier.
const allTiers = catalog.ServiceTierSet(1<<catalog.ServiceTierStandard | 1<<catalog.ServiceTierFlex | 1<<catalog.ServiceTierPriority |
	1<<catalog.ServiceTierFast)

// within reports whether what a request may be charged per item is within the limit: the highest price over the service
// tiers it may be served at and the context tiers a prompt of at most length tokens can fall in, so a priority or
// long-context price above the limit counts. A tier without a price does not hide the ones that have: the known prices
// are held to the limit, and a token item missing a price is held at the highest token price of the schedule, which is
// what billing charges it (docs/domains/billing.md「价目表缺项」). A call item missing a price has the site's default price,
// which routing does not know, so it is not compared.
func (p PriceLimit) within(s catalog.Schedule, tiers catalog.ServiceTierSet, length int64) bool {
	for it := range catalog.NumItems {
		if !p.Set.Has(it) {
			continue
		}
		var one catalog.ItemSet
		one.Add(it)
		highest, missing := s.Highest(one, tiers, length)
		if highest.Cmp(p.Max[it]) > 0 {
			return false
		}
		if missing {
			switch {
			case tokenItems.Has(it):
				if fallback, _ := s.Highest(tokenItems, allTiers, math.MaxInt64); fallback.Cmp(p.Max[it]) > 0 {
					return false
				}
			case it == catalog.ItemRequest:
				// A request missing at every eligible tier is charged at the highest request price the schedule has,
				// matching settlement and the quote. Other call items keep the site default, which routing cannot know.
				if fallback, _ := s.Highest(one, allTiers, math.MaxInt64); fallback.Cmp(p.Max[it]) > 0 {
					return false
				}
			}
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
	LowestLatency
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
	models  []catalog.ModelID // in id order, for openrouter/auto
	free    []catalog.ModelID // the free ones, in id order, for openrouter/free
	presets map[PresetID]Preset
}

// candidate is an endpoint with what is worked out once at build: its price as a number for weighting, and its
// live state, which outlives the snapshot.
type candidate struct {
	Endpoint
	model         billing.Model
	price         candidatePrice
	prices        []candidatePrice
	state         *epState
	supplierPause *cooldown
}

func (c *candidate) priceFor(a billing.Audience) *candidatePrice {
	if c.AudiencePrices == nil {
		return &c.price
	}
	at := c.AudiencePrices.Select(a)
	if at < 0 {
		return nil
	}
	return &c.prices[at]
}

type candidatePrice struct {
	PriceRef
	blend        float64
	cacheCheaper bool
}

func priceOf(p PriceRef, direct bool) (candidatePrice, error) {
	price := candidatePrice{PriceRef: p}
	if p.none() {
		return price, nil
	}
	in, hasIn := p.Schedule.BaseRate(catalog.ItemInput)
	out, hasOut := p.Schedule.BaseRate(catalog.ItemOutput)
	if !hasIn || (!direct && !hasOut) {
		return price, errors.New("routing snapshot: an endpoint's price schedule lacks input or output")
	}
	price.blend = number(in)
	if hasOut {
		price.blend += number(out)
	}
	read, hasRead := p.Schedule.BaseRate(catalog.ItemCacheRead)
	price.cacheCheaper = hasRead && read.Cmp(in) < 0
	return price, nil
}

// NewSnapshot groups the endpoints by model and checks them: an endpoint twice, or a preset twice, is a mistake in
// the data that would otherwise surface as a wrong choice.
func (r *Router) NewSnapshot(built time.Time, endpoints []Endpoint, presets []Preset) (*Snapshot, error) {
	s := &Snapshot{built: built, byModel: map[catalog.ModelID][]*candidate{}, presets: make(map[PresetID]Preset, len(presets))}
	seen := make(map[EndpointID]struct{}, len(endpoints))
	prices := map[*billing.AudiencePrices][]candidatePrice{}
	for _, e := range endpoints {
		if _, dup := seen[e.ID]; dup {
			return nil, fmt.Errorf("routing snapshot: endpoint %s twice", uuid.UUID(e.ID))
		}
		seen[e.ID] = struct{}{}
		c, err := r.snapshotCandidate(e, prices)
		if err != nil {
			return nil, err
		}

		s.byModel[e.Model] = append(s.byModel[e.Model], c)
	}
	s.models, s.free = modelsOf(endpoints)
	for _, p := range presets {
		if _, dup := s.presets[p.ID]; dup {
			return nil, fmt.Errorf("routing snapshot: preset %s twice", uuid.UUID(p.ID))
		}
		if p.NoFallbacks && len(p.Order) == 0 {
			return nil, fmt.Errorf("routing snapshot: preset %s closes the fallbacks but names no suppliers", uuid.UUID(p.ID))
		}
		p.Order = slices.Clone(p.Order) // the caller keeps its slice; the snapshot never changes
		if p.Classes == 0 || p.Policies == 0 {
			return nil, fmt.Errorf("routing snapshot: preset %s allows no class or no data policy, which would filter out everything", uuid.UUID(p.ID))
		}
		s.presets[p.ID] = p
	}
	return s, nil
}

// modelsOf lists the models in id order, and the free ones among them in the same order: a model is free when its
// endpoint serves the catalog's :free entry, and those are the only ones openrouter/free may choose.
func modelsOf(endpoints []Endpoint) (models, free []catalog.ModelID) {
	isFree := make(map[catalog.ModelID]bool, len(endpoints))
	for _, e := range endpoints {
		isFree[e.Model] = isFree[e.Model] || e.Free
	}
	for m, f := range isFree {
		models = append(models, m)
		if f {
			free = append(free, m)
		}
	}
	slices.SortFunc(models, compareModelID)
	slices.SortFunc(free, compareModelID)
	return models, free
}

// compareModelID orders models as bytes, the order of the database's uuids.
func compareModelID(a, b catalog.ModelID) int { return uuid.UUID(a).Compare(uuid.UUID(b)) }

func (r *Router) snapshotCandidate(e Endpoint, prices map[*billing.AudiencePrices][]candidatePrice) (*candidate, error) {
	c, err := r.candidate(e)
	if err != nil {
		return nil, err
	}
	if e.AudiencePrices != nil {
		c.prices, err = compileAudiencePrices(e.AudiencePrices, prices, directOnly(e.Native))
		if err != nil {
			return nil, err
		}
	}
	return c, nil
}

func compileAudiencePrices(table *billing.AudiencePrices, cache map[*billing.AudiencePrices][]candidatePrice, direct bool) ([]candidatePrice, error) {
	if !direct {
		if prices, ok := cache[table]; ok {
			return prices, nil
		}
	}
	compiled := make([]candidatePrice, len(table.Layers()))
	for i, layer := range table.Layers() {
		price, err := priceOf(PriceRef{Version: layer.Sold.Version, Schedule: layer.Sold.Schedule}, direct)
		if err != nil {
			return nil, err
		}
		compiled[i] = price
	}
	if !direct {
		cache[table] = compiled
	}
	return compiled, nil
}

func (r *Router) candidate(e Endpoint) (*candidate, error) {
	if e.Served&e.Native != e.Native {
		return nil, fmt.Errorf("routing snapshot: endpoint %s serves less than it speaks", uuid.UUID(e.ID))
	}
	if e.Context <= 0 {
		return nil, fmt.Errorf("routing snapshot: endpoint %s has no context length", uuid.UUID(e.ID))
	}
	e.ToolUses = maps.Clone(e.ToolUses)
	if e.ServiceTiers == 0 {
		return nil, fmt.Errorf("routing snapshot: endpoint %s offers no service tier", uuid.UUID(e.ID))
	}
	c := &candidate{Endpoint: e, state: r.state.of(e.ID), supplierPause: r.state.supplier(e.Supplier), model: billing.Model{
		ID: e.Model, ContextLength: e.Context, ImageTokens: e.ImageTokens, PageTokens: e.PageTokens, FilePages: e.FilePages,
		OutputSpeed: e.OutputSpeed, MaxUses: e.ToolUses, Reports: e.Reports, ServiceTiers: e.ServiceTiers,
	}}
	var err error
	c.price, err = priceOf(e.Price, directOnly(e.Native))
	return c, err
}

// directOnly reports whether every format is a direct one, which needs no output price.
func directOnly(fs FormatSet) bool {
	return fs != 0 && fs&^Of(protocol.Embeddings, protocol.Rerank) == 0
}

// number is a unit price as a float, for weighting only; money is never computed with it. It allocates, so it runs
// at build, not per request.
func number(p platform.UnitPrice) float64 {
	f, _ := p.Mul(1).Rat().Float64()
	return f
}
