package billing

import (
	"bytes"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

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
func fetches(tb testing.TB, input int64, n string) Segment {
	tb.Helper()
	return segment(tb, map[catalog.Item]int64{in: input, catalog.ItemOutput: 20},
		catalog.UnknownUsage{Field: "usage.server_tool_use.web_fetch_requests", Value: n})
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

// fallbackWindow has a correction window short enough to wait out; lapsedWindow also lets every reservation lapse.
var (
	fallbackWindow = Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, CorrectionWindow: 300 * time.Millisecond}
	lapsedWindow   = Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond, CorrectionWindow: 300 * time.Millisecond}
)

func (b *bank) expire() {
	b.t.Helper()
	time.Sleep(400 * time.Millisecond)
	b.cleanup()
}

// TestFallbackPrices pins the amounts of docs/domains/billing.md「价目表缺项」 on the metered entry (input and output 1
// a token, a search 10; 1000 bytes reserve 1100) with testFallbacks: the first charge, the corrections by cost, the
// end of the window and a late Settle, none of them free.
func TestFallbackPrices(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"a known per-use item without a price takes the default", fallbackDefault},
		{"unknown uses: first, corrected by cost, back to the record, window ends", fallbackCorrected},
		{"unknown tokens corrected by cost", fallbackTokens},
		{"the window ends at the fallback price", fallbackWindowEnds},
		{"corrected by cost down to the floor, another reservation covered", fallbackFloor},
		{"a cleaned-up call's record with unknown uses", fallbackCleanedUp},
		{"a late Settle after cleanup released charges the uses", fallbackLate},
		{"a late Settle past the floor", fallbackLateFloor},
		{"a waiver keeps the uses, also when corrected", fallbackWaiver},
	} {
		t.Run(tc.name, tc.run)
	}
}

func fallbackDefault(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, fallbackWindow)
	h := b.hold(1000)
	s := segment(t, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20, webSearch: 1, catalog.ItemCodeExecution: 2})
	if f := b.settleWith(h, outcome(Committed, FinishStop, false, s)); f.Estimated || f.Charged != platform.AmountExact(70+10+100) {
		t.Errorf("Settle = %+v, want 180 exact: the search at the model's 10, the executions at the default 50", f)
	}
	b.expect("settled", 180)
}

func fallbackCorrected(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, fallbackWindow)
	h := b.hold(1000)
	if f := b.settleWith(h, outcome(Committed, FinishStop, false, fetches(t, 50, "2"))); !f.Estimated || f.Charged != platform.AmountExact(170) {
		t.Errorf("Settle = %+v, want 170 estimated", f)
	}
	b.expect("estimated", 170)
	b.correct(h, 1, 1, 300, fetches(t, 400, "2")) // 1.5 × 300
	b.expect("estimated", 450)
	b.correct(h, 1, 2, 1000, fetches(t, 400, "2")) // 1500, past the hold of 1100: the free balance pays
	b.expect("estimated", 1500)
	if n := b.borne(); n != 0 {
		t.Errorf("the platform bore %d, want 0", n)
	}
	b.correct(h, 1, 3, 1000, used(t, 400, 20, 0)) // a record that maps everything prices at the locked schedule
	b.expect("estimated", 420)
	b.expire()
	b.expect("settled", 420)
	if n := b.count("SELECT balance FROM billing.balances"); n != 10_000-420 {
		t.Errorf("balance %d, want %d", n, 10_000-420)
	}
}

func fallbackTokens(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, fallbackWindow)
	h := b.hold(1000)
	b.settleWith(h, outcome(Committed, FinishStop, false, unknown(t, 50, "usage.new_thing_tokens", "30")))
	b.expect("estimated", 100)
	b.correct(h, 1, 1, 9, unknown(t, 400, "usage.new_thing_tokens", "40"))
	b.expect("estimated", 14) // ⌈1.5 × 9⌉
}

func fallbackWindowEnds(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, fallbackWindow)
	b.settleWith(b.hold(1000), outcome(Committed, FinishStop, false, fetches(t, 50, "2")))
	b.expire()
	b.expect("settled", 170)
	if n := b.count("SELECT reserved FROM billing.balances"); n != 0 {
		t.Errorf("%d still reserved", n)
	}
}

