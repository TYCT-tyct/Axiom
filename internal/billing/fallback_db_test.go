package billing

import (
	"bytes"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// saveFallbacks saves body, a row in its stored form, through SaveFallbacks.
func (b *bank) saveFallbacks(body string) error {
	var r SaveFallbacksRequest
	if err := jsonx.UnmarshalKnown([]byte(body), &r); err != nil {
		b.t.Fatal(err)
	}
	r.Reason = "test"
	r.SetActor(audit.System("billing.test"))
	return b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error { return SaveFallbacks(b.t.Context(), tx, r) })
}

// fallbackBank is a bank whose fallbacks are testFallbacks: a web search 20, a code execution 50, markup 1.5.
func fallbackBank(t *testing.T, balance, floor int64, config Config) *bank {
	t.Helper()
	b := newBank(t, balance, floor, config)
	if err := b.saveFallbacks(testFallbacks); err != nil {
		t.Fatal(err)
	}
	return b
}

// segment is one sampling with the given counts, all reported, and usage protocol could not map.
func segment(tb testing.TB, counts map[catalog.Item]int64, unknown ...catalog.UnknownUsage) Segment {
	tb.Helper()
	u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, unknown)
	if err != nil {
		tb.Fatal(err)
	}
	return Segment{Usage: u, Reported: ^catalog.ItemSet(0)}
}

// fetches is 50 input, 20 output and n web fetches, which protocol cannot map: 70 and n uses at 50 each.
func fetches(tb testing.TB, n string) Segment {
	tb.Helper()
	return segment(tb, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20},
		catalog.UnknownUsage{Field: "usage.server_tool_use.web_fetch_requests", Value: n})
}

// servedAt is segment with its usage reported at the given service tier.
func servedAt(tb testing.TB, st catalog.ServiceTier, counts map[catalog.Item]int64) Segment {
	tb.Helper()
	u, err := catalog.NewUsage(counts, st, nil)
	if err != nil {
		tb.Fatal(err)
	}
	return Segment{Usage: u, Reported: ^catalog.ItemSet(0)}
}

func (b *bank) settleWith(h *Hold, o Outcome) Final {
	b.t.Helper()
	f, err := b.billing.Settle(b.t.Context(), h, o)
	if err != nil {
		b.t.Fatal(err)
	}
	return f
}

func (b *bank) expect(state string, charged int64) {
	b.t.Helper()
	if s, n := b.state(), b.count("SELECT charged FROM billing.reservations"); s != state || n != charged {
		b.t.Errorf("%s charged %d, want %s charged %d", s, n, state, charged)
	}
	b.balanced()
}

func (b *bank) borne() int64 {
	b.t.Helper()
	return b.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = 'platform_borne'")
}

// fallbackBank has reservations that stay pending long enough to settle; lapsedBank lets every reservation lapse, so
// cleanup takes it over.
var (
	fallbackBankConfig = Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour}
	lapsedBankConfig   = Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond}
)

// TestFallbackPrices pins the amounts of docs/domains/billing.md「价目表缺项」 on the metered entry (input and output 1
// a token, a search 10; 1000 bytes reserve 1100) with testFallbacks: the charge, cleanup's release, a late Settle and
// the floor, none of them free.
func TestFallbackPrices(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"a known per-use item without a price takes the default", fallbackDefault},
		{"a tier the price list lacks is charged, not given away", fallbackUnlistedTier},
		{"unknown uses are priced by the fallback", fallbackEstimated},
		{"unknown uses down to the floor, another reservation covered", fallbackFloor},
		{"a late Settle after cleanup released charges the uses", fallbackLate},
		{"a late Settle past the floor", fallbackLateFloor},
		{"a waiver keeps the uses", fallbackWaiver},
		{"the dearest fallbacks at the most uses: capped at the balance", fallbackHuge},
	} {
		t.Run(tc.name, tc.run)
	}
}

func fallbackDefault(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, fallbackBankConfig)
	h := b.hold(1000)
	s := segment(t, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20, webSearch: 1, catalog.ItemCodeExecution: 2})
	if f := b.settleWith(h, outcome(Committed, FinishStop, s)); f.Estimated || f.Charged != platform.AmountExact(70+10+100) {
		t.Errorf("Settle = %+v, want 180 exact: the search at the model's 10, the executions at the default 50", f)
	}
	b.expect("settled", 180)
}

