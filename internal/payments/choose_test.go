package payments

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

const today = "(now() AT TIME ZONE 'UTC')::date"

func (w *world) site(id uuid.UUID, code string) accounts.Site {
	return accounts.Site{ID: accounts.SiteID(id), Currency: currency(w.t, code)}
}

// open opens top-ups on site, with strategy.
func (w *world) open(site uuid.UUID, strategy Strategy) {
	w.exec(fmt.Sprintf("INSERT INTO payments.settings (site_id, open, strategy) VALUES ('%s', true, '%s')", site, strategy))
}

// choose places an order's choice as an order would: the menu first, then the order's own transaction.
func (w *world) choose(s pg.Scope, site accounts.Site, method Method, credits int64) (candidate, error) {
	code := string(method)
	m, err := w.p.menu(w.t.Context(), s, site, &code)
	if err != nil {
		return candidate{}, err
	}
	var c candidate
	err = w.tx(s, func(tx pg.Tx) (err error) { c, err = w.p.choose(w.t.Context(), tx, m, method, credits); return err })
	return c, err
}

func (w *world) used(merchant uuid.UUID) int64 {
	return int64(w.count(pg.NewScope(true, nil, nil), "SELECT coalesce(sum(used), 0) FROM payments.merchant_usage WHERE merchant_id = $1", merchant))
}

