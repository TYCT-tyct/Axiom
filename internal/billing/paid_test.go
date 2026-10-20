package billing

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

func currency(tb testing.TB, code string) platform.Currency {
	tb.Helper()
	c, err := platform.ParseCurrency(code)
	if err != nil {
		tb.Fatal(err)
	}
	return c
}

// cnyUSD is 1 USD = 7.1 CNY as billing.exchange_rates keeps it: 1 CNY = 10/71 USD, a rate no decimal ends.
var cnyUSD = BillingExchangeRate{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Version: 1, Numerator: 10, Denominator: 71}

// TestCreditsFor: the credit a payment buys is what was paid less the fee, converted exactly and rounded down once, in
// the platform's favour; a payment the ledger cannot hold, in a currency it does not know or without the rate it needs
// buys nothing.
func TestCreditsFor(t *testing.T) {
	t.Parallel()
	usd, cny, jpy := currency(t, "USD"), currency(t, "CNY"), currency(t, "JPY")
	for _, tc := range []struct {
		name string
		p    Payment
		site platform.Currency
		rate *BillingExchangeRate
		want int64 // -1: an error
	}{
		{"one currency", Payment{usd, 1000, 30}, usd, nil, 9_700_000_000},
		{"yen have no minor unit", Payment{jpy, 1000, 0}, jpy, nil, 1000_000_000_000},
		{"rounded down", Payment{cny, 7400, 0}, usd, &cnyUSD, 10_422_535_211}, // 74 × 10/71 = 10.42253521126…
		{"the fee is not converted", Payment{cny, 7400, 300}, usd, &cnyUSD, 10_000_000_000},
		{"the reciprocal", Payment{usd, 100, 0}, cny, &cnyUSD, 7_100_000_000},
		{"no rate across currencies", Payment{cny, 7400, 0}, usd, nil, -1},
		{"a rate of another pair", Payment{jpy, 100, 0}, usd, &cnyUSD, -1},
		{"an unknown currency", Payment{currency(t, "BTC"), 100, 0}, usd, nil, -1},
		{"a fee of all of it", Payment{usd, 100, 100}, usd, nil, -1},
		{"nothing paid", Payment{usd, 0, 0}, usd, nil, -1},
		{"more than the ledger holds", Payment{usd, math.MaxInt64 / 10_000_000, 0}, usd, nil, math.MaxInt64 / 10_000_000 * 10_000_000},
		{"beyond it", Payment{usd, math.MaxInt64/10_000_000 + 1, 0}, usd, nil, -1},
		{"bought beyond it", Payment{usd, math.MaxInt64 / 10_000_000, 0}, cny, &cnyUSD, -1},
	} {
		got, err := CreditsFor(tc.p, tc.site, tc.rate)
		if tc.want < 0 && !errors.As(err, new(*PaidError)) || tc.want >= 0 && (err != nil || got != tc.want) {
			t.Errorf("%s: CreditsFor = %d, %v; want %d", tc.name, got, err, tc.want)
		}
	}
}

// TestMinorUnits: an amount in billionths is a payment only in whole minor units of its currency.
func TestMinorUnits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		code       string
		billionths int64
		want       int64 // -1: an error
	}{
		{"USD", 1_230_000_000, 123},
		{"USD", 1_230_000_001, -1},
		{"JPY", 5_000_000_000, 5},
		{"JPY", 500_000_000, -1},
		{"XAU", 1_000_000_000, -1},
	} {
		got, err := MinorUnits(currency(t, tc.code), tc.billionths)
		if tc.want < 0 && err == nil || tc.want >= 0 && (err != nil || got != tc.want) {
			t.Errorf("MinorUnits(%s, %d) = %d, %v; want %d", tc.code, tc.billionths, got, err, tc.want)
		}
	}
}

// rate keeps 1 USD = 7.1 CNY in the bank's database and returns its version.
func (b *bank) rate() BillingExchangeRate {
	b.t.Helper()
	var row BillingExchangeRate
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		row, err = SetExchangeRate(b.t.Context(), tx, audit.System("billing.test"), fxInput("71", "10", "initial", 0))
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return row
}

// paid is a payment received offline into the bank's account, whose site counts in USD.
func (b *bank) paid(credited int64, p Payment, rate uuid.UUID) PaidCredit {
	return PaidCredit{
		ID: uuid.NewV7(), Site: b.admitted.Site, Account: b.admitted.Account, SiteCurrency: currency(b.t, "USD"),
		Credited: credited, Payment: p, Channel: Channel{Offline: "bank transfer"}, RateID: rate,
		Actor: audit.System("billing.test"), Reason: "paid by bank transfer",
	}
}

func (b *bank) creditPaid(p PaidCredit) error {
	return b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) error { return CreditPaid(b.t.Context(), tx, p) })
}

func (b *bank) ledgerOf(id uuid.UUID) []LedgerTransaction {
	b.t.Helper()
	var l []LedgerTransaction
	if err := b.app.ReadTx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		l, err = LedgerOf(b.t.Context(), tx, id)
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return l
}

