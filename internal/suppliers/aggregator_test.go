package suppliers

import (
	"context"
	"log/slog"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
)

// The router type of the pasted world is an aggregator (docs/domains/suppliers-direct.md「OpenRouter 的成本」) whose
// answers keyProvider gives: a name's ceiling at /v1/ceiling/<name> as a price schedule, and a generation's record at
// /v1/generation as {"cost":…,"byok":…}. OpenRouter's own answers are read in vendors' tests.

func routerType(aggregate adapter.Type) adapter.Type {
	aggregate.Kind, aggregate.Name = "router", "router"
	return aggregate
}

var routerAggregator = &direct.Aggregator{
	GenerationHeader: "X-Generation-Id",
	CeilingPath: func(name string) string {
		if strings.Contains(name, ":") {
			return ""
		}
		return "/v1/ceiling/" + name
	},
	Ceiling: func(body []byte) adapter.Ceiling {
		if string(body) == "unexpressible" {
			return adapter.Ceiling{Outcome: adapter.CeilingUnexpressible}
		}
		s, err := catalog.ParseSchedule(body)
		if err != nil {
			return adapter.Ceiling{}
		}
		return adapter.Ceiling{Outcome: adapter.CeilingRead, Schedule: s}
	},
	GenerationPath: func(id string) string { return "/v1/generation?id=" + url.QueryEscape(id) },
	Generation: func(body []byte) (adapter.Generation, error) {
		var g struct {
			Cost string `json:"cost"`
			BYOK bool   `json:"byok"`
		}
		if err := jsonx.Unmarshal(body, &g); err != nil {
			return adapter.Generation{}, err
		}
		cost, err := platform.ParseExact(g.Cost)
		return adapter.Generation{Found: true, Cost: cost, BYOK: g.BYOK}, err
	},
}

// stoneCeiling is the highest price of stone across the router's providers.
const stoneCeiling = `{"v":1,"currency":"USD","rates":{"input":"0.000006","output":"0.00003","cache_read":"0.0000006"}}`

// routerLine connects the router with a key that lists stone, at the ratio, and the jobs that keep it.
func routerLine(t *testing.T, w *pastedWorld, ratio string) (uuid.UUID, *lines) {
	t.Helper()
	w.provider.set("sk-good-router", providerKey{models: []string{"stone"}})
	line := w.connect("router", ratio, "sk-good-router")
	return line, &lines{db: w.db, scope: platformScope, admin: w.admin, cat: w.cat, logger: slog.Default()}
}

// setCeiling makes the router answer the name's ceiling so.
func (w *pastedWorld) setCeiling(name string, a answer) {
	w.provider.mu.Lock()
	defer w.provider.mu.Unlock()
	w.provider.ceilings[name] = a
}

// setGeneration makes the router answer the generation's record so; a zero answer is a record not there.
func (w *pastedWorld) setGeneration(id string, a answer) {
	w.provider.mu.Lock()
	defer w.provider.mu.Unlock()
	if a == (answer{}) {
		delete(w.provider.generations, id)
		return
	}
	w.provider.generations[id] = a
}