// fallbackUnlistedTier: a schedule that prices the standard tier only, as one whose model's spec declares no other can,
// still charges a call its upstream served at priority — which no price list has, so Cost prices nothing — at the
// fallback prices, estimated: 70 tokens at the highest token price 1 and the search at its default 20. Before, the call
// was charged nothing but the flag (docs/domains/billing.md「定价」: an upstream that reports a tier the price list lacks
// is charged the fallback prices and marked estimated). A waiver keeps the search alone.
func fallbackUnlistedTier(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    Outcome
		want int64
	}{
		{"50 input and 20 output at priority, one search", outcome(Committed, FinishStop,
			servedAt(t, catalog.ServiceTierPriority, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20, webSearch: 1})), 90},
		{"waived: the tokens go, the search stays", outcome(Unknown, FinishNone,
			servedAt(t, catalog.ServiceTierPriority, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20, webSearch: 1})), 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := fallbackBank(t, 10_000, 0, fallbackBankConfig)
			if f := b.settleWith(b.hold(1000), tc.o); !f.Estimated || f.Charged != platform.AmountExact(tc.want) {
				t.Errorf("Settle = %+v, want %d estimated: a normal answer is never free", f, tc.want)
			}
			b.expect("settled", tc.want)
			if n := b.count("SELECT balance FROM billing.balances"); n != 10_000-tc.want {
				t.Errorf("balance %d, want %d", n, 10_000-tc.want)
			}
		})
	}
}

func fallbackEstimated(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, fallbackBankConfig)
	h := b.hold(1000)
	if f := b.settleWith(h, outcome(Committed, FinishStop, fetches(t, "2"))); !f.Estimated || f.Charged != platform.AmountExact(170) {
		t.Errorf("Settle = %+v, want 170 estimated", f)
	}
	b.expect("settled", 170)
	if n := b.count("SELECT reserved FROM billing.balances"); n != 0 {
		t.Errorf("%d still reserved, want 0: an estimate holds nothing once it is settled", n)
	}
	if n := b.count("SELECT balance FROM billing.balances"); n != 10_000-170 {
		t.Errorf("balance %d, want %d", n, 10_000-170)
	}
}

func fallbackFloor(t *testing.T) {
	b := fallbackBank(t, 1200, 0, fallbackBankConfig)
	other := b.hold(100) // 200 reserved
	h := b.hold(800)     // 900 reserved
	// 1000 input tokens and 1000 unknown uses at the dearest default price 50: 51000 priced, against the balance
	// above the floor and what the other call holds, which leaves 1000; the platform bears the rest.
	b.settleWith(h, outcome(Committed, FinishStop,
		segment(t, map[catalog.Item]int64{in: 1000, catalog.ItemOutput: 0}, catalog.UnknownUsage{Field: "usage.server_tool_use.web_fetch_requests", Value: "1000"})))
	if n := b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); n != 1000 {
		t.Errorf("charged %d, want 1000: what was above the floor and the other reservation", n)
	}
	if n := b.borne(); n <= 0 {
		t.Errorf("the platform bore %d, want the amount past the floor", n)
	}
	b.balanced()
	b.settleWith(other, outcome(Committed, FinishStop, used(t, 100, 100, 0)))
	if n := b.count("SELECT balance FROM billing.balances"); n != 0 {
		t.Errorf("balance %d, want 0: the other reservation was still covered", n)
	}
	b.balanced()
}

// fetchesOnly is three web fetches and nothing else: 150 at the fallback price.
func fetchesOnly(t *testing.T) Outcome {
	t.Helper()
	return outcome(Committed, FinishStop, segment(t, nil, catalog.UnknownUsage{Field: "usage.server_tool_use.web_fetch_requests", Value: "3"}))
}

func fallbackLate(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, lapsedBankConfig)
	h := b.hold(1000)
	b.cleanup()
	b.expect("cleanup_released", 0)
	first := b.settleWith(h, fetchesOnly(t))
	if first.Charged != platform.AmountExact(150) || !first.Estimated {
		t.Errorf("late Settle = %+v, want 150 estimated", first)
	}
	b.expect("settled", 150)
	if again := b.settleWith(h, fetchesOnly(t)); !reflect.DeepEqual(again, first) {
		t.Errorf("retry = %+v, want the stored %+v", again, first)
	}
}