// TestCreditPaidBooks: a top-up debits cash with what was paid and credits fee income with the fee, in the payment's
// currency, and credits the user's balance in the site's; the exchange gain or loss balances each currency on its own,
// and in one currency takes only what the credit left of a minor unit. The batch keeps what was paid and the audit
// entry says so.
func TestCreditPaidBooks(t *testing.T) {
	t.Parallel()
	usd, cny := currency(t, "USD"), currency(t, "CNY")
	for _, tc := range []struct {
		name     string
		credited int64
		payment  Payment
		cross    bool
		want     []LedgerEntry
	}{
		{"one currency", 9_700_000_000, Payment{usd, 1000, 30}, false, []LedgerEntry{
			{bookCash, 10_000_000_000, ""}, {bookFeeIncome, -300_000_000, ""}, {bookUserBalance, -9_700_000_000, ""},
		}},
		{"a credit short of a cent", 9_699_999_999, Payment{usd, 1000, 30}, false, []LedgerEntry{
			{bookCash, 10_000_000_000, ""}, {bookExchange, -1, ""}, {bookFeeIncome, -300_000_000, ""}, {bookUserBalance, -9_699_999_999, ""},
		}},
		{"across currencies", 10_000_000_000, Payment{cny, 7400, 300}, true, []LedgerEntry{
			{bookExchange, 10_000_000_000, ""},
			{bookUserBalance, -10_000_000_000, ""},
			{bookCash, 74_000_000_000, "CNY"},
			{bookExchange, -71_000_000_000, "CNY"},
			{bookFeeIncome, -3_000_000_000, "CNY"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBank(t, 0, 0, Config{})
			var rate uuid.UUID
			if tc.cross {
				rate = b.rate().ID
			}
			p := b.paid(tc.credited, tc.payment, rate)
			if err := b.creditPaid(p); err != nil {
				t.Fatal(err)
			}
			b.checkPaid(p, tc.want)
		})
	}
}

// checkPaid checks what p left: its ledger transaction with entries want, the balance it alone made, its batch, and an
// audit entry that says what was paid and where.
func (b *bank) checkPaid(p PaidCredit, want []LedgerEntry) {
	b.t.Helper()
	l := b.ledgerOf(p.ID)
	if len(l) != 1 || l[0].Kind != "credit" || !slices.Equal(l[0].Entries, want) {
		b.t.Errorf("the ledger: %+v", l)
	}
	if n := b.count("SELECT balance FROM billing.balances"); n != p.Credited {
		b.t.Errorf("balance %d, want %d", n, p.Credited)
	}
	if got, ok := b.readCredit(b.platform, p.ID); !ok || !p.recordedBy(got) {
		b.t.Errorf("the batch: %+v", got)
	}
	detail := fmt.Sprintf(`{"source": "paid", "amount": %d, "currency": %q, "paid": %d, "fee": %d, "channel": "offline: bank transfer"}`,
		p.Credited, p.Payment.Currency, p.Payment.Paid, p.Payment.Fee)
	if n := b.count("SELECT count(*) FROM platform.audit_log WHERE action = 'credit.add' AND detail @> '" + detail + "'"); n != 1 {
		b.t.Errorf("%d audit entries with %s", n, detail)
	}
	b.balanced()
	b.replayed(b.admitted.Account)
}

func (p PaidCredit) recordedBy(got CreditBatch) bool {
	return got.Source == "paid" && got.Amount == p.Credited && got.Payment != nil && *got.Payment == p.Payment &&
		got.Channel == p.Channel && got.RateID == p.RateID
}

// TestCreditPaidOnce: the same credit again changes nothing; the same ID with any other amount, channel or rate, or an
// ID a gift took, is ErrCreditConflict and writes nothing.
func TestCreditPaidOnce(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	usd, cny := currency(t, "USD"), currency(t, "CNY")
	rate := b.rate().ID
	p := b.paid(10_000_000_000, Payment{cny, 7400, 300}, rate)
	for range 2 {
		if err := b.creditPaid(p); err != nil {
			t.Fatal(err)
		}
	}
	gift, other := b.creditBatch(5, nil), uuid.NewV7()
	if err := b.app.Tx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{other}), func(tx pg.Tx) error {
		return OpenBalance(t.Context(), tx, b.admitted.Site, other, 0)
	}); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*PaidCredit){
		"credited": func(q *PaidCredit) { q.Credited-- },
		"paid":     func(q *PaidCredit) { q.Payment.Paid++ },
		"fee":      func(q *PaidCredit) { q.Payment.Fee-- },
		"currency": func(q *PaidCredit) {
			q.Payment, q.Credited, q.RateID = Payment{usd, 1000, 0}, 10_000_000_000, uuid.UUID{}
		},
		"channel": func(q *PaidCredit) { q.Channel = Channel{Merchant: uuid.NewV7()} },
		"account": func(q *PaidCredit) { q.Account = other },
		"gift":    func(q *PaidCredit) { q.ID = gift },
	} {
		q := p
		change(&q)
		if err := b.creditPaid(q); !errors.Is(err, ErrCreditConflict) {
			t.Errorf("%s: CreditPaid = %v, want ErrCreditConflict", name, err)
		}
	}
	if n := b.count("SELECT balance FROM billing.balances"); n != 10_000_000_005 {
		t.Errorf("balance %d, want %d", n, 10_000_000_005)
	}
	if n := b.count("SELECT count(*) FROM platform.audit_log WHERE action = 'credit.add'"); n != 2 {
		t.Errorf("%d audit entries, want 2", n)
	}
	b.balanced()
}