// routable is the snapshot's view of the endpoint.
func (w *pastedWorld) routable(endpoint uuid.UUID) Routable {
	w.t.Helper()
	if err := w.svc.Reload(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	all, err := w.svc.Routable()
	if err != nil {
		w.t.Fatal(err)
	}
	for _, r := range all {
		if r.ID == endpoint {
			return r
		}
	}
	w.t.Fatalf("endpoint %s is not routable", endpoint)
	return Routable{}
}

// TestAnAggregatorsLineIsCostedAtItsCeiling: an aggregator's endpoint is not routed until its ceiling is read; then its
// plan is the ceiling times the line's ratio, of basis ceiling, and its calls carry the max_price of the ceiling in
// credits and are reserved whole whatever is measured. The same ceiling read again is no new plan and keeps
// reconciliation's verdict; a ceiling not read for a day, a name nobody serves and one priced past the schedule leave
// it unpriced.
func TestAnAggregatorsLineIsCostedAtItsCeiling(t *testing.T) {
	t.Parallel()
	w := newPastedWorld(t)
	line, jobs := routerLine(t, w, "0.8")
	stone := w.endpoint(line, "stone")
	w.expectRouted("connected", line, "stone", false)

	w.setCeiling("stone", answer{body: stoneCeiling})
	if err := jobs.relist(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.expectRouted("the ceiling read", line, "stone", true)
	w.expectPriced("the ceiling read", line, stone, stoneCeiling, "0.8")
	if basis := w.text("SELECT s.basis FROM suppliers.endpoints e JOIN suppliers.cost_schedules s ON s.id = e.cost_schedule_id WHERE e.id = $1", stone); basis != ceilingBasis {
		t.Errorf("the plan's basis is %s", basis)
	}
	w.svc.SetMeasuredCancels(stone, true)
	r := w.routable(stone)
	if r.Cancels || string(r.Provider) != `{"max_price":{"completion":"30","prompt":"6"}}` {
		t.Errorf("routable: cancels %v, provider %s; want reserved whole within the ceiling", r.Cancels, r.Provider)
	}
	plan, plans := w.plan(stone)

	w.sql("UPDATE suppliers.endpoints SET cost_state = 'unknown', cost_state_reason = 'under_estimated' WHERE id = '" + stone.String() + "'")
	if err := jobs.relist(t.Context()); err != nil {
		t.Fatal(err)
	}
	if again, n := w.plan(stone); again != plan || n != plans {
		t.Errorf("the same ceiling read again: plan %s of %s, want %s of %s", again, n, plan, plans)
	}
	w.expectRouted("delisted, then the same ceiling", line, "stone", false)
	w.sql("UPDATE suppliers.endpoints SET cost_state = 'known', cost_state_reason = NULL WHERE id = '" + stone.String() + "'")

	for _, step := range []struct {
		name    string
		ceiling answer
		advance time.Duration
		routed  bool
	}{
		{"a server error within the day", answer{status: 503}, 23 * time.Hour, true},
		{"a server error past the day", answer{status: 503}, 2 * time.Hour, false},
		{"read again", answer{body: stoneCeiling}, 0, true},
		{"nobody serves it", answer{status: 404}, 0, false},
		{"read again", answer{body: stoneCeiling}, 0, true},
		{"priced past the schedule", answer{body: "unexpressible"}, 0, false},
	} {
		w.clock.advance(step.advance)
		w.setCeiling("stone", step.ceiling)
		if err := jobs.relist(t.Context()); err != nil {
			t.Fatal(err)
		}
		w.expectRouted(step.name, line, "stone", step.routed)
	}
}

// TestAnAttemptAtACeilingCostsWhatTheAggregatorSaid: an attempt at a ceiling plan costs the aggregator's figure times
// the line's ratio, known, an explicit 0 among them; a BYOK call costs the fee and the bound together, unknown; no
// figure costs the bound, which is never 0 for usage that ran, unknown. Each row keeps the figure, the bound in
// credits and what is left to check, and the generation id in place of a charge record's key. A figure from any
// other supplier is not taken.
func TestAnAttemptAtACeilingCostsWhatTheAggregatorSaid(t *testing.T) {
	t.Parallel()
	w := newPastedWorld(t)
	line, jobs := routerLine(t, w, "0.5")
	w.setCeiling("stone", answer{body: stoneCeiling})
	if err := jobs.relist(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.provider.set("sk-good-pasted", providerKey{models: []string{"stone"}})
	pasted := w.connect("pasted", "", "sk-good-pasted")
	// 1000 input and 100 output tokens bound at 1000 × 0.000006 + 100 × 0.00003 = 0.009 credits, booked at 0.0045.
	usage := map[catalog.Item]int64{catalog.ItemInput: 1000, catalog.ItemOutput: 100}
	for _, c := range []struct {
		name       string
		line       uuid.UUID
		generation string // the id the answer's header gave
		reported   *protocol.ReportedCost
		native     string // the cost booked, in the supplier's unit
		known      bool
		row        string // the row's generation_id, reported_cost, cost_report, bound_cost, check_state, charge_ref
	}{
		{"charged", line, "gen-a", &protocol.ReportedCost{Amount: exact(t, "0.004")}, "0.002", true, "gen-a/0.004/charged/0.009/judge/"},
		{"charged nothing", line, "gen-b", &protocol.ReportedCost{}, "0", true, "gen-b/0/charged/0.009/judge/"},
		{"byok", line, "gen-c", &protocol.ReportedCost{Amount: exact(t, "0.0004"), BYOK: true}, "0.0047", false, "gen-c/0.0004/byok_fee/0.009/judge/"},
		{"no figure", line, "gen-d", nil, "0.0045", false, "gen-d///0.009/lookup/"},
		{"no figure and no id", line, "", nil, "0.0045", false, "///0.009/done/"},
		// stone at its catalog price: 1000 × 0.000003 + 100 × 0.000015.
		{"another supplier's figure", pasted, "", &protocol.ReportedCost{Amount: exact(t, "0.004")}, "0.0045", true, "/////"},
	} {
		record := w.settle(w.endpoint(c.line, "stone"), c.generation, usage, c.reported, c.native, c.known)
		row := w.text(`SELECT concat_ws('/', coalesce(generation_id, ''), coalesce(trim_scale(reported_cost)::text, ''), coalesce(cost_report, ''),
			coalesce(trim_scale(bound_cost)::text, ''), coalesce(check_state, ''), coalesce(charge_ref, '')) || '|' || trim_scale(estimated_native)::text
			FROM suppliers.attempt_costs WHERE request_id = $1`, uuid.UUID(record.Request))
		if want := c.row + "|" + c.native; row != want {
			t.Errorf("%s: the row is %s, want %s", c.name, row, want)
		}
	}
	// The router's day: three figures, booked at 0.002 + 0 + 0.0047 against bounds of 0.0045 each, and two attempts
	// estimated at the bound they are booked at.
	got, err := inTx(w.admins, func(ctx context.Context, tx pg.Tx) (Reconciliation, error) {
		return w.admin.Reconciliation(ctx, tx, line, 1)
	})
	if err != nil || len(got.Days) != 1 {
		t.Fatalf("the router's reconciliation is %+v (%v)", got, err)
	}
	day := got.Days[0]
	estimated, _ := new(big.Rat).SetString(day.EstimatedNative)
	charged, _ := new(big.Rat).SetString(day.ChargedNative)
	if estimated.Cmp(big.NewRat(225, 10000)) != 0 || charged.Cmp(big.NewRat(67, 10000)) != 0 || day.Charges != 3 || day.Matched != 3 {
		t.Errorf("the router's day is %+v", day)
	}
}

// settle prices an attempt at the endpoint with the usage and figure, as gateway does, checks what Credits made of it,
// and records it as billing's settlement does; ref is the id the answer's header gave.
func (w *pastedWorld) settle(endpoint uuid.UUID, ref string, counts map[catalog.Item]int64, reported *protocol.ReportedCost, native string, known bool) AttemptRecord {
	w.t.Helper()
	if err := w.svc.Reload(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	a, err := w.svc.Acquire(endpoint)
	if err != nil {
		w.t.Fatal(err)
	}
	receipt := a.Done()
	receipt.ref = ref
	u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	c, _, gotKnown, err := w.svc.Credits(receipt, []catalog.Usage{u}, reported, usd)
	if want, _ := new(big.Rat).SetString(native); err != nil || c.Native.Rat().Cmp(want) != 0 || gotKnown != known {
		w.t.Errorf("costs %s, known %v (%v); want %s, %v", c.Native.Rat().FloatString(15), gotKnown, err, native, known)
	}
	record := AttemptRecord{Request: platform.RequestID(uuid.NewV7()), Seq: 1, Receipt: receipt, Usage: []catalog.Usage{u}, Cost: c, FinishedAt: w.clock.now()}
	if _, err := inTx(w.admins, func(ctx context.Context, tx pg.Tx) (struct{}, error) {
		return struct{}{}, RecordAttempts(ctx, tx, []AttemptRecord{record})
	}); err != nil {
		w.t.Fatal(err)
	}
	return record
}

// TestTheCeilingChecks: an attempt that settled with no figure has its generation's record read and booked against its
// own row, once however often the checks run; a BYOK record books its fee on top of the bound. A record not there yet is
// waited for a day, after which the bound stands. A figure past its bound by more than the tolerance, for usage all
// reported, takes the endpoint out of routing while it is on that plan, as any BYOK figure does, with an alert; one
// within the tolerance, one bounded by guessed usage and one of a plan the endpoint has left do not.
func TestTheCeilingChecks(t *testing.T) {
	t.Parallel()
	figure := func(s string, byok bool) *protocol.ReportedCost {
		return &protocol.ReportedCost{Amount: exact(t, s), BYOK: byok}
	}
	// Each attempt is of 1000 input and 100 output tokens: bound at 0.009 credits, booked at 0.0045 with no figure.
	for _, c := range []ceilingCheck{
		{name: "a record read", settled: "0.0045", record: answer{body: `{"cost":"0.008"}`}, wait: 2 * time.Minute, booked: "0.004", state: "done"},
		{name: "a record past the bound", settled: "0.0045", record: answer{body: `{"cost":"0.01"}`}, wait: 2 * time.Minute, booked: "0.005", state: "done", alert: "ceiling_exceeded"},
		{name: "a BYOK record", settled: "0.0045", record: answer{body: `{"cost":"0.0004","byok":true}`}, wait: 2 * time.Minute, booked: "0.0047", state: "done", alert: "ceiling_byok"},
		{name: "too soon for a record", settled: "0.0045", record: answer{body: `{"cost":"0.008"}`}, wait: 30 * time.Second, booked: "0.0045", state: "lookup"},
		{name: "no record yet", settled: "0.0045", wait: time.Hour, booked: "0.0045", state: "lookup"},
		{name: "no record in a day", settled: "0.0045", wait: 25 * time.Hour, booked: "0.0045", state: "done"},
		{name: "a server error within the day", settled: "0.0045", record: answer{status: 503}, wait: 2 * time.Minute, booked: "0.0045", state: "lookup"},
		{name: "a server error past the day", settled: "0.0045", record: answer{status: 503}, wait: 25 * time.Hour, booked: "0.0045", state: "done"},
		// The attempt is booked at the ratio it ran at, 0.5, whatever the line's is by the lookup.
		{name: "a record read after the ratio changed", settled: "0.0045", record: answer{body: `{"cost":"0.008"}`}, wait: 2 * time.Minute, ratio: "1", booked: "0.004", state: "done"},
		{name: "a key erased since", settled: "0.0045", record: answer{body: `{"cost":"0.008"}`}, wait: 2 * time.Minute, erased: true, booked: "0.0045", state: "done"},
		{name: "a key sealed under a version this node lacks", settled: "0.0045", record: answer{body: `{"cost":"0.008"}`}, wait: 2 * time.Minute, unsealable: true, booked: "0.0045", state: "lookup"},
		{name: "a figure within the tolerance", reported: figure("0.00909", false), settled: "0.004545", booked: "0.004545", state: "done"},
		{name: "a figure past the tolerance", reported: figure("0.0091", false), settled: "0.00455", booked: "0.00455", state: "done", alert: "ceiling_exceeded"},
		{name: "past the bound of guessed usage", reported: figure("0.02", false), settled: "0.01", guessed: true, booked: "0.01", state: "done"},
		{name: "past the bound of a plan left", reported: figure("0.02", false), settled: "0.01", repriced: true, booked: "0.01", state: "done"},
		{name: "a BYOK figure", reported: figure("0.0004", true), settled: "0.0047", booked: "0.0047", state: "done", alert: "ceiling_byok"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(t)
		})
	}
}

// ceilingCheck is one case of TestTheCeilingChecks.
type ceilingCheck struct {
	name     string
	reported *protocol.ReportedCost
	settled  string        // the cost booked at the settlement
	record   answer        // the generation's record, a zero answer for none
	wait     time.Duration // from the attempt's end to the checks
	guessed  bool          // the usage was estimated, not reported
	repriced bool          // the endpoint is on another plan by the checks
	ratio    string        // the line's ratio by the checks, "" for the one the attempt ran at
	erased   bool          // the attempt's key was erased by the checks
	// unsealable: the key is sealed under a key version this node does not hold, which a restart may bring
	unsealable bool
	booked     string // the cost booked after the checks, run twice
	state      string
	alert      string // the alert's key, "" for none: the endpoint is still routed
}

// arrange makes what the case says changed between the settlement and the checks.
func (c ceilingCheck) arrange(w *pastedWorld, jobs *lines, line uuid.UUID) {
	w.t.Helper()
	if c.guessed {
		w.sql("UPDATE suppliers.attempt_costs SET cost_flag = 'unpriced'")
	}
	if c.repriced {
		w.relistAt(jobs, strings.Replace(stoneCeiling, "0.00003", "0.00004", 1))
	}
	if c.ratio != "" {
		if err := w.setRatio(line, c.ratio); err != nil {
			w.t.Fatal(err)
		}
	}
	if c.unsealable {
		w.sql("UPDATE suppliers.credentials SET key_version = key_version + 1000")
	}
	if c.erased {
		w.sql("UPDATE suppliers.credentials SET status = 'erased', ciphertext = NULL, nonce = NULL, wrapped_dek = NULL, key_version = NULL")
	}
}

func (c ceilingCheck) run(t *testing.T) {
	w := newPastedWorld(t)
	line, jobs := routerLine(t, w, "0.5")
	w.relistAt(jobs, stoneCeiling)
	usage := map[catalog.Item]int64{catalog.ItemInput: 1000, catalog.ItemOutput: 100}
	known := c.reported != nil && !c.reported.BYOK
	record := w.settle(w.endpoint(line, "stone"), "gen-1", usage, c.reported, c.settled, known)
	w.setGeneration("gen-1", c.record)
	c.arrange(w, jobs, line)
	w.clock.advance(c.wait)
	for range 2 {
		if err := jobs.checkCeilings(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	row := w.text("SELECT trim_scale(coalesce(actual_native, estimated_native))::text || '/' || check_state FROM suppliers.attempt_costs WHERE request_id = $1", uuid.UUID(record.Request))
	if want := c.booked + "/" + c.state; row != want {
		t.Errorf("the attempt is %s, want %s", row, want)
	}
	if !c.erased && !c.unsealable { // a line without a key it can open is in no snapshot
		w.expectRouted(c.name, line, "stone", c.alert == "")
	}
	w.expectAlert(c.alert)
}

// relistAt makes the router answer stone's ceiling so and runs the hourly re-list.
func (w *pastedWorld) relistAt(jobs *lines, ceiling string) {
	w.t.Helper()
	w.setCeiling("stone", answer{body: ceiling})
	if err := jobs.relist(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
}

// expectAlert checks that the checks raised one alert of the key, or none for "".
func (w *pastedWorld) expectAlert(key string) {
	w.t.Helper()
	got := w.text("SELECT coalesce(string_agg(body ->> 'key', ','), '') FROM platform.alert_signals WHERE body ->> 'kind' = 'suppliers.reconciliation_difference'")
	if got != key {
		w.t.Errorf("alerts %q, want %q", got, key)
	}
}

// TestACeilingPricesTheStandardTierOnly: OpenRouter lists no price per service tier, so an aggregator's plan prices
// every item the model reports at the standard tier and no other tier, even of a model that declares flex and
// priority, and its endpoint is served at the standard tier only; a ceiling that lacks an item the model reports leaves
// the endpoint unpriced rather than putting a plan that cannot price the model among its costs.
func TestACeilingPricesTheStandardTierOnly(t *testing.T) {
	t.Parallel()
	const geminiSpec = `{"items":["input","output","cache_read"],"tiers":["standard","flex","priority"],"context_length":1000000,` +
		`"max_output_tokens":8192,"streams":true,"abilities":["tools"]}`
	const geminiPrice = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000004","cache_read":"0.0000001"},` +
		`"service_tiers":{"flex":{"rates":{"input":"0.0000005","output":"0.000002","cache_read":"0.00000005"}},` +
		`"priority":{"rates":{"input":"0.0000018","output":"0.0000072","cache_read":"0.00000018"}}}}`
	for _, c := range []struct {
		name, ceiling string
		routed        bool
	}{
		{"every item at the ceiling", `{"v":1,"currency":"USD","rates":{"input":"0.000002","output":"0.00001","cache_read":"0.0000002"}}`, true},
		{"an item the ceiling lacks", `{"v":1,"currency":"USD","rates":{"input":"0.000002","output":"0.00001"}}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newPastedWorld(t)
			w.listSpec("gemini", geminiSpec, geminiPrice)
			w.provider.set("sk-good-router", providerKey{models: []string{"gemini"}})
			line := w.connect("router", "", "sk-good-router")
			jobs := &lines{db: w.db, scope: platformScope, admin: w.admin, cat: w.cat, logger: slog.Default()}
			w.setCeiling("gemini", answer{body: c.ceiling})
			if err := jobs.relist(t.Context()); err != nil {
				t.Fatal(err)
			}
			w.expectRouted(c.name, line, "gemini", c.routed)
			if c.routed {
				w.expectStandardPlan(w.endpoint(line, "gemini"), w.models["gemini"])
				if r := w.routable(w.endpoint(line, "gemini")); !r.StandardOnly {
					t.Error("the endpoint is not served at the standard tier only")
				}
			}
		})
	}
}

// TestACeilingReadIsKeptWhenTheRoundIsCut: each name's ceiling is stored once it is read, so a round the job's deadline
// cuts short, here at a name whose answer never comes, keeps what it read before.
func TestACeilingReadIsKeptWhenTheRoundIsCut(t *testing.T) {
	t.Parallel()
	w := newPastedWorld(t)
	w.list("gravel", stonePrice)
	w.provider.set("sk-good-router", providerKey{models: []string{"gravel", "stone"}})
	line := w.connect("router", "", "sk-good-router")
	jobs := &lines{db: w.db, scope: platformScope, admin: w.admin, cat: w.cat, logger: slog.Default()}
	w.setCeiling("stone", answer{body: stoneCeiling})
	w.setCeiling("gravel", answer{hang: true})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := jobs.relist(ctx); err == nil {
		t.Error("a round the deadline cut short reported no error")
	}
	if got := w.text("SELECT count(*)::text FROM suppliers.supplier_models WHERE supplier_id = $1 AND upstream_name = 'stone' AND ceiling IS NOT NULL", line); got != "1" {
		t.Errorf("stone's ceiling read before the cut was not kept")
	}
}

// expectStandardPlan checks that the endpoint's plan prices every item the model reports at the standard tier, and
// prices no other tier.
func (w *pastedWorld) expectStandardPlan(endpoint uuid.UUID, model catalog.ModelID) {
	w.t.Helper()
	plan, err := catalog.ParseSchedule([]byte(w.text(`SELECT s.schedule::text FROM suppliers.endpoints e
		JOIN suppliers.cost_schedules s ON s.id = e.cost_schedule_id WHERE e.id = $1`, endpoint)))
	if err != nil {
		w.t.Fatal(err)
	}
	m, _ := w.snap().Model(model)
	reports := m.Reportable()
	if missing := plan.Missing(catalog.Reportable{Items: reports.Items}); missing != (catalog.Reportable{}) {
		w.t.Errorf("the plan leaves %+v of what the model reports unpriced", missing)
	}
	if others := reports.ServiceTiers &^ (1 << catalog.ServiceTierStandard); plan.Missing(reports).ServiceTiers != others {
		w.t.Errorf("the plan prices a tier of %b besides standard", others)
	}
}