func fallbackLateFloor(t *testing.T) {
	b := fallbackBank(t, 100, 0, lapsedBankConfig)
	h := b.hold(0) // the 100 output tokens
	b.cleanup()
	b.settleWith(h, fetchesOnly(t))
	b.expect("settled", 100)
	if n := b.borne(); n != 50 {
		t.Errorf("the platform bore %d, want 50", n)
	}
}

func fallbackWaiver(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, fallbackBankConfig)
	h := b.hold(1000)
	s := segment(t, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20},
		catalog.UnknownUsage{Field: "usage.new_thing_tokens", Value: "30"},
		catalog.UnknownUsage{Field: "usage.server_tool_use.web_fetch_requests", Value: "2"})
	if f := b.settleWith(h, outcome(Unknown, FinishNone, s)); f.Charged != platform.AmountExact(100) {
		t.Errorf("Settle = %+v, want the two uses alone, 100", f)
	}
	b.expect("settled", 100)
}

// fallbackHuge: valid settings price two unknown fields of 10¹⁰ uses at a default of one credit each past what an
// amount holds; the charge saturates instead of failing and the account pays what it has above the floor.
func fallbackHuge(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, fallbackBankConfig)
	if err := b.saveFallbacks(`{"currency":"USD","markup":"1.2","per_use":{"code_execution":"1","web_search":"1"}}`); err != nil {
		t.Fatal(err)
	}
	h := b.hold(1000)
	huge := segment(t, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20},
		catalog.UnknownUsage{Field: "usage.server_tool_use.web_fetch_requests", Value: "10000000000"},
		catalog.UnknownUsage{Field: "usage.server_tool_use.map_requests", Value: "10000000000"})
	if f := b.settleWith(h, outcome(Committed, FinishStop, huge)); f.Charged != platform.AmountExact(10_000) || !f.Estimated {
		t.Errorf("Settle = %+v, want the whole balance, 10000, estimated", f)
	}
	b.expect("settled", 10_000)
	if n := b.borne(); n != math.MaxInt64-10_000 {
		t.Errorf("the platform bore %d, want the saturated price less the balance", n)
	}
}

// secondTenant is another account on the same billing, with a balance.
func (b *bank) secondTenant(balance int64) Admitted {
	b.t.Helper()
	site, account := uuid.NewV7(), uuid.NewV7()
	a := Admitted{Scope: pg.NewScope(false, nil, []uuid.UUID{account}), Site: site, Account: account, Workspace: uuid.NewV7(), Key: uuid.NewV7()}
	if err := b.app.Tx(b.t.Context(), a.Scope, func(tx pg.Tx) error { return OpenBalance(b.t.Context(), tx, site, account, 0) }); err != nil {
		b.t.Fatal(err)
	}
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error {
		return AddCredit(b.t.Context(), tx, Credit{ID: uuid.NewV7(), Site: site, Account: account, Amount: balance, Actor: audit.System("billing.test"), Reason: "test"})
	}); err != nil {
		b.t.Fatal(err)
	}
	return a
}

func (b *bank) unknownItems() []UnknownItem {
	b.t.Helper()
	var items []UnknownItem
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		items, err = UnknownItems(b.t.Context(), tx)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return items
}

