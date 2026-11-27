package suppliers

import (
	"context"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// officialPrices is a catalog with one model's official price in US dollars.
type officialPrices map[catalog.ModelID]catalog.Schedule

func (o officialPrices) ReferencePrice(id catalog.ModelID, currency string) (catalog.ReferencePrice, bool) {
	s, ok := o[id]
	return catalog.ReferencePrice{Model: id, Currency: currency, Schedule: s}, ok && currency == "USD"
}

// memState is the sync's store over a state read from the database: it keeps what a round commits.
type memState struct {
	state syncer.State
	plans []syncer.Plan
}

func (m *memState) Load(context.Context) (syncer.State, error) { return m.state, nil }
func (m *memState) Commit(_ context.Context, _ int64, c syncer.Commit) error {
	m.state, m.plans = c.State, append(m.plans, c.Plans...)
	return nil
}

// unpriced is a relay that lists one group, of a ratio, with one model and no price.
type unpriced struct {
	group, ratio, model string
}

func (unpriced) Account(context.Context) (adapter.Account, error) { return adapter.Account{}, nil }
func (u unpriced) Offer(context.Context, adapter.Listing) (adapter.Page[adapter.Group], error) {
	return adapter.Page[adapter.Group]{Done: true, Items: []adapter.Group{{
		Ref: u.group, Name: u.group, Ratio: adapter.Amount(u.ratio), Facts: adapter.GroupFacts(`{}`),
		Models: []adapter.Model{{UpstreamName: u.model}},
	}}}, nil
}

func (unpriced) Keys(context.Context, adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	return adapter.Page[adapter.KeyState]{Done: true}, nil
}

func (unpriced) KeySecret(context.Context, adapter.KeyRef) (secret.Secret, error) {
	return secret.Secret{}, nil
}

func (unpriced) Charges(context.Context, adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	return adapter.ChargePage{}, nil, nil
}

// TestConfirmedBaseIsWhatACallIsCharged: the invariant of a confirmed base. A person confirms the model's official
// price for a new-api endpoint (quota, 500,000 to the dollar); the sync's next catalog round makes the plan of it at the
// group's ratio; and an attempt on the endpoint is charged its usage × base × ratio × unit value, rounded up to a
// billionth once. The product is written out here apart from the code under test.
func TestConfirmedBaseIsWhatACallIsCharged(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1) // quota, a unit value of 2, the endpoint "gpt" of group "g"
	model := w.endpointModel()
	official := officialPrices{model: mustParse(t, `{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000015"}}`)}
	var confirmed []BasePrice
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		confirmed, err = w.adminOf().ConfirmBasePrices(ctx, tx, audit.User(uuid.NewV7()), w.supplier, []BasePriceItem{{Endpoint: w.endpoint}}, official, "the relay hides its prices")
		return err
	})
	same(t, "the base in quota", `{"currency":"XXX","rates":{"input":"1.5","output":"7.5"},"v":1}`, scheduleText(confirmed[0].Base))

	// The next round of the sync, over the state the confirmation left, reads the group at a ratio of 1.37.
	st := &memState{state: w.syncState()}
	if due := st.state.Tasks[syncer.TaskCatalog].Due; due.After(w.clock.now()) {
		t.Fatalf("the catalog task is due at %v, not now", due)
	}
	if err := syncer.New(st, unpriced{group: "g", ratio: "1.37", model: "gpt"}, syncer.Defaults(), w.clock.now).Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(st.plans) != 1 || st.plans[0].Endpoint != "g/gpt" || st.plans[0].Basis != "confirmed" {
		t.Fatalf("the plans %+v, want one confirmed plan of g/gpt", st.plans)
	}
	w.priceWith(st.plans[0])

	// (1000 × 1.5 × 1.37 + 777 × 7.5 × 1.37) quota × 2 USD = (2055 + 7983.675) × 2 = 20077.35 USD, in billionths.
	if got := w.charged(map[catalog.Item]int64{catalog.ItemInput: 1000, catalog.ItemOutput: 777}); got != 20_077_350_000_000 {
		t.Errorf("charged %d billionths, want 20077350000000", got)
	}
	cost := w.endpointCost()
	for _, l := range cost.Lines {
		if len(l.Factors) != 2 || l.Factors[0].Kind != "base_price" || l.Factors[1] != (CostFactor{Kind: "group_ratio", Value: "1.37"}) {
			t.Errorf("%s: factors %v, want the base price then the group ratio", l.Item, l.Factors)
		}
	}
}

