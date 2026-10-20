package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// PaidCredit is credit a user paid for (docs/domains/billing.md「额度批次」「账本」的「用户充值」): through a merchant
// account of payments, or offline, which an administrator records. ID makes it idempotent: the order's ID, or the
// pending record's. The ledger books the cash in the payment's currency and the credit in the site's, so nothing paid
// is converted in the books; RateID is the reference exchange rate version the payment was converted at, zero exactly
// when the payment is in the site's currency.
type PaidCredit struct {
	ID, Site, Account uuid.UUID
	SiteCurrency      platform.Currency // the currency of the site's credits, which the caller knows from the site
	Credited          int64             // billionths of a credit, positive, at most CreditsFor(Payment, SiteCurrency, rate)
	Payment           Payment
	Channel           Channel
	RateID            uuid.UUID
	ExpiresAt         *time.Time
	Actor             audit.Actor
	Reason            string
}

// Payment is money received, in minor units of its currency (cents for USD, yen for JPY): Paid is what the user paid,
// Fee the part of it that is the channel's fee the user paid, 0 <= Fee < Paid.
type Payment struct {
	Currency  platform.Currency
	Paid, Fee int64
}

// Channel is where a payment was received: a merchant account of payments, or, for an offline payment, what the
// administrator names (a bank account, a wallet). Exactly one is set; the cash book is kept by it.
type Channel struct {
	Merchant uuid.UUID
	Offline  string
}

// ErrCreditConflict is a credit ID used before for other amounts: payments takes the payment to its pending records
// instead of crediting it twice or dropping the difference.
var ErrCreditConflict = errors.New("billing: the credit was made before with other amounts")

// PaidError is a PaidCredit, or a payment amount, that breaks a rule of paid credit.
type PaidError struct{ Reason string }

func (e *PaidError) Error() string { return "billing: paid credit " + e.Reason }

// unitScale is how many billionths of a unit of c make one of its minor units.
func unitScale(c platform.Currency) (int64, error) {
	d, ok := minorDigits[c.String()]
	if !ok {
		return 0, &PaidError{"in " + c.String() + ", which is not a supported currency"}
	}
	scale := int64(1)
	for range 9 - d {
		scale *= 10
	}
	return scale, nil
}

// MinorUnits is billionths of a unit of c in c's minor units: an amount the admin API gives in billionths, as all its
// money is, must be whole minor units to be a payment.
func MinorUnits(c platform.Currency, billionths int64) (int64, error) {
	scale, err := unitScale(c)
	if err != nil {
		return 0, err
	}
	if billionths%scale != 0 {
		return 0, &PaidError{"of a fraction of the minor unit of " + c.String()}
	}
	return billionths / scale, nil
}

// billionths is p's paid and fee amounts in billionths of a unit of its currency, the unit of the ledger.
func (p Payment) billionths() (paid, fee int64, err error) {
	scale, err := unitScale(p.Currency)
	if err != nil {
		return 0, 0, err
	}
	if p.Paid <= 0 || p.Fee < 0 || p.Fee >= p.Paid {
		return 0, 0, &PaidError{fmt.Sprintf("of %d with a fee of %d", p.Paid, p.Fee)}
	}
	if p.Paid > math.MaxInt64/scale {
		return 0, 0, &PaidError{fmt.Sprintf("of %d, more than the ledger holds", p.Paid)}
	}
	return p.Paid * scale, p.Fee * scale, nil
}

// CreditsFor is the most credit p buys in site credits: what was paid less the fee, at rate, which converts p's
// currency to site's (nil when they are the same), rounded down once, in the platform's favour
// (docs/domains/billing.md「钱的表示」).
func CreditsFor(p Payment, site platform.Currency, rate *BillingExchangeRate) (int64, error) {
	paid, fee, err := p.billionths()
	if err != nil {
		return 0, err
	}
	if rate == nil {
		if p.Currency != site {
			return 0, &PaidError{"in " + p.Currency.String() + " needs an exchange rate to " + site.String()}
		}
		return paid - fee, nil
	}
	r, ok := rate.Basis().Between(p.Currency, site)
	if !ok {
		return 0, &PaidError{"in " + p.Currency.String() + " was given a rate of " + rate.Base + "/" + rate.Quote}
	}
	n := new(big.Int).Mul(big.NewInt(paid-fee), r.Num())
	n.Quo(n, r.Denom()) // both positive, so truncation is the floor
	if !n.IsInt64() {
		return 0, &PaidError{"buys more credit than the ledger holds"}
	}
	return n.Int64(), nil
}