// TestUnknownItems: the first appearance of a field nothing prices, in a Settle, takes its model out of routing and
// alerts once, whichever tenant sees it again; an administrator's clearing, audited, lets it back.
func TestUnknownItems(t *testing.T) {
	var logs bytes.Buffer
	b := newBank(t, 10_000, 0, Config{
		HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour,
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err := b.saveFallbacks(testFallbacks); err != nil {
		t.Fatal(err)
	}
	model := catalog.ModelID(uuid.NewV7())
	q := metered(t)
	q.Model.ID = model
	const fetch = "usage.server_tool_use.web_fetch_requests"
	first := b.holdOn(q, 100)
	b.settleWith(first, outcome(Committed, FinishStop, fetches(t, "1")))
	b.settleWith(b.holdOn(q, 100), outcome(Committed, FinishStop, fetches(t, "2")))
	b.settleWith(b.holdFor(b.secondTenant(10_000), q, 100), outcome(Committed, FinishStop, fetches(t, "1"))) // the row is the first tenant's
	estimate := b.holdOn(q, 100)
	b.settleWith(estimate, outcome(Committed, FinishStop, unknown(t, 50, "usage.new_thing_tokens", "5")))
	items := b.unknownItems()
	for i := range items {
		items[i].Seen = time.Time{}
	}
	if want := []UnknownItem{{model, "usage.new_thing_tokens", estimate.id, time.Time{}}, {model, fetch, first.id, time.Time{}}}; !reflect.DeepEqual(items, want) {
		t.Errorf("unknown items %+v, want %+v", items, want)
	}
	if n := strings.Count(logs.String(), "an unknown billing item appeared"); n != 2 {
		t.Errorf("%d alerts, want one per field:\n%s", n, logs.String())
	}
	b.checkClearing(model, fetch)
	b.settleWith(b.holdOn(q, 100), outcome(Committed, FinishStop, fetches(t, "1")))
	if n := strings.Count(logs.String(), "an unknown billing item appeared"); n != 2 || len(b.unknownItems()) != 1 {
		t.Errorf("a cleared field came back: %d alerts, %+v", n, b.unknownItems())
	}
}

// TestUnknownItemsOnEveryAttempt: an attempt that reported fields nothing prices and then failed takes its own model
// out of routing, though the other entry's attempt is the one charged, and the charge follows the charged attempt
// alone.
func TestUnknownItemsOnEveryAttempt(t *testing.T) {
	const newThing = "usage.new_thing_tokens"
	b := fallbackBank(t, 10_000, 0, fallbackBankConfig)
	h, first := b.holdOnTwoModels()
	failed := segment(t, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20},
		catalog.UnknownUsage{Field: newThing, Value: "5"},
		catalog.UnknownUsage{Field: "usage.server_tool_use.web_fetch_requests", Value: "2"})
	o := Outcome{Attempts: []AttemptResult{
		{Seq: 1, Entry: 0, Cost: 5, Segments: []Segment{failed}, Result: FailedBeforeCommit, Finish: FinishError},
		{Seq: 2, Entry: 1, Cost: 7, Segments: []Segment{used(t, 50, 20, 0)}, Result: Committed, Finish: FinishStop},
	}}
	if f := b.settleWith(h, o); f.Charged != platform.AmountExact(70) || f.Estimated {
		t.Errorf("Settle = %+v, want the fallback's 70, exact", f)
	}
	b.expect("settled", 70)
	items := b.unknownItems()
	for i := range items {
		items[i].Seen = time.Time{}
	}
	want := []UnknownItem{
		{first.Model.ID, newThing, h.id, time.Time{}},
		{first.Model.ID, "usage.server_tool_use.web_fetch_requests", h.id, time.Time{}},
	}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("unknown items %+v, want %+v", items, want)
	}
}