// TestBasePricesAreConfirmedAndCleared: what a confirmation stores and refuses, and that a clear takes it away. Each
// change raises the configuration generation, so a round already in flight writes nothing, and is audited.
func TestBasePricesAreConfirmedAndCleared(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	model := w.endpointModel()
	usd := mustParse(t, `{"v":1,"currency":"USD","rates":{"input":"0.000002"}}`)
	official := officialPrices{model: usd}
	confirm := func(items ...BasePriceItem) error {
		return w.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
			_, err := w.adminOf().ConfirmBasePrices(t.Context(), tx, audit.User(uuid.NewV7()), w.supplier, items, official, "r")
			return err
		})
	}
	entered := mustParse(t, `{"v":1,"currency":"USD","rates":{"input":"0.000004","output":"0.00001"}}`)
	cny := mustParse(t, `{"v":1,"currency":"CNY","rates":{"input":"0.00002"}}`)
	free := mustParse(t, `{"v":1,"currency":"USD","rates":{"input":"0"}}`)
	for _, c := range []struct {
		name  string
		items []BasePriceItem
		want  error
		base  string // the stored base after the change; "" for none
	}{
		{"the official price", []BasePriceItem{{Endpoint: w.endpoint}}, nil, `{"currency":"XXX","rates":{"input":"1"},"v":1}`},
		{"an entered price replaces it", []BasePriceItem{{Endpoint: w.endpoint, Price: &entered}}, nil, `{"currency":"XXX","rates":{"input":"2","output":"5"},"v":1}`},
		{"a price not in dollars", []BasePriceItem{{Endpoint: w.endpoint, Price: &cny}}, ErrBasePrice, `{"currency":"XXX","rates":{"input":"2","output":"5"},"v":1}`},
		{"a price of nothing", []BasePriceItem{{Endpoint: w.endpoint, Price: &free}}, ErrBasePrice, `{"currency":"XXX","rates":{"input":"2","output":"5"},"v":1}`},
		{"an endpoint of another supplier", []BasePriceItem{{Endpoint: uuid.NewV7()}}, ErrNoSupplierEndpoint, `{"currency":"XXX","rates":{"input":"2","output":"5"},"v":1}`},
	} {
		gen := w.configGen()
		refuses(t, c.name, confirm(c.items...), c.want)
		same(t, c.name+": the stored base", c.base, w.syncState().Confirmed["g/gpt"])
		same(t, c.name+": the generation raised", c.want == nil, w.configGen() > gen)
	}
	delete(official, model)
	refuses(t, "a model without an official price", confirm(BasePriceItem{Endpoint: w.endpoint}), ErrNoOfficialPrice)

	clear := func() (out []BasePrice) {
		w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
			out, err = w.adminOf().ClearBasePrices(ctx, tx, audit.User(uuid.NewV7()), w.supplier, []uuid.UUID{w.endpoint, w.endpoint}, "r")
			return err
		})
		return out
	}
	if out := clear(); len(out) != 1 || out[0].Endpoint != w.endpoint {
		t.Errorf("the clear answered %+v, want the endpoint's base once", out)
	}
	_, stored := w.syncState().Confirmed["g/gpt"]
	same(t, "a base after the clear", false, stored)
	same(t, "a second clear", 0, len(clear()))
	same(t, "the changes audited", "2 1", w.query(`SELECT
		(SELECT count(*) FROM platform.audit_log WHERE action = 'supplier.base_price_confirm')::text || ' ' ||
		(SELECT count(*) FROM platform.audit_log WHERE action = 'supplier.base_price_clear')::text`))
}

// charged is what an attempt on the world's endpoint of these counts is charged, in billionths of its currency.
func (w *world) charged(counts map[catalog.Item]int64) int64 {
	w.t.Helper()
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		w.t.Fatal(err)
	}
	receipt := a.Done()
	u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	_, got, known, err := w.svc.Credits(receipt, []catalog.Usage{u}, receipt.currency)
	if err != nil || !known {
		w.t.Fatalf("Credits: %v, known %t", err, known)
	}
	return got
}

// mustParse is a schedule from its JSON.
func mustParse(t *testing.T, text string) catalog.Schedule {
	t.Helper()
	s, err := catalog.ParseSchedule([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// query is the one text the query answers.
func (w *world) query(q string, args ...any) (v string) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error { return tx.QueryRow(ctx, q, args...).Scan(&v) })
	return v
}

// endpointModel is the model of the world's endpoint.
func (w *world) endpointModel() (id catalog.ModelID) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return tx.QueryRow(ctx, `SELECT model_id FROM suppliers.endpoints WHERE id = $1`, w.endpoint).Scan((*uuid.UUID)(&id))
	})
	return id
}

// configGen is the supplier's configuration generation.
func (w *world) configGen() (gen int64) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return tx.QueryRow(ctx, `SELECT config_gen FROM suppliers.suppliers WHERE id = $1`, w.supplier).Scan(&gen)
	})
	return gen
}

// syncState is the supplier's stored sync state.
func (w *world) syncState() (st syncer.State) {
	w.t.Helper()
	raw := w.query(`SELECT state::text FROM suppliers.sync_states WHERE supplier_id = $1`, w.supplier)
	if err := jsonx.UnmarshalKnown([]byte(raw), &st); err != nil {
		w.t.Fatal(err)
	}
	// Only the catalog task runs in these tests: the others are not due.
	for _, task := range []syncer.Task{syncer.TaskBalance, syncer.TaskKeys, syncer.TaskCharges} {
		st.Tasks[task].Due = w.clock.now().Add(time.Hour)
	}
	return st
}

// priceWith puts the endpoint on a plan the sync made, as the projection stores it, and reloads.
func (w *world) priceWith(p syncer.Plan) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		plan, err := store.New().EnsureCostSchedule(ctx, tx, store.EnsureCostScheduleParams{
			SupplierID: w.supplier, EndpointID: w.endpoint, NativeUnit: "quota", Basis: p.Basis, Fingerprint: p.Fingerprint,
			Schedule: []byte(p.Schedule), Derivation: p.Derivation,
		})
		if err != nil {
			return err
		}
		return store.New().SetEndpointCost(ctx, tx, store.SetEndpointCostParams{ID: w.endpoint, CostScheduleID: &plan})
	})
	if err := w.svc.Reload(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
}

// endpointCost is the price lines of the world's endpoint.
func (w *world) endpointCost() (cost EndpointCost) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		cost, err = w.adminOf().EndpointCost(ctx, tx, w.supplier, w.endpoint, PlatformOwned)
		return err
	})
	return cost
}
