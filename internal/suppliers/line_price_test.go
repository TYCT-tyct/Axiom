package suppliers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
	"axiom/internal/suppliers/internal/registry"
)

// pastedWorld is a catalog with acme's model stone, a provider of acme's models reached with pasted keys, and the admin
// and call sides over one database. The provider's types are made here, not taken from the providers' packages, so that
// taking one of those out leaves this package building (just adapters-removable): pasted lists its keys' models,
// wallet also reads each key's balance, and aggregate names no author it serves, as a type that sells any author's
// models does.
type pastedWorld struct {
	admins
	cat      *catalog.Catalog
	svc      *Service
	provider *keyProvider
	models   map[string]catalog.ModelID
}

// The model specification and prices of the world: stone's cache read times 0.8 has more decimals than a unit price.
const (
	stoneSpec = `{"items":["input","output","cache_read"],"tiers":["standard"],"context_length":200000,"max_output_tokens":8192,` +
		`"streams":true,"abilities":["tools"]}`
	stonePrice = `{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000015","cache_read":"0.000000123456789"}}`
)

func newPastedWorld(t *testing.T) *pastedWorld {
	t.Helper()
	provider := newKeyProvider(t)
	data := pgtest.New(t, Module(), catalog.Migrations)
	db, err := pg.Open(t.Context(), data.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	cat, err := catalog.Open(db, platformScope, catalog.Config{MaxStale: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	entry := []adapter.PriceEntry{{ID: "global", Origin: provider.server.URL, Currency: "USD"}}
	typeOf := func(kind string) adapter.Type {
		return adapter.Type{
			Kind: adapter.Kind(kind), Name: kind, Vendor: "acme", Serves: []string{"acme"}, Group: adapter.GroupOfficial,
			Entries: entry, Families: []adapter.Family{adapter.FamilyOpenAI}, Lists: true,
		}
	}
	aggregate := typeOf("aggregate")
	aggregate.Serves = nil
	reg, err := registry.New(
		direct.New(direct.Provider{Type: typeOf("pasted")}),
		direct.New(direct.Provider{Type: aggregate}),
		direct.New(direct.Provider{Type: typeOf("wallet"), Balance: &direct.Balance{Path: "/v1/balance", Read: walletBalance}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	keys, clock := newKeys(t), &testClock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	out := egress.NewForTesting(egresstest.Loopback(t, provider.server.Certificate()))
	admin, err := NewAdmin(AdminConfig{Keys: keys, Egress: out, Registry: reg, Clock: clock.now, Byok: true})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{DB: db, Scope: platformScope, Keys: keys, Egress: out, Registry: reg, Clock: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	w := &pastedWorld{cat: cat, svc: svc, provider: provider, models: map[string]catalog.ModelID{}}
	w.admins = admins{t: t, db: db, admin: admin, keys: keys, data: data, actor: audit.System("suppliers.test"), clock: clock}
	w.list("stone", stonePrice)
	return w
}

// walletBalance reads the wallet type's answer, {"amount": "…", "currency": "…"}.
func walletBalance(body []byte, _ platform.Currency) (adapter.Money, error) {
	var b struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := jsonx.Unmarshal(body, &b); err != nil {
		return adapter.Money{}, err
	}
	return adapter.ParseMoney(b.Amount, b.Currency)
}

// op is a catalog operation of the tests.
func (w *pastedWorld) op() catalog.Op {
	return catalog.Op{Actor: w.actor, Key: uuid.NewV7(), Reason: "test"}
}

// list puts acme's model of the name on sale at the price, taken from acme's own price list.
func (w *pastedWorld) list(name, price string) { w.listFrom(name, price, catalog.SourceOfficial) }

// listFrom puts acme's model of the name on sale at the price, from the source.
func (w *pastedWorld) listFrom(name, price string, source catalog.PriceSource) {
	w.t.Helper()
	ctx := w.t.Context()
	if _, err := w.cat.SaveAuthor(ctx, catalog.SaveAuthorRequest{Op: w.op(), Slug: "acme", Name: "acme"}); err != nil {
		w.t.Fatal(err)
	}
	made, err := w.cat.CreateModel(ctx, catalog.CreateModelRequest{
		Op: w.op(), Slug: "acme/" + name, VersionBasis: catalog.VersionDated, PriceSource: source, Spec: jsonx.Raw(stoneSpec),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	id, err := uuid.Parse(made.Subject)
	if err != nil {
		w.t.Fatal(err)
	}
	model := catalog.ModelID(id)
	for _, public := range []string{"acme/" + name, name} {
		if _, err := w.cat.AddName(ctx, catalog.AddNameRequest{Op: w.op(), Model: model, Name: public, Visibility: catalog.VisibilityPublic}); err != nil {
			w.t.Fatal(err)
		}
	}
	w.models[name] = model
	w.price(model, price)
	if _, err := w.cat.ListModel(ctx, catalog.ModelRequest{Op: w.op(), Model: model}); err != nil {
		w.t.Fatal(err)
	}
}

// price publishes the model's USD reference price from the model's source.
func (w *pastedWorld) price(model catalog.ModelID, price string) {
	w.t.Helper()
	snap := w.snap()
	var base *uuid.UUID
	if ref, ok := snap.ReferencePrice(model, "USD"); ok {
		base = new(ref.ID)
	}
	m, err := w.cat.Model(w.t.Context(), model)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.cat.PublishPrice(w.t.Context(), catalog.PublishPriceRequest{
		Op: w.op(), Model: model, Base: base, Source: m.PriceSource, Schedule: jsonx.Raw(price), Evidence: w.evidence(m.PriceSource, "USD"),
	}); err != nil {
		w.t.Fatal(err)
	}
}

// evidence is what a price from the source rests on: for acme's own price list, the current snapshot of a watched
// page of it in the currency, as a page watch would have left it.
func (w *pastedWorld) evidence(source catalog.PriceSource, currency string) jsonx.Raw {
	w.t.Helper()
	if source != catalog.SourceOfficial {
		return jsonx.Raw(`{"url":"https://example.com/pricing"}`)
	}
	const text = "stone input 3 per million"
	digest := sha256.Sum256([]byte(text))
	var page, snapshot uuid.UUID
	if err := w.db.Tx(w.t.Context(), platformScope, func(tx pg.Tx) (err error) {
		ctx, q := w.t.Context(), catalog.New()
		if err := q.RegisterPageSite(ctx, tx, catalog.RegisterPageSiteParams{Origin: "https://example.com", DelaySeconds: 10}); err != nil {
			return err
		}
		if page, err = q.InsertWatchedPage(ctx, tx, catalog.InsertWatchedPageParams{
			Author: "acme", Url: "https://example.com/" + uuid.NewV7().String(), Origin: "https://example.com", Currency: currency,
			SectionStart: "Start", SectionEnd: "End", IntervalSeconds: 3600,
		}); err != nil {
			return err
		}
		if snapshot, err = q.InsertPageSnapshot(ctx, tx, catalog.InsertPageSnapshotParams{PageID: page, ExtractorVersion: 1, Text: text, TextSha256: digest[:]}); err != nil {
			return err
		}
		return q.RecordPageSuccess(ctx, tx, catalog.RecordPageSuccessParams{ID: page, CurrentSnapshotID: &snapshot, Seconds: 3600})
	}); err != nil {
		w.t.Fatal(err)
	}
	body, err := jsonx.MarshalCanonical(catalog.PageEvidence{PageID: page, SnapshotID: snapshot, Excerpts: []string{"input 3 per million"}})
	if err != nil {
		w.t.Fatal(err)
	}
	return body
}

// snap is the catalog as it is now.
func (w *pastedWorld) snap() *catalog.Snapshot {
	w.t.Helper()
	if err := w.cat.Refresh(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	snap, err := w.cat.Current()
	if err != nil {
		w.t.Fatal(err)
	}
	return snap
}

// connect connects a direct API of the kind with the keys, at the ratio (empty for none given), as the admin handler
// does, and returns the supplier.
func (w *pastedWorld) connect(kind, ratio string, keys ...string) uuid.UUID {
	w.t.Helper()
	ctx := w.t.Context()
	r := DirectRequest{Kind: kind, Name: kind, Ratio: ratio, Keys: keys, MaxConcurrency: 4, Reason: "a direct source"}
	var plan *DirectPlan
	if err := w.db.ReadTx(ctx, platformScope, func(tx pg.Tx) (err error) {
		plan, err = w.admin.PlanDirect(ctx, tx, r)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	if err := w.admin.CheckDirect(ctx, plan); err != nil {
		w.t.Fatal(err)
	}
	snap := w.snap()
	made, err := inTx(w.admins, func(ctx context.Context, tx pg.Tx) (Connected, error) {
		return w.admin.CreateDirect(ctx, tx, w.actor, snap, plan)
	})
	if err != nil || made.Supplier == nil {
		w.t.Fatalf("connecting %s: %+v (%v)", kind, made.Checks, err)
	}
	return made.Supplier.ID
}

// endpoint is the supplier's endpoint of the model.
func (w *pastedWorld) endpoint(supplier uuid.UUID, model string) uuid.UUID {
	w.t.Helper()
	return uuid.MustParse(w.text("SELECT id::text FROM suppliers.endpoints WHERE supplier_id = $1 AND model_id = $2", supplier, uuid.UUID(w.models[model])))
}

// plan is the cost plan in force on the endpoint, and how many plans it has.
func (w *pastedWorld) plan(endpoint uuid.UUID) (string, string) {
	w.t.Helper()
	return w.text("SELECT cost_schedule_id::text FROM suppliers.endpoints WHERE id = $1", endpoint),
		w.text("SELECT count(*)::text FROM suppliers.cost_schedules WHERE endpoint_id = $1", endpoint)
}

// setRatio sets the supplier's line ratio as the admin handler does.
func (w *pastedWorld) setRatio(supplier uuid.UUID, ratio string) error {
	w.t.Helper()
	snap := w.snap()
	_, err := inTx(w.admins, func(ctx context.Context, tx pg.Tx) (Supplier, error) {
		return w.admin.SetCostRatio(ctx, tx, w.actor, snap, supplier, ratio, "a discount")
	})
	return err
}

// keyProvider is a provider's API whose keys each list their models and hold a balance of their own; a key it does not
// know it refuses with a 401.
type keyProvider struct {
	server *httptest.Server
	mu     sync.Mutex
	keys   map[string]providerKey
}

// providerKey is what the provider knows of one key: the models it lists, its balance in a currency, and whether it
// refuses the key's calls for want of funds.
type providerKey struct {
	models            []string
	balance, currency string
	broke             bool
}

func newKeyProvider(t *testing.T) *keyProvider {
	t.Helper()
	p := &keyProvider{keys: map[string]providerKey{}}
	p.server = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func (p *keyProvider) serve(rw http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	k, ok := p.keys[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	p.mu.Unlock()
	var body string
	switch {
	case !ok:
		rw.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/v1/models":
		ids := make([]string, len(k.models))
		for i, m := range k.models {
			ids[i] = fmt.Sprintf(`{"id":%q}`, m)
		}
		body = `{"data":[` + strings.Join(ids, ",") + `]}`
	case r.URL.Path == "/v1/balance":
		body = fmt.Sprintf(`{"amount":%q,"currency":%q}`, k.balance, k.currency)
	case r.URL.Path == "/v1/chat/completions" && k.broke:
		rw.WriteHeader(http.StatusPaymentRequired)
	}
	_, _ = rw.Write([]byte(body)) //nolint:errcheck // what arrives is what the test's checks read
}

// set makes the provider know the key as k, or forget it for a zero k.
func (p *keyProvider) set(key string, k providerKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k.models == nil {
		delete(p.keys, key)
		return
	}
	p.keys[key] = k
}

// scaled is the price times the ratio, rounded up to a unit price once: what each price of a line's plan is.
func scaled(t *testing.T, price *big.Rat, ratio string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(ratio)
	if !ok {
		t.Fatalf("the ratio %s", ratio)
	}
	units := new(big.Rat).Mul(new(big.Rat).Mul(price, r), big.NewRat(1e15, 1))
	q, rem := new(big.Int).QuoRem(units.Num(), units.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return new(big.Rat).SetFrac(q, big.NewInt(1e15))
}

// expectPriced checks that every price of the endpoint's plan, as EndpointCost shows it, is the price's times the
// ratio rounded up once, and returns those prices by item.
func (w *pastedWorld) expectPriced(what string, line, endpoint uuid.UUID, price, ratio string) map[catalog.Item]*big.Rat {
	w.t.Helper()
	list, err := catalog.ParseSchedule([]byte(price))
	if err != nil {
		w.t.Fatal(err)
	}
	cost, err := inTx(w.admins, func(ctx context.Context, tx pg.Tx) (EndpointCost, error) {
		return w.admin.EndpointCost(ctx, tx, line, endpoint, PlatformOwned)
	})
	if err != nil || len(cost.Lines) == 0 {
		w.t.Fatalf("%s: the cost %+v (%v)", what, cost, err)
	}
	want := map[catalog.Item]*big.Rat{}
	for _, l := range cost.Lines {
		rate, _ := list.BaseRate(l.Item)
		want[l.Item] = scaled(w.t, rate.Rat(), ratio)
		if l.Native.Rat().Cmp(want[l.Item]) != 0 {
			w.t.Errorf("%s: %s costs %s, want %s × %s rounded up: %s", what, l.Item, l.Native.Rat().FloatString(15),
				rate.Rat().FloatString(15), ratio, want[l.Item].FloatString(15))
		}
	}
	return want
}

// expectAttemptCost checks that an attempt at the endpoint with the usage costs exactly the usage at the prices.
func (w *pastedWorld) expectAttemptCost(what string, endpoint uuid.UUID, usage map[catalog.Item]int64, prices map[catalog.Item]*big.Rat) {
	w.t.Helper()
	want := new(big.Rat)
	for item, n := range usage {
		want.Add(want, new(big.Rat).Mul(prices[item], big.NewRat(n, 1)))
	}
	if err := w.svc.Reload(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	a, err := w.svc.Acquire(endpoint)
	if err != nil {
		w.t.Fatalf("%s: %v", what, err)
	}
	u, err := catalog.NewUsage(usage, catalog.ServiceTierStandard, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	got, err := w.svc.Cost(a.Done(), []catalog.Usage{u})
	if err != nil || got.Flag != CostComplete || got.Native.Rat().Cmp(want) != 0 {
		w.t.Errorf("%s: an attempt costs %s (%v, %v), want %s", what, got.Native.Rat().FloatString(15), got.Flag, err, want.FloatString(15))
	}
}

// ownKey is an account's own key (BYOK) for stone, and its plans as text: the plan in force on each endpoint and how
// many it has.
func (w *pastedWorld) ownKey() (uuid.UUID, func() string) {
	w.t.Helper()
	site, account := uuid.NewV7(), uuid.NewV7()
	registerTenant(w.admins, site, account)
	own, err := inTx(w.admins, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return w.admin.CreateByok(ctx, tx, ByokRequest{
			Site: site, Account: account, Name: "my key", Provider: "openai", Key: "sk-proj-abcdefghijklmnop", Actor: w.actor,
		}, []ByokModel{{
			ID: uuid.UUID(w.models["stone"]), Upstream: "stone", Formats: []protocol.Format{protocol.Chat},
			Schedule: []byte(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0","cache_read":"0"}}`),
		}})
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return own.ID, func() string {
		return w.text(`SELECT string_agg(e.cost_schedule_id::text || '/' || (SELECT count(*) FROM suppliers.cost_schedules c
			WHERE c.endpoint_id = e.id)::text, ',') FROM suppliers.endpoints e WHERE e.supplier_id = $1`, own.ID)
	}
}

// TestALinesCostIsItsTypesPriceTimesItsRatio: the line ratio's invariant (docs/domains/suppliers-direct.md「成本与线路
// 倍率」). After each step, every price of the endpoint's plan, as EndpointCost shows it, is the type's price times the
// line's ratio rounded up once, and an attempt costs exactly its usage at those prices; the products are computed
// apart from the code under test. A ratio changed and changed back points the endpoint at the plan it had, without a
// plan of the same fingerprint again; a line connected at a ratio is priced at it from the start. An account's own key
// is not the platform's to set a ratio for, and its plans stay as they were.
func TestALinesCostIsItsTypesPriceTimesItsRatio(t *testing.T) {
	t.Parallel()
	w := newPastedWorld(t)
	usage := map[catalog.Item]int64{catalog.ItemInput: 1000, catalog.ItemOutput: 100, catalog.ItemCacheRead: 7}
	var line, endpoint uuid.UUID
	var connectedWith string
	for i, step := range []struct {
		name, ratio string
		connect     string // the ratio a new line is connected at, "-" for a step that sets the last line's instead
		plans       string // how many plans the endpoint has after the step
		original    bool   // the endpoint is back at the plan it was connected with
	}{
		{"connected without a ratio", "1", "", "1", true},
		{"set to 0.8", "0.8", "-", "2", false},
		{"set back to 1", "1", "-", "2", true},
		{"connected at 0.8", "0.8", "0.8", "1", true},
	} {
		if step.connect != "-" {
			key := fmt.Sprintf("sk-good-key-%d", i)
			w.provider.set(key, providerKey{models: []string{"stone"}})
			line = w.connect("pasted", step.connect, key)
			endpoint = w.endpoint(line, "stone")
			connectedWith, _ = w.plan(endpoint)
		} else if err := w.setRatio(line, step.ratio); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if plan, plans := w.plan(endpoint); plans != step.plans || (plan == connectedWith) != step.original {
			t.Errorf("%s: the endpoint is at %s of %s plans, connected with %s", step.name, plan, plans, connectedWith)
		}
		w.expectAttemptCost(step.name, endpoint, usage, w.expectPriced(step.name, line, endpoint, stonePrice, step.ratio))
	}

	own, plans := w.ownKey()
	before := plans()
	refuses(t, "a ratio for an account's own key", w.setRatio(own, "0.5"), ErrNoSupplier)
	if after := plans(); after != before {
		t.Errorf("an account's own key went from plans %s to %s", before, after)
	}
}