// holdOnTwoModels reserves 100 text bytes on two metered entries of their own models, and returns the first.
func (b *bank) holdOnTwoModels() (*Hold, Quotable) {
	b.t.Helper()
	first, second := metered(b.t), metered(b.t)
	first.Model.ID, second.Model.ID = catalog.ModelID(uuid.NewV7()), catalog.ModelID(uuid.NewV7())
	var h *Hold
	if err := b.app.Tx(b.t.Context(), b.admitted.Scope, func(tx pg.Tx) (err error) {
		h, _, err = b.billing.Reserve(b.t.Context(), tx, b.admitted, uuid.NewV7(), Demand{TextBytes: 100}, []Quotable{first, second})
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return h, first
}

// holdFor reserves text bytes on q for another tenant a.
func (b *bank) holdFor(a Admitted, q Quotable, text int64) (h *Hold) {
	b.t.Helper()
	if err := b.app.Tx(b.t.Context(), a.Scope, func(tx pg.Tx) (err error) {
		h, _, err = b.billing.Reserve(b.t.Context(), tx, a, uuid.NewV7(), Demand{TextBytes: text}, []Quotable{q})
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return h
}

// checkClearing: a clearing needs a reason, is audited, and finds nothing the second time.
func (b *bank) checkClearing(model catalog.ModelID, field string) {
	b.t.Helper()
	clear := func(reason string) error {
		r := ClearUnknownItemRequest{Model: uuid.UUID(model), Field: field, Reason: reason}
		r.SetActor(audit.System("billing.test"))
		return b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error { return ClearUnknownItem(b.t.Context(), tx, r) })
	}
	if err := clear(" "); err == nil {
		b.t.Error("a clearing without a reason went through")
	}
	if err := clear("priced"); err != nil {
		b.t.Fatal(err)
	}
	if err := clear("again"); !errors.Is(err, ErrNotFound) {
		b.t.Errorf("clearing again = %v, want ErrNotFound", err)
	}
	if items := b.unknownItems(); len(items) != 1 || items[0].Field == field {
		b.t.Errorf("after clearing: %+v", items)
	}
	if n := b.count("SELECT count(*) FROM platform.audit_log WHERE action = 'unknown_item.clear'"); n != 1 {
		b.t.Errorf("%d audit entries, want 1", n)
	}
}

// TestFallbackSettings: a row that fails the check is refused on saving and stops Start; a currency without a row
// quotes nothing; a reservation keeps the fallbacks it locked when they change.
func TestFallbackSettings(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{})
	seeded := b.count("SELECT count(*) FROM billing.fallbacks WHERE currency = 'USD' AND body = " +
		`'{"currency":"USD","markup":"1.2","per_use":{"code_execution":"0.05","web_search":"0.025"}}'`)
	if seeded != 1 {
		t.Fatal("the migration did not seed the USD row")
	}
	for _, body := range []string{
		`{"currency":"USD","markup":"0.9","per_use":{"code_execution":"0.05","web_search":"0.025"}}`,
		`{"currency":"USD","markup":"1.2","per_use":{"code_execution":"0","web_search":"0.025"}}`,
		`{"currency":"USD","markup":"1.2","per_use":{"web_search":"0.025"}}`,
	} {
		if err := b.saveFallbacks(body); !errors.Is(err, errFallbacks) {
			t.Errorf("saving %s = %v, want refused", body, err)
		}
	}
	if b.count("SELECT count(*) FROM billing.fallbacks") != 1 || b.count("SELECT count(*) FROM platform.audit_log") != 1 {
		t.Error("a refused save changed something") // the one audit entry is newBank's credit
	}
	cny := metered(t)
	cny.Price = mustSchedule(t, `{"v":1,"currency":"CNY","rates":{"input":"0.000000001","output":"0.000000001"}}`)
	if _, err := b.tryHoldOn(cny, 100); !errors.Is(err, ErrUnpriced) {
		t.Errorf("a CNY entry without CNY fallbacks = %v, want ErrUnpriced", err)
	}
	if err := b.saveFallbacks(strings.ReplaceAll(testFallbacks, "USD", "CNY")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.tryHoldOn(cny, 100); err != nil {
		t.Errorf("a CNY entry with CNY fallbacks: %v", err)
	}
	h := b.holdOn(metered(t), 1000) // locks the seeded USD row
	if err := b.saveFallbacks(testFallbacks); err != nil {
		t.Fatal(err)
	}
	s := segment(t, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20, catalog.ItemCodeExecution: 1})
	b.settleWith(h, outcome(Committed, FinishStop, s))
	if n := b.count("SELECT price FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); n != 70+5e7 {
		t.Errorf("priced %d, want the code execution at the locked 0.05", n)
	}
	if n := b.count("SELECT count(*) FROM platform.audit_log WHERE action = 'fallbacks.save'"); n != 2 {
		t.Errorf("%d saves audited, want 2", n)
	}
}

// TestStartChecksFallbacks: Start refuses fallback rows that fail the check, or no USD row, before it registers.
func TestStartChecksFallbacks(t *testing.T) {
	for name, statement := range map[string]string{
		"an invalid row":               `UPDATE billing.fallbacks SET body = replace(body, '"markup":"1.2"', '"markup":"0.5"')`,
		"a row that is no JSON":        `UPDATE billing.fallbacks SET body = '{'`,
		"a row under another currency": `INSERT INTO billing.fallbacks (currency, body) SELECT 'EUR', body FROM billing.fallbacks`,
		"no USD row":                   `DELETE FROM billing.fallbacks`,
	} {
		t.Run(name, func(t *testing.T) {
			b := newBank(t, 0, 0, Config{})
			b.sql(statement)
			holders := b.count("SELECT count(*) FROM billing.holders")
			if _, err := Start(t.Context(), b.app, b.platform, Config{HolderTTL: time.Minute}, b.supplies); !errors.Is(err, errFallbacks) {
				t.Errorf("Start = %v, want errFallbacks", err)
			}
			if n := b.count("SELECT count(*) FROM billing.holders"); n != holders {
				t.Errorf("a refused Start registered a holder")
			}
		})
	}
}