// TestCreditPaidRefusals: a credit beyond what the payment buys, a payment across currencies without its rate or with
// another pair's, and a channel that is neither or both write nothing.
func TestCreditPaidRefusals(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	usd, cny, eur := currency(t, "USD"), currency(t, "CNY"), currency(t, "EUR")
	rate := b.rate().ID
	for name, p := range map[string]PaidCredit{
		"more than it buys":   b.paid(10_422_535_212, Payment{cny, 7400, 0}, rate),
		"more than was paid":  b.paid(10_000_000_001, Payment{usd, 1000, 0}, uuid.UUID{}),
		"nothing":             b.paid(0, Payment{usd, 1000, 0}, uuid.UUID{}),
		"no rate":             b.paid(1, Payment{cny, 7400, 0}, uuid.UUID{}),
		"another pair's rate": b.paid(1, Payment{eur, 7400, 0}, rate),
		"a rate in one":       b.paid(1, Payment{usd, 7400, 0}, rate),
		"a rate nobody set":   b.paid(1, Payment{cny, 7400, 0}, uuid.NewV7()),
		"no channel":          func() PaidCredit { p := b.paid(1, Payment{usd, 100, 0}, uuid.UUID{}); p.Channel = Channel{}; return p }(),
		"both channels": func() PaidCredit {
			p := b.paid(1, Payment{usd, 100, 0}, uuid.UUID{})
			p.Channel.Merchant = uuid.NewV7()
			return p
		}(),
		"a fee beyond payment": b.paid(1, Payment{usd, 100, 100}, uuid.UUID{}),
	} {
		if err := b.creditPaid(p); !errors.As(err, new(*PaidError)) {
			t.Errorf("%s: CreditPaid = %v, want a PaidError", name, err)
		}
	}
	if n := b.count("SELECT count(*) FROM billing.credit_batches"); n != 0 {
		t.Errorf("%d batches were written", n)
	}
}

// TestPostBalancesEachCurrency: entries that sum to zero only across currencies are refused when posted, and found by
// the reconciliation if they were ever written.
func TestPostBalancesEachCurrency(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	lines := []line{{bookCash, 100, "CNY"}, {bookUserBalance, -100, siteCredits}}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("post took entries that balance only across currencies")
			}
		}()
		err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
			_, err := post(t.Context(), tx, event{site: b.admitted.Site, account: b.admitted.Account, kind: "credit", request: uuid.NewV7()}, lines...)
			return err
		})
		t.Errorf("post returned %v", err)
	}()
	id := uuid.NewV7()
	b.sql(fmt.Sprintf(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id) VALUES ('%[1]s', '%[2]s', '%[3]s', 'credit', '%[1]s');
		INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount, currency) VALUES
			('%[1]s', '%[2]s', '%[3]s', 'cash', 100, 'CNY'), ('%[1]s', '%[2]s', '%[3]s', 'exchange_gain_loss', -100, NULL)`,
		id, b.admitted.Site, b.admitted.Account))
	if problems, err := Reconcile(t.Context(), b.app, b.platform); err != nil || !slices.Equal(problems, []string{"transaction " + id.String()}) {
		t.Errorf("Reconcile = %v, %v", problems, err)
	}
}

// randomCredit adds amount to the account as a gift, as a payment in the site's currency (a cent buys it, the rest an
// exchange gain), or as one in CNY at the bank's rate, which TestLedgerProperty mixes with its charges and expiries.
func (b *bank) randomCredit(r *rand.Rand, amount int64, expires *time.Time) {
	b.t.Helper()
	var p PaidCredit
	switch r.IntN(3) {
	case 0:
		b.creditBatch(amount, expires)
		return
	case 1:
		p = b.paid(amount, Payment{currency(b.t, "USD"), 1, 0}, uuid.UUID{})
	default:
		var rate BillingExchangeRate
		if err := b.app.ReadTx(b.t.Context(), b.platform, func(tx pg.Tx) error {
			rows, err := ExchangeRateHistory(b.t.Context(), tx, "CNY", "USD", 0)
			if len(rows) > 0 {
				rate = rows[0]
			}
			return err
		}); err != nil {
			b.t.Fatal(err)
		}
		if rate.ID == (uuid.UUID{}) {
			rate = b.rate()
		}
		p = b.paid(amount, Payment{currency(b.t, "CNY"), 2, 1}, rate.ID)
	}
	p.ExpiresAt = expires
	if err := b.creditPaid(p); err != nil {
		b.t.Fatal(err)
	}
}
