package payments

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

// sameCurrency prices CNY credits in CNY.
func sameCurrency(t *testing.T) pricing { return pricing{site: currency(t, "CNY")} }

func (w *world) choose(s pg.Scope, method Method, credits int64) (candidate, error) {
	var c candidate
	err := w.tx(s, func(tx pg.Tx) (err error) {
		c, err = w.p.choose(w.t.Context(), tx, w.siteA, sameCurrency(w.t), RoundRobin, method, credits)
		return err
	})
	return c, err
}

func (w *world) used(merchant uuid.UUID) int64 {
	var used int64
	err := w.tx(siteScope(w.siteA), func(tx pg.Tx) error {
		return tx.QueryRow(w.t.Context(), "SELECT coalesce(sum(used), 0) FROM payments.merchant_usage WHERE merchant_id = $1", merchant).Scan(&used)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return used
}

func TestChooseRefusesWhatNoMerchantTakes(t *testing.T) {
	w := newWorld(t)
	limits := MethodInput{Method: "alipay", Upstream: "alipay", SingleMin: new(int64(500)), SingleMax: new(int64(10_000)), DailyLimit: new(int64(20_000)), Enabled: true}
	m := w.mustSave(siteScope(w.siteA), w.input(w.siteA, limits))
	account := accountScope(w.accountA)
	for name, c := range map[string]struct {
		method  Method
		credits int64
	}{
		"below the single minimum": {"alipay", 4 * yuan},
		"above the single maximum": {"alipay", 101 * yuan},
		"no merchant has it":       {"wxpay", 10 * yuan},
	} {
		if _, err := w.choose(account, c.method, c.credits); !errors.Is(err, ErrNoMerchant) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for range 2 {
		if _, err := w.choose(account, "alipay", 100*yuan); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.choose(account, "alipay", 5*yuan); !errors.Is(err, ErrNoMerchant) || w.used(m.ID) != 20_000 {
		t.Fatalf("over the daily limit: %v, used %d", err, w.used(m.ID))
	}
	w.mustSave(siteScope(w.siteA), w.input(w.siteA)) // a second merchant without limits takes it
	if c, err := w.choose(account, "alipay", 5*yuan); err != nil || c.offer.MerchantID == m.ID {
		t.Fatalf("with another merchant: %+v, %v", c, err)
	}
	w.exec("REVOKE SELECT ON payments.merchant_usage FROM axiom_app")
	if _, err := w.choose(account, "alipay", 5*yuan); err == nil || errors.Is(err, ErrNoMerchant) {
		t.Fatalf("usage unreadable: %v", err)
	}
}

// TestChooseGivesTheLastQuotaOnlyToWhatFits has ten orders grab a daily limit that fits three, each in its own
// account-scoped transaction, all at once.
func TestChooseGivesTheLastQuotaOnlyToWhatFits(t *testing.T) {
	w := newWorld(t)
	m := w.mustSave(siteScope(w.siteA), w.input(w.siteA, MethodInput{Method: "alipay", Upstream: "a", DailyLimit: new(int64(3 * 1000)), Enabled: true}))
	start, done := make(chan struct{}), make(chan error, 10)
	for range 10 {
		go func() {
			<-start
			_, err := w.choose(accountScope(w.accountA), "alipay", 10*yuan)
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

// TestChooseBoundsTheWaitForTheUsageRow holds the usage row in one transaction: another order gives up at the lock
// timeout instead of queueing behind it.
func TestChooseBoundsTheWaitForTheUsageRow(t *testing.T) {
	w := newWorld(t)
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
	_, err := w.choose(accountScope(w.accountA), "alipay", yuan)
	close(release)
	if pg.SQLState(err) != "55P03" {
		t.Fatalf("waited for the row: %v", err)
	}
	if err = <-holder; err != nil {
		t.Fatal(err)
	}
}

// twoSites saves a merchant on each site and gives site B's some usage today.
func twoSites(t *testing.T) (w *world, a, b Merchant) {
	w = newWorld(t)
	a = w.mustSave(siteScope(w.siteA), w.input(w.siteA))
	b = w.mustSave(siteScope(w.siteB), w.input(w.siteB))
	w.exec(fmt.Sprintf("INSERT INTO payments.merchant_usage VALUES ('%s', '%s', current_date, 5)", b.ID, w.siteB))
	return w, a, b
}

func (w *world) count(s pg.Scope, query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.tx(s, func(tx pg.Tx) error { return tx.QueryRow(w.t.Context(), query, args...).Scan(&n) }); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestRowLevelSecurityReadsTheSiteOnly(t *testing.T) {
	w, _, b := twoSites(t)
	account := accountScope(w.accountA)
	for query, want := range map[string]int{
		"SELECT count(*) FROM payments.merchants":        1,
		"SELECT count(*) FROM payments.merchant_methods": 1,
		"SELECT count(*) FROM payments.merchant_configs": 0,
		"SELECT count(*) FROM payments.merchant_usage":   0,
	} {
		if n := w.count(account, query); n != want {
			t.Errorf("account of A: %s = %d, want %d", query, n, want)
		}
	}
	if n := w.count(siteScope(w.siteA), "SELECT count(*) FROM payments.merchants WHERE id = $1", b.ID); n != 0 {
		t.Errorf("site A reads site B's merchant")
	}
	in := w.input(w.siteB)
	in.ID, in.Rev = MerchantID(b.ID), b.Rev
	if _, err := w.save(siteScope(w.siteA), in); !errors.Is(err, ErrNotFound) {
		t.Errorf("site A changed site B's merchant: %v", err)
	}
	if _, err := w.save(account, w.input(w.siteA)); err == nil {
		t.Error("an account created a merchant")
	}
}

func TestRowLevelSecurityKeepsWritesOnTheSite(t *testing.T) {
	w, a, b := twoSites(t)
	account := accountScope(w.accountA)
	// $1 is A's merchant, $2 B's, $3 site A, $4 site B.
	ids := strings.NewReplacer("$1", quoted(a.ID), "$2", quoted(b.ID), "$3", quoted(w.siteA), "$4", quoted(w.siteB))
	run := func(statement string) (changed int64, err error) {
		err = w.tx(account, func(tx pg.Tx) error {
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
		"INSERT INTO payments.merchant_usage VALUES ($2, $4, current_date + 1, 1)": "42501", // B's row
		"INSERT INTO payments.merchant_usage VALUES ($2, $3, current_date + 1, 1)": "23503", // B's merchant as A's
		"INSERT INTO payments.merchant_usage AS u VALUES ($2, $3, current_date, 1) " +
			"ON CONFLICT (merchant_id, day) DO UPDATE SET used = u.used + 1": "42501",
		"INSERT INTO payments.merchant_usage AS u VALUES ($2, $4, current_date, 1) " +
			"ON CONFLICT (merchant_id, day) DO UPDATE SET used = u.used + 1": "42501",
	} {
		if _, err := run(statement); pg.SQLState(err) != state {
			t.Errorf("%s: %v, want SQLSTATE %s", statement, err, state)
		}
	}
	if n := w.count(pg.NewScope(true, nil, nil), "SELECT used FROM payments.merchant_usage WHERE merchant_id = $1", b.ID); n != 5 {
		t.Errorf("B's usage is %d, want 5", n)
	}
	// The account's own site's usage it does add to, as an order does.
	if _, err := w.choose(account, "alipay", yuan); err != nil {
		t.Fatal(err)
	}
}

func quoted(id uuid.UUID) string { return "'" + id.String() + "'::uuid" }

func TestOptions(t *testing.T) {
	w := newWorld(t)
	site := accounts.Site{ID: accounts.SiteID(w.siteA), Currency: currency(t, "USD")}
	account := accountScope(w.accountA)
	if _, err := w.p.Options(t.Context(), account, site, 10*yuan); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed by default: %v", err)
	}
	w.exec(fmt.Sprintf("INSERT INTO payments.settings (site_id, open, credits_min, credits_max) VALUES ('%s', true, %d, %d)", w.siteA, yuan, 1000*yuan))
	usd := w.input(w.siteA, MethodInput{Method: "alipay", Upstream: "a", Fee: Fee{PPM: 30_000}, Enabled: true})
	usd.Currency = currency(t, "USD")
	w.mustSave(siteScope(w.siteA), usd)
	w.mustSave(siteScope(w.siteA), w.input(w.siteA, MethodInput{Method: "wxpay", Upstream: "w", Enabled: true})) // CNY, no rate yet
	options, err := w.p.Options(t.Context(), account, site, 100*yuan)
	if err != nil || fmt.Sprint(options) != "[{alipay {USD 10310} 310}]" {
		t.Fatalf("options = %v, %v", options, err)
	}
	err = w.tx(pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		_, err := billing.SetExchangeRate(t.Context(), tx, admin, billing.ExchangeInput{Base: "USD", Quote: "CNY", Numerator: "36", Denominator: "5", Kind: "initial", Reason: "test"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// 100 USD at 7.2 with the default 2% buffer is 734.40 CNY.
	options, err = w.p.Options(t.Context(), account, site, 100*yuan)
	if err != nil || fmt.Sprint(options) != "[{alipay {USD 10310} 310} {wxpay {CNY 73440} 0}]" {
		t.Fatalf("options = %v, %v", options, err)
	}
	for _, credits := range []int64{yuan - 1, 1000*yuan + 1} {
		if _, err := w.p.Options(t.Context(), account, site, credits); !errors.Is(err, ErrCreditsRange) {
			t.Errorf("Options(%d) = %v", credits, err)
		}
	}
	// An account of site B sees nothing of site A's, so its top-ups there are closed.
	if _, err := w.p.Options(t.Context(), accountScope(w.acctB), site, 100*yuan); !errors.Is(err, ErrClosed) {
		t.Errorf("another site's account: %v", err)
	}
}

// TestPricingUsesTheBufferedRate checks the rate an order would lock: the reference version and the buffer on it.
func TestPricingUsesTheBufferedRate(t *testing.T) {
	w := newWorld(t)
	err := w.tx(pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		_, err := billing.SetExchangeRate(t.Context(), tx, admin, billing.ExchangeInput{Base: "CNY", Quote: "USD", Numerator: "5", Denominator: "36", Kind: "initial", Reason: "test"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := w.p.pricing(t.Context(), w.siteA, currency(t, "USD"), []string{"CNY", "EUR", "USD", "CNY"})
	if err != nil || len(pr.rates) != 1 || pr.rates["CNY"].Applied.Cmp(big.NewRat(36*102, 5*100)) != 0 || pr.rates["CNY"].Reference.Version != 1 {
		t.Fatalf("pricing = %+v, %v", pr, err)
	}
}
