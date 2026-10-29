package routing_test

import (
	"math/rand/v2"
	"strconv"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

var (
	t0    = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	model = catalog.ModelID(uuid.MustParse("00000000-0000-7000-8000-0000000000aa"))
	other = catalog.ModelID(uuid.MustParse("00000000-0000-7000-8000-0000000000bb"))
	acct  = routing.AccountID(uuid.MustParse("00000000-0000-7000-8000-000000000a01"))
	site  = routing.SiteID(uuid.MustParse("00000000-0000-7000-8000-000000000b01"))
	pre   = routing.PresetID(uuid.MustParse("00000000-0000-7000-8000-000000000c01"))
)

// id makes the n-th identifier of a kind, stable across a run.
func id(n int) [16]byte {
	var b [16]byte
	b[0], b[14], b[15] = 0x10, byte(n>>8), byte(n) //nolint:gosec // n is a test number below 2¹⁶
	return b
}

// standardAndPriority is what the helper endpoints offer.
const standardAndPriority = catalog.ServiceTierSet(1<<catalog.ServiceTierStandard | 1<<catalog.ServiceTierPriority)

func ep(n int) routing.EndpointID  { return routing.EndpointID(id(n)) }
func sup(n int) routing.SupplierID { return routing.SupplierID(id(n)) }

// schedule is a price schedule where input costs in and output costs out dollars per token, and reading the cache costs
// cacheRead; an empty cacheRead leaves the cache without a price.
func schedule(tb testing.TB, in, out, cacheRead string) catalog.Schedule {
	tb.Helper()
	rates := `"input":"` + in + `","output":"` + out + `"`
	if cacheRead != "" {
		rates += `,"cache_read":"` + cacheRead + `"`
	}
	s, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{` + rates + `}}`))
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

// endpoint is a healthy endpoint of the model that serves every format and capability, at 1e-6 input and 2e-6 output
// with a cheap cache; each test changes what it is about. Its supplier is its own.
func endpoint(tb testing.TB, n int, mods ...func(*routing.Endpoint)) routing.Endpoint {
	tb.Helper()
	all := routing.Of(protocol.Messages, protocol.Responses, protocol.Chat)
	e := routing.Endpoint{
		ID: ep(n), Supplier: sup(n), Codename: "g" + strconv.Itoa(n), Model: model, Enabled: true, BalanceOK: true, Egresses: 1,
		Class: routing.ClassOfficial, Policy: routing.PolicyNoRetention, Verified: true, Quality: 1, Confidence: 1, Health: 1,
		Throughput: 100, Latency: 500 * time.Millisecond, ServiceTiers: standardAndPriority, Native: all, Served: all, Caps: ^protocol.Capabilities(0),
		Context: 1_000_000, MaxOutput: 8192, OutputSpeed: 100, Reports: 1 << catalog.ItemCacheRead, Streams: true, Cancels: true,
		ReasoningVisible: all,
		Price:            routing.PriceRef{Version: uuid.UUID(id(100 + n)), Schedule: schedule(tb, "0.000001", "0.000002", "0.0000001")},
	}
	for _, m := range mods {
		if m != nil {
			m(&e)
		}
	}
	return e
}

func mustRouter(tb testing.TB) *routing.Router {
	tb.Helper()
	r, err := routing.New(routing.Config{})
	if err != nil {
		tb.Fatal(err)
	}
	return r
}

func openPreset() routing.Preset {
	return routing.Preset{ID: pre, Classes: routing.AllClasses, Policies: routing.AllPolicies}
}

// harness is a router with a fixed clock and a seeded random source, holding a snapshot built from the endpoints.
type harness struct {
	t   testing.TB
	r   *routing.Router
	now time.Time
}

func newHarness(tb testing.TB, cfg routing.Config, preset routing.Preset, endpoints ...routing.Endpoint) *harness {
	tb.Helper()
	h := &harness{t: tb, now: t0.Add(-time.Nanosecond)} // the first snapshot is built at t0
	cfg.Now = func() time.Time { return h.now }
	if cfg.Rand == nil {
		cfg.Rand = rand.New(rand.NewPCG(1, 2)).Float64 //nolint:gosec // a seeded generator keeps the tests repeatable
	}
	r, err := routing.New(cfg)
	if err != nil {
		tb.Fatal(err)
	}
	h.r = r
	h.replace(preset, endpoints...)
	return h
}

// replace builds and installs a snapshot, built a moment after the last: a snapshot that is not newer is ignored.
func (h *harness) replace(preset routing.Preset, endpoints ...routing.Endpoint) {
	h.t.Helper()
	h.now = h.now.Add(time.Nanosecond)
	snap, err := h.r.NewSnapshot(h.now, endpoints, []routing.Preset{preset})
	if err != nil {
		h.t.Fatal(err)
	}
	h.r.Replace(snap)
}

// input is a plain request for the model by the account.
func input() routing.Input {
	return routing.Input{
		Admission: routing.Admission{Account: acct, Site: site, Preset: pre},
		Req: &protocol.Request{
			Format: protocol.Messages, Model: "m", Choices: 1, TextBytes: 4000,
			ServiceTiers: 1 << catalog.ServiceTierStandard,
		},
		Models: []catalog.ModelID{model},
	}
}

// plan plans, failing the test on a Wait or an error.
func (h *harness) plan(in routing.Input) routing.Plan {
	h.t.Helper()
	p, wait, err := h.r.Plan(in)
	if wait != nil || err != nil {
		h.t.Fatalf("plan: wait %v, error %v", wait, err)
	}
	return p
}

// first is the endpoint the plan tries first.
func first(p routing.Plan) routing.EndpointID { return p.Entries[0].Endpoint }