func (w *world) count(s pg.Scope, query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.tx(s, func(tx pg.Tx) error { return tx.QueryRow(w.t.Context(), query, args...).Scan(&n) }); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (w *world) setRate(base, quote, numerator, denominator string) {
	w.t.Helper()
	err := w.tx(pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		_, err := billing.SetExchangeRate(w.t.Context(), tx, admin, billing.ExchangeInput{Base: base, Quote: quote, Numerator: numerator, Denominator: denominator, Kind: "initial", Reason: "test"})
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

func TestChooseRefusesWhatNoMerchantTakes(t *testing.T) {
	w := newWorld(t)
	w.open(w.siteA, RoundRobin)
	cny := w.site(w.siteA, "CNY")
	limits := MethodInput{Method: "alipay", Upstream: "alipay", SingleMin: new(int64(500)), SingleMax: new(int64(10_000)), DailyLimit: new(int64(20_000)), Enabled: true}
	m := w.mustSave(siteScope(w.siteA), w.input(w.siteA, limits))
	for name, c := range map[string]struct {
		method  Method
		credits int64
	}{
		"below the single minimum": {"alipay", 4 * yuan},
		"above the single maximum": {"alipay", 101 * yuan},
		"no merchant has it":       {"wxpay", 10 * yuan},
	} {
		if _, err := w.choose(w.accountA(), cny, c.method, c.credits); !errors.Is(err, ErrNoMerchant) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for range 2 {
		if _, err := w.choose(w.accountA(), cny, "alipay", 100*yuan); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.choose(w.accountA(), cny, "alipay", 5*yuan); !errors.Is(err, ErrNoMerchant) || w.used(m.ID) != 20_000 {
		t.Fatalf("over the daily limit: %v, used %d", err, w.used(m.ID))
	}
	w.mustSave(siteScope(w.siteA), w.input(w.siteA)) // a second merchant without limits takes it
	if c, err := w.choose(w.accountA(), cny, "alipay", 5*yuan); err != nil || c.offer.MerchantID == m.ID {
		t.Fatalf("with another merchant: %+v, %v", c, err)
	}
	w.exec("REVOKE SELECT ON payments.merchant_usage FROM axiom_app")
	if _, err := w.choose(w.accountA(), cny, "alipay", 5*yuan); err == nil || errors.Is(err, ErrNoMerchant) {
		t.Fatalf("usage unreadable: %v", err)
	}
}

// TestChooseGivesTheLastQuotaOnlyToWhatFits has ten orders grab a daily limit that fits three, each in its own
// account-scoped transaction, all at once.
func TestChooseGivesTheLastQuotaOnlyToWhatFits(t *testing.T) {
	w := newWorld(t)
	w.open(w.siteA, RoundRobin)
	m := w.mustSave(siteScope(w.siteA), w.input(w.siteA, MethodInput{Method: "alipay", Upstream: "a", DailyLimit: new(int64(3 * 1000)), Enabled: true}))
	start, done := make(chan struct{}), make(chan error, 10)
	for range 10 {
		go func() {
			<-start
			_, err := w.choose(w.accountA(), w.site(w.siteA, "CNY"), "alipay", 10*yuan)
			done <- err
		}()
	}
	close(start)
	admitted := 0
	for range 10 {
		switch err := <-done; {
		case err == nil:
			admitted++
		case !errors.Is(err, ErrNoMerchant):
			t.Error(err)
		}
	}
	if admitted != 3 || w.used(m.ID) != 3000 {
		t.Fatalf("admitted %d, used %d; want 3 and 3000", admitted, w.used(m.ID))
	}
}

// TestCreateOrderBoundsTheWaitForUsageRow holds the usage row in one transaction: another order gives up at the lock
// timeout instead of queueing behind it.
func TestCreateOrderBoundsTheWaitForUsageRow(t *testing.T) {
	w := newWorld(t)
	w.open(w.siteA, RoundRobin)
	m := w.mustSave(siteScope(w.siteA), w.input(w.siteA))
	held, release, holder := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		holder <- w.tx(siteScope(w.siteA), func(tx pg.Tx) error {
			_, err := queries.AddUsage(t.Context(), tx, AddUsageParams{MerchantID: m.ID, SiteID: w.siteA, Amount: 1})
			close(held)
			<-release
			return err
		})
	}()
	<-held
	_, _, err := w.place(yuan, "locked-usage")
	close(release)
	if pg.SQLState(err) != "55P03" {
		t.Fatalf("waited for the row: %v", err)
	}
	if err = <-holder; err != nil {
		t.Fatal(err)
	}
}

// TestUnusableMerchantsAreNeitherQuotedNorChosen breaks two of three merchants outside a save, one by editing its
// gateway and one by naming a plugin the program does not have: only the good one is quoted and chosen.
func TestUnusableMerchantsAreNeitherQuotedNorChosen(t *testing.T) {
	w := newWorld(t)
	w.open(w.siteA, RoundRobin)
	method := func(ppm int32) MethodInput {
		return MethodInput{Method: "alipay", Upstream: "a", Fee: Fee{PPM: ppm}, Enabled: true}
	}
	good := w.mustSave(siteScope(w.siteA), w.input(w.siteA, method(0)))
	tampered := w.mustSave(siteScope(w.siteA), w.input(w.siteA, method(10_000)))
	orphan := w.mustSave(siteScope(w.siteA), w.input(w.siteA, method(20_000)))
	w.exec(fmt.Sprintf(`UPDATE payments.merchant_configs SET settings = '{"gateway":"https://evil.example.com","methods":["alipay"]}' WHERE merchant_id = '%s'`, tampered.ID),
		fmt.Sprintf("UPDATE payments.merchants SET kind = 'gone' WHERE id = '%s'", orphan.ID))
	cny := w.site(w.siteA, "CNY")
	options, err := w.p.Options(t.Context(), w.accountA(), cny, 100*yuan)
	if err != nil || fmt.Sprint(options) != "[{alipay {CNY 10000} 0}]" {
		t.Fatalf("options = %v, %v", options, err)
	}
	for range 3 {
		if c, err := w.choose(w.accountA(), cny, "alipay", 100*yuan); err != nil || c.offer.MerchantID != good.ID {
			t.Fatalf("chose %v, %v; want the good merchant", c.offer.MerchantID, err)
		}
	}
	if w.used(tampered.ID) != 0 || w.used(orphan.ID) != 0 || w.used(good.ID) != 30_000 {
		t.Fatalf("usage %d, %d, %d", w.used(good.ID), w.used(tampered.ID), w.used(orphan.ID))
	}
	if log := w.logs.String(); !strings.Contains(log, tampered.ID.String()) && !strings.Contains(log, MerchantID(tampered.ID).TypeID()) {
		t.Fatalf("no alert for the tampered merchant: %s", log)
	}
}

// TestRoundRobinTurnsPerSiteAndMethod interleaves two rotations at a time: each must still alternate between its own
// two merchants, and the page must show the merchant the next order goes to.
func TestRoundRobinTurnsPerSiteAndMethod(t *testing.T) {
	w := newWorld(t)
	w.open(w.siteA, RoundRobin)
	w.open(w.siteB, RoundRobin)
	both := func(ppm int32) []MethodInput {
		return []MethodInput{{Method: "alipay", Upstream: "a", Fee: Fee{PPM: ppm}, Enabled: true}, {Method: "wxpay", Upstream: "w", Fee: Fee{PPM: ppm}, Enabled: true}}
	}
	for i, site := range []uuid.UUID{w.siteA, w.siteA, w.siteB, w.siteB} {
		w.mustSave(siteScope(site), w.input(site, both(int32(i%2)*10_000)...)) // the two of a site charge apart
	}
	a, aw, b := stream{w.accountA(), w.site(w.siteA, "CNY"), "alipay"}, stream{w.accountA(), w.site(w.siteA, "CNY"), "wxpay"}, stream{w.accountB(), w.site(w.siteB, "CNY"), "alipay"}
	for _, pair := range [][2]stream{{a, b}, {a, aw}} {
		last := map[string]uuid.UUID{}
		for range 4 {
			for _, s := range pair {
				chosen, key := w.order(s), s.site.ID.TypeID()+"/"+string(s.method)
				if last[key] == chosen {
					t.Fatalf("%s on %s went to the same merchant twice in a row", s.method, s.site.ID.TypeID())
				}
				last[key] = chosen
			}
		}
	}
}

// stream is the orders of one method on one site.
type stream struct {
	scope  pg.Scope
	site   accounts.Site
	method Method
}

// order shows the top-up page, places an order, checks that the page showed the merchant the order went to, and
// returns that merchant.
func (w *world) order(s stream) uuid.UUID {
	w.t.Helper()
	options, err := w.p.Options(w.t.Context(), s.scope, s.site, 100*yuan)
	if err != nil {
		w.t.Fatal(err)
	}
	c, err := w.choose(s.scope, s.site, s.method, 100*yuan)
	if err != nil {
		w.t.Fatal(err)
	}
	if i := slices.IndexFunc(options, func(o Option) bool { return o.Method == s.method }); i < 0 || options[i].Total != c.quote.Total {
		w.t.Errorf("the page showed %v, the order went for %v", options, c.quote.Total)
	}
	return c.offer.MerchantID
}

// TestLeastUsedComparesInTheSiteCurrency: today's usage is 180.00 USD, 25000 JPY (166.67 USD at 150) and 1000.00 CNY
// (138.89 USD at 7.2). The least used is the CNY merchant, though its usage has the most minor units; the page shows it
// and the order goes to it, priced in CNY.
func TestLeastUsedComparesInTheSiteCurrency(t *testing.T) {
	w := newWorld(t)
	w.open(w.siteA, LeastUsed)
	w.setRate("USD", "JPY", "150", "1")
	w.setRate("USD", "CNY", "36", "5")
	used := map[string]int64{"USD": 18_000, "JPY": 25_000, "CNY": 100_000}
	ids := map[string]uuid.UUID{}
	for _, code := range []string{"USD", "JPY", "CNY"} {
		in := w.input(w.siteA)
		in.Currency = currency(t, code)
		ids[code] = w.mustSave(siteScope(w.siteA), in).ID
		w.exec(fmt.Sprintf("INSERT INTO payments.merchant_usage VALUES ('%s', '%s', %s, %d)", ids[code], w.siteA, today, used[code]))
	}
	usd := w.site(w.siteA, "USD")
	// 10 USD at 7.2 with the 2% buffer is 73.44 CNY, 75.72 with the 3% fee.
	options, err := w.p.Options(t.Context(), w.accountA(), usd, 10*yuan)
	if err != nil || fmt.Sprint(options) != "[{alipay {CNY 7572} 228}]" {
		t.Fatalf("options = %v, %v", options, err)
	}
	if c, err := w.choose(w.accountA(), usd, "alipay", 10*yuan); err != nil || c.offer.MerchantID != ids["CNY"] || c.quote.Total.Minor != 7572 {
		t.Fatalf("chose %+v, %v; want the CNY merchant", c, err)
	}
}

// twoSites saves a merchant on each site and gives site B's some usage today; account A has also forged a
// registration of itself on site B, which its scope lets it write.
func twoSites(t *testing.T) (w *world, a, b Merchant) {
	w = newWorld(t)
	w.open(w.siteA, RoundRobin)
	a = w.mustSave(siteScope(w.siteA), w.input(w.siteA))
	b = w.mustSave(siteScope(w.siteB), w.input(w.siteB))
	w.exec(fmt.Sprintf("INSERT INTO payments.merchant_usage VALUES ('%s', '%s', %s, 5)", b.ID, w.siteB, today))
	err := w.tx(w.accountA(), func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "INSERT INTO platform.tenants VALUES ($1, $2)", w.siteB, w.account)
		return err
	})
	if err != nil {
		t.Fatalf("forge a registration: %v", err)
	}
	return w, a, b
}

func TestRowLevelSecurityReadsTheSiteOnly(t *testing.T) {
	w, _, b := twoSites(t)
	for query, want := range map[string]int{
		"SELECT count(*) FROM payments.merchants":        1,
		"SELECT count(*) FROM payments.merchant_methods": 1,
		"SELECT count(*) FROM payments.merchant_configs": 0,
		"SELECT count(*) FROM payments.merchant_usage":   0,
		"SELECT count(*) FROM payments.settings":         1,
	} {
		if n := w.count(w.accountA(), query); n != want {
			t.Errorf("account of A: %s = %d, want %d", query, n, want)
		}
	}
	if n := w.count(pg.NewScope(false, nil, []uuid.UUID{w.account}), "SELECT count(*) FROM payments.merchants"); n != 0 {
		t.Errorf("an account scope without its site reads %d merchants", n)
	}
	if n := w.count(siteScope(w.siteA), "SELECT count(*) FROM payments.merchants WHERE id = $1", b.ID); n != 0 {
		t.Errorf("site A reads site B's merchant")
	}
	in := w.input(w.siteB)
	in.ID, in.Rev = MerchantID(b.ID), b.Rev
	if _, err := w.save(siteScope(w.siteA), in); !errors.Is(err, ErrNotFound) {
		t.Errorf("site A changed site B's merchant: %v", err)
	}
	if _, err := w.save(w.accountA(), w.input(w.siteA)); err == nil {
		t.Error("an account created a merchant")
	}
	if _, err := w.p.Options(t.Context(), w.accountA(), w.site(w.siteB, "CNY"), 10*yuan); !errors.Is(err, ErrClosed) {
		t.Errorf("account A sees site B's top-ups: %v", err)
	}
}

func quoted(id uuid.UUID) string { return "'" + id.String() + "'::uuid" }

func TestRowLevelSecurityKeepsWritesOnTheSite(t *testing.T) {
	w, a, b := twoSites(t)
	// $1 is A's merchant, $2 B's, $3 site A, $4 site B.
	ids := strings.NewReplacer("$1", quoted(a.ID), "$2", quoted(b.ID), "$3", quoted(w.siteA), "$4", quoted(w.siteB))
	run := func(statement string) (changed int64, err error) {
		err = w.tx(w.accountA(), func(tx pg.Tx) error {
			tag, err := tx.Exec(t.Context(), ids.Replace(statement))
			changed = tag.RowsAffected()
			return err
		})
		return changed, err
	}
	for _, statement := range []string{ // outside the policies: no row found
		"UPDATE payments.merchants SET name = 'x' WHERE id = $1",
		"UPDATE payments.merchants SET name = 'x' WHERE id = $2",
		"UPDATE payments.merchant_usage SET used = 0 WHERE merchant_id = $2",
	} {
		if changed, err := run(statement); changed != 0 || err != nil {
			t.Errorf("%s: changed %d, %v", statement, changed, err)
		}
	}
	for statement, state := range map[string]string{
		"DELETE FROM payments.merchant_usage WHERE merchant_id = $1": "42501", // no DELETE
		"INSERT INTO payments.merchants (id, site_id, kind, name, enabled, currency, ttl_seconds, refund_enabled, user_refund, sort, rev) " +
			"VALUES (gen_random_uuid(), $3, 'fake', 'x', true, 'CNY', 60, false, false, 0, 1)": "42501",
		"INSERT INTO payments.merchant_usage VALUES ($2, $4, " + today + " + 1, 1)": "42501", // B's row
		"INSERT INTO payments.merchant_usage VALUES ($2, $3, " + today + " + 1, 1)": "23503", // B's merchant as A's
		"INSERT INTO payments.merchant_usage AS u VALUES ($2, $3, " + today + ", 1) " +
			"ON CONFLICT (merchant_id, day) DO UPDATE SET used = u.used + 1": "42501",
		"INSERT INTO payments.merchant_usage AS u VALUES ($2, $4, " + today + ", 1) " +
			"ON CONFLICT (merchant_id, day) DO UPDATE SET used = u.used + 1": "42501",
	} {
		if _, err := run(statement); pg.SQLState(err) != state {
			t.Errorf("%s: %v, want SQLSTATE %s", statement, err, state)
		}
	}
	if used := w.used(b.ID); used != 5 {
		t.Errorf("B's usage is %d, want 5", used)
	}
	// The account's own site's usage it does add to, as an order does.
	if _, err := w.choose(w.accountA(), w.site(w.siteA, "CNY"), "alipay", yuan); err != nil {
		t.Fatal(err)
	}
}

func TestOptions(t *testing.T) {
	w := newWorld(t)
	site := w.site(w.siteA, "USD")
	if _, err := w.p.Options(t.Context(), w.accountA(), site, 10*yuan); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed by default: %v", err)
	}
	w.exec(fmt.Sprintf("INSERT INTO payments.settings (site_id, open, credits_min, credits_max) VALUES ('%s', true, %d, %d)", w.siteA, yuan, 1000*yuan))
	usd := w.input(w.siteA, MethodInput{Method: "alipay", Upstream: "a", Fee: Fee{PPM: 30_000}, Enabled: true})
	usd.Currency = currency(t, "USD")
	w.mustSave(siteScope(w.siteA), usd)
	w.mustSave(siteScope(w.siteA), w.input(w.siteA, MethodInput{Method: "wxpay", Upstream: "w", Enabled: true})) // CNY, no rate yet
	options, err := w.p.Options(t.Context(), w.accountA(), site, 100*yuan)
	if err != nil || fmt.Sprint(options) != "[{alipay {USD 10310} 310}]" {
		t.Fatalf("options = %v, %v", options, err)
	}
	w.setRate("USD", "CNY", "36", "5")
	// 100 USD at 7.2 with the default 2% buffer is 734.40 CNY.
	options, err = w.p.Options(t.Context(), w.accountA(), site, 100*yuan)
	if err != nil || fmt.Sprint(options) != "[{alipay {USD 10310} 310} {wxpay {CNY 73440} 0}]" {
		t.Fatalf("options = %v, %v", options, err)
	}
	for _, credits := range []int64{yuan - 1, 1000*yuan + 1} {
		if _, err := w.p.Options(t.Context(), w.accountA(), site, credits); !errors.Is(err, ErrCreditsRange) {
			t.Errorf("Options(%d) = %v", credits, err)
		}
	}
	// An account of site B sees nothing of site A's, so its top-ups there are closed.
	if _, err := w.p.Options(t.Context(), w.accountB(), site, 100*yuan); !errors.Is(err, ErrClosed) {
		t.Errorf("another site's account: %v", err)
	}
}

// TestPricingUsesTheBufferedRate checks the rate an order would lock: the reference version and the buffer on it.
func TestPricingUsesTheBufferedRate(t *testing.T) {
	w := newWorld(t)
	w.setRate("CNY", "USD", "5", "36")
	offers := []OffersRow{{Currency: "CNY"}, {Currency: "EUR"}, {Currency: "USD"}, {Currency: "CNY"}}
	read := func() pricing {
		var pr pricing
		err := w.tx(pg.NewScope(true, nil, nil), func(tx pg.Tx) (err error) {
			pr, err = readPricing(t.Context(), tx, w.siteA, currency(t, "USD"), offers)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return pr
	}
	if pr := read(); len(pr.rates) != 1 || pr.rates["CNY"].Applied.Cmp(big.NewRat(36*102, 5*100)) != 0 || pr.rates["CNY"].Reference.Version != 1 {
		t.Fatalf("pricing = %+v", pr)
	}
	// A site that set its buffer to 0 pays the reference rate.
	w.exec(fmt.Sprintf("INSERT INTO billing.top_up_buffers VALUES ('%s', 0)", w.siteA))
	if pr := read(); pr.rates["CNY"].Applied.Cmp(big.NewRat(36, 5)) != 0 || pr.rates["CNY"].BufferPPM != 0 {
		t.Fatalf("pricing without a buffer = %+v", pr)
	}
}