func fallbackFloor(t *testing.T) {
	b := fallbackBank(t, 1200, 0, fallbackWindow)
	other := b.hold(100) // 200 reserved
	h := b.hold(800)     // 900 reserved
	b.settleWith(h, outcome(Committed, FinishStop, false, fetches(t, 50, "2")))
	b.correct(h, 1, 1, 2000, fetches(t, 400, "2")) // 3000 asked: 1200 - 170 - 200 left above the floor
	if n := b.count("SELECT charged FROM billing.reservations WHERE request_id = '" + h.id.String() + "'"); n != 1000 {
		t.Errorf("charged %d, want 170 + 830 = 1000", n)
	}
	if n := b.borne(); n != 2000 {
		t.Errorf("the platform bore %d, want 2000", n)
	}
	b.balanced()
	b.settleWith(other, outcome(Committed, FinishStop, false, used(t, 100, 100, 0)))
	if n := b.count("SELECT balance FROM billing.balances"); n != 0 {
		t.Errorf("balance %d, want 0: the other reservation was still covered", n)
	}
	b.balanced()
}

func fallbackCleanedUp(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, lapsedWindow)
	h := b.hold(1000)
	b.supplies.journal[h.id] = JournalAttempt{Seq: 1, Key: "client:abc"}
	b.cleanup()
	b.correct(h, 1, 1, 100, fetches(t, 400, "2"))
	b.expect("cleanup_settled", 150)
}

// fetchesOnly is three web fetches and nothing else: 150 at the fallback price.
func fetchesOnly(t *testing.T) Outcome {
	t.Helper()
	return outcome(Committed, FinishStop, false, segment(t, nil, catalog.UnknownUsage{Field: "usage.server_tool_use.web_fetch_requests", Value: "3"}))
}

func fallbackLate(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, lapsedWindow)
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
	b := fallbackBank(t, 100, 0, lapsedWindow)
	h := b.hold(0) // the 100 output tokens
	b.cleanup()
	b.settleWith(h, fetchesOnly(t))
	b.expect("settled", 100)
	if n := b.borne(); n != 50 {
		t.Errorf("the platform bore %d, want 50", n)
	}
}

func fallbackWaiver(t *testing.T) {
	waiving := fallbackWindow
	waiving.WaiverCap = 1000
	b := fallbackBank(t, 10_000, 0, waiving)
	h := b.hold(1000)
	s := segment(t, map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20},
		catalog.UnknownUsage{Field: "usage.new_thing_tokens", Value: "30"},
		catalog.UnknownUsage{Field: "usage.server_tool_use.web_fetch_requests", Value: "2"})
	if f := b.settleWith(h, outcome(Unknown, FinishNone, false, s)); f.Charged != platform.AmountExact(100) {
		t.Errorf("Settle = %+v, want the two uses alone, 100", f)
	}
	b.correct(h, 1, 1, 1000, fetches(t, 400, "3")) // waived: by the record's uses, not by cost
	b.expect("estimated", 150)
}