// CreditPaid records p in tx, which must be in the platform scope, as the audit log requires: the paid batch, the
// ledger transaction of the top-up, and the audit entry, all or none. The same ID again with the same amounts writes
// nothing; with others it is ErrCreditConflict, never a second credit or a silent drop.
//
// The entries (docs/domains/billing.md「账本」): cash is debited what was paid and fee income credited the fee, in the
// payment's currency; the user's balance is credited the credit, in the site's. The exchange gain or loss balances each
// currency on its own: across currencies it is credited the net paid in the payment's and debited the credit in the
// site's; in one currency it takes what rounding the credit to a minor unit left over, if anything.
func CreditPaid(ctx context.Context, tx pg.Tx, p PaidCredit) error {
	lines, rate, err := p.entries(ctx, tx)
	if err != nil {
		return err
	}
	detail := map[string]any{"currency": p.Payment.Currency.String(), "paid": p.Payment.Paid, "fee": p.Payment.Fee}
	if p.Channel.Offline != "" {
		detail["channel"] = "offline: " + p.Channel.Offline
	} else {
		detail["merchant"] = p.Channel.Merchant.String()
	}
	if rate != nil {
		detail["exchange_rate"] = map[string]any{
			"id": rate.ID.String(), "version": rate.Version, "base": rate.Base,
			"quote": rate.Quote, "numerator": rate.Numerator, "denominator": rate.Denominator,
		}
	}
	posted, err := addBatch(ctx, tx, p.batch(), lines, p.Actor, p.Reason, detail)
	if err != nil || posted {
		return err
	}
	return p.same(ctx, tx)
}

// entries checks p and returns the entries of its top-up, with the rate it was converted at (nil in one currency).
func (p PaidCredit) entries(ctx context.Context, tx pg.Tx) ([]line, *BillingExchangeRate, error) {
	if (p.Channel.Merchant == uuid.UUID{}) == (p.Channel.Offline == "") {
		return nil, nil, &PaidError{"names no channel, or both a merchant account and an offline channel"}
	}
	rate, err := readRate(ctx, tx, p.RateID)
	if err != nil {
		return nil, nil, err
	}
	most, err := CreditsFor(p.Payment, p.SiteCurrency, rate)
	if err != nil {
		return nil, nil, err
	}
	if p.Credited <= 0 || p.Credited > most {
		return nil, nil, &PaidError{fmt.Sprintf("credits %d, outside 1 to the %d it buys", p.Credited, most)}
	}
	paid, fee, err := p.Payment.billionths()
	if err != nil {
		return nil, nil, err
	}
	if rate == nil {
		return []line{
			{bookCash, paid, siteCredits},
			{bookFeeIncome, -fee, siteCredits},
			{bookExchange, p.Credited - (paid - fee), siteCredits},
			{bookUserBalance, -p.Credited, siteCredits},
		}, nil, nil
	}
	in := p.Payment.Currency.String()
	return []line{
		{bookCash, paid, in},
		{bookFeeIncome, -fee, in},
		{bookExchange, fee - paid, in},
		{bookExchange, p.Credited, siteCredits},
		{bookUserBalance, -p.Credited, siteCredits},
	}, rate, nil
}

// readRate reads the exchange rate version id, none for the zero ID.
func readRate(ctx context.Context, tx pg.Tx, id uuid.UUID) (*BillingExchangeRate, error) {
	if id == (uuid.UUID{}) {
		return nil, nil //nolint:nilnil // no rate is the answer for a payment in the site's currency
	}
	rows, err := queries.ExchangeRateByID(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("billing: read the exchange rate %s: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, &PaidError{"names an exchange rate " + id.String() + " that does not exist"}
	}
	return &rows[0], nil
}

// batch is the row p's batch is.
func (p PaidCredit) batch() InsertCreditBatchParams {
	b := InsertCreditBatchParams{
		ID: p.ID, SiteID: p.Site, AccountID: p.Account, Source: "paid", Amount: p.Credited, ExpiresAt: p.ExpiresAt,
		Currency: p.Payment.Currency.String(), Paid: &p.Payment.Paid, Fee: &p.Payment.Fee, OfflineChannel: p.Channel.Offline,
	}
	if p.Channel.Offline == "" {
		b.MerchantID = &p.Channel.Merchant
	}
	if p.RateID != (uuid.UUID{}) {
		b.RateID = &p.RateID
	}
	return b
}

// same answers p's ID posted before: nothing to do if its batch is p's, ErrCreditConflict if it is anything else. The
// expiry is left out: a retry may work it out again from a later clock.
func (p PaidCredit) same(ctx context.Context, tx pg.Tx) error {
	b, ok, err := ReadCredit(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	if ok && b.Account == p.Account && b.Site == p.Site && b.Source == "paid" && b.Amount == p.Credited &&
		b.Payment != nil && *b.Payment == p.Payment && b.Channel == p.Channel && b.RateID == p.RateID {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrCreditConflict, p.ID)
}