// TestConcurrentCorrections: revisions delivered at once in any order leave the newest one's charge by cost, once.
func TestConcurrentCorrections(t *testing.T) {
	for range 3 {
		b := fallbackBank(t, 10_000, 0, Config{})
		h := b.hold(1000)
		b.settleWith(h, outcome(Committed, FinishStop, false, fetches(t, 50, "2")))
		var wg sync.WaitGroup
		for revision := range int64(4) {
			wg.Go(func() {
				c := Correction{Request: h.id, Seq: 1, Revision: revision + 1, Usage: usages([]Segment{fetches(t, 400, "2")}), Cost: (revision + 1) * 100}
				if err := b.billing.Correct(t.Context(), c); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		b.expect("estimated", 600) // 1.5 × 400
		if c := b.cost(h, 1); c != 400 {
			t.Errorf("cost %d, want the newest record's 400", c)
		}
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

// TestUnknownItems: the first appearance of a field nothing prices, in a Settle or a correction, takes its model out
// of routing and alerts once, whichever tenant sees it again; an administrator's clearing, audited, lets it back.
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
	b.settleWith(first, outcome(Committed, FinishStop, false, fetches(t, 50, "1")))
	b.settleWith(b.holdOn(q, 100), outcome(Committed, FinishStop, false, fetches(t, 50, "2")))
	b.settleWith(b.holdFor(b.secondTenant(10_000), q, 100), outcome(Committed, FinishStop, false, fetches(t, 50, "1"))) // the row is the first tenant's
	estimate := b.holdOn(q, 100)
	b.settleWith(estimate, outcome(Committed, FinishStop, true, used(t, 50, 20, 0, in)))
	b.correct(estimate, 1, 1, 9, unknown(t, 50, "usage.new_thing_tokens", "5"))
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
	b.settleWith(b.holdOn(q, 100), outcome(Committed, FinishStop, false, fetches(t, 50, "1")))
	if n := strings.Count(logs.String(), "an unknown billing item appeared"); n != 2 || len(b.unknownItems()) != 1 {
		t.Errorf("a cleared field came back: %d alerts, %+v", n, b.unknownItems())
	}
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
	if _, err := b.tryHoldOn(cny, 100); !errors.Is(err, errUnpriced) {
		t.Errorf("a CNY entry without CNY fallbacks = %v, want errUnpriced", err)
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
	b.settleWith(h, outcome(Committed, FinishStop, false, s))
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

// TestCloseHold: the fallback holds are listed and counted on the gauges, an estimate on unreported usage is not; an
// operator closes one, audited, and cleanup settles it at its charge and releases the rest.
func TestCloseHold(t *testing.T) {
	b := fallbackBank(t, 10_000, 0, Config{})
	reader := sdkmetric.NewManualReader()
	if err := RegisterMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("billing"), b.app, b.platform); err != nil {
		t.Fatal(err)
	}
	fallback, guessed := b.hold(1000), b.hold(1000)
	b.settleWith(fallback, outcome(Committed, FinishStop, false, fetches(t, 50, "2")))
	b.settleWith(guessed, outcome(Committed, FinishStop, false, used(t, 300, 20, 0, in)))
	if holds := b.fallbackHolds(); len(holds) != 1 || holds[0].Request != fallback.id || holds[0].Held != 1100-170 || holds[0].By == nil {
		t.Fatalf("FallbackHolds = %+v; want the fallback estimate holding 930", holds)
	}
	wantGauges(t, reader, 1, 930)
	if err := b.closeHold(fallback.id, " "); err == nil {
		t.Error("a close without a reason went through")
	}
	if err := b.closeHold(fallback.id, "no record is coming"); err != nil {
		t.Fatal(err)
	}
	if err := b.closeHold(fallback.id, "again"); !errors.Is(err, ErrNotFound) {
		t.Errorf("closing again = %v, want ErrNotFound", err)
	}
	b.cleanup()
	if s := b.count("SELECT count(*) FROM billing.reservations WHERE state = 'settled' AND charged = 170 AND request_id = '" + fallback.id.String() + "'"); s != 1 {
		t.Error("the closed hold did not settle at 170")
	}
	if n := b.count("SELECT reserved FROM billing.balances"); n != 1100-320 {
		t.Errorf("%d reserved, want the other estimate's 780 alone", n)
	}
	wantGauges(t, reader, 0, 0)
	if n := b.count("SELECT count(*) FROM platform.audit_log WHERE action = 'hold.close'"); n != 1 {
		t.Errorf("%d audit entries, want 1", n)
	}
	b.balanced()
}

func (b *bank) fallbackHolds() (holds []FallbackHold) {
	b.t.Helper()
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		holds, err = FallbackHolds(b.t.Context(), tx)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return holds
}

func (b *bank) closeHold(request uuid.UUID, reason string) error {
	r := CloseHoldRequest{Request: request, Reason: reason}
	r.SetActor(audit.System("billing.test"))
	return b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error { return CloseHold(b.t.Context(), tx, r) })
}

func wantGauges(t *testing.T, reader *sdkmetric.ManualReader, holds, held int64) {
	t.Helper()
	if g := gauges(t, reader); g["axiom.billing.fallback_holds"] != holds || g["axiom.billing.fallback_holds.held"] != held {
		t.Errorf("gauges %v, want %d and %d", g, holds, held)
	}
}

func gauges(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	for _, sm := range data.ScopeMetrics {
		for _, m := range sm.Metrics {
			if g, ok := m.Data.(metricdata.Gauge[int64]); ok && len(g.DataPoints) == 1 {
				seen[m.Name] = g.DataPoints[0].Value
			}
		}
	}
	return seen
}
