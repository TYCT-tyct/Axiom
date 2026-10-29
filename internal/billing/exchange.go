package billing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// ExchangeInput defines 1 Base = Numerator/Denominator Quote. Text bounds are checked before big-number arithmetic.
type ExchangeInput struct {
	Base, Quote, Numerator, Denominator, Kind, Reason string
	ExpectedVersion                                   int64
}

// ExchangeError identifies an invalid input without echoing its value. An empty Field is a rule on the rate as a whole,
// which two fields together break.
type ExchangeError struct{ Field, Reason string }

func (e *ExchangeError) Error() string {
	if e.Field == "" {
		return "billing: exchange rate " + e.Reason
	}
	return "billing: exchange rate " + e.Field + " " + e.Reason
}

// ErrStaleExchange refuses an adjustment decided against a superseded version.
var ErrStaleExchange = errors.New("billing: the exchange rate changed since it was read")

// minorDigits is every currency an exchange rate can be kept for (the CHECK of billing.exchange_rates lists the same),
// with the number of decimals of its minor unit (ISO 4217), which a payment in it is counted in.
var minorDigits = map[string]int{
	"USD": 2, "CNY": 2, "EUR": 2, "GBP": 2, "JPY": 0, "HKD": 2, "SGD": 2, "AUD": 2, "CAD": 2, "CHF": 2,
}

// ExchangePair canonicalizes both directions to one history and one serialization point.
func ExchangePair(base, quote string) (string, string, error) {
	for _, f := range []struct{ name, value string }{{"base", base}, {"quote", quote}} {
		if _, ok := minorDigits[f.value]; !ok {
			return "", "", &ExchangeError{f.name, "is not a supported currency"}
		}
	}
	if base == quote {
		return "", "", &ExchangeError{"quote", "must differ from base"}
	}
	if base > quote {
		base, quote = quote, base
	}
	return base, quote, nil
}

func exchangeInteger(field, value string) (int64, error) {
	if len(value) == 0 || len(value) > 19 || strings.Trim(value, "0123456789") != "" {
		return 0, &ExchangeError{field, "must be an integer from 1 to 10^18"}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 || n > 1e18 {
		return 0, &ExchangeError{field, "must be an integer from 1 to 10^18"}
	}
	return n, nil
}

func (in ExchangeInput) normalized() (platform.ExchangeRate, error) {
	base, quote, err := ExchangePair(in.Base, in.Quote)
	if err != nil {
		return platform.ExchangeRate{}, err
	}
	n, err := exchangeInteger("numerator", in.Numerator)
	if err != nil {
		return platform.ExchangeRate{}, err
	}
	d, err := exchangeInteger("denominator", in.Denominator)
	if err != nil {
		return platform.ExchangeRate{}, err
	}
	if base != in.Base {
		n, d = d, n
	}
	r := big.NewRat(n, d)
	if r.Cmp(big.NewRat(1, 1e6)) < 0 || r.Cmp(big.NewRat(1e6, 1)) > 0 {
		return platform.ExchangeRate{}, &ExchangeError{"", "rate must be between 10^-6 and 10^6"}
	}
	if in.ExpectedVersion < 0 || in.ExpectedVersion == math.MaxInt64 {
		return platform.ExchangeRate{}, &ExchangeError{"expected_version", "is out of range"}
	}
	if !slices.Contains([]string{"initial", "adjustment", "correction"}, in.Kind) {
		return platform.ExchangeRate{}, &ExchangeError{"kind", "must be initial, adjustment or correction"}
	}
	if strings.TrimSpace(in.Reason) == "" || utf8.RuneCountInString(in.Reason) > 1000 {
		return platform.ExchangeRate{}, &ExchangeError{"reason", "must contain 1 to 1000 characters"}
	}
	return platform.ExchangeRate{Base: base, Quote: quote, Numerator: r.Num().Int64(), Denominator: r.Denom().Int64()}, nil
}

func checkExchangeChange(in ExchangeInput, next platform.ExchangeRate, previous []BillingExchangeRate) error {
	version := int64(0)
	if len(previous) > 0 {
		version = previous[0].Version
	}
	if version != in.ExpectedVersion {
		return ErrStaleExchange
	}
	if (version == 0) != (in.Kind == "initial") {
		return &ExchangeError{"kind", "initial is required only for the first version"}
	}
	if version > 0 && in.Kind != "correction" {
		p := previous[0]
		ratio := new(big.Rat).Quo(big.NewRat(next.Numerator, next.Denominator), big.NewRat(p.Numerator, p.Denominator))
		if ratio.Cmp(big.NewRat(1, 5)) < 0 || ratio.Cmp(big.NewRat(5, 1)) > 0 {
			return &ExchangeError{"", "change exceeds fivefold; use correction with a reason"}
		}
	}
	return nil
}

// SetExchangeRate appends and audits in the caller's platform transaction. The admin wraps it in idem.Once.
func SetExchangeRate(ctx context.Context, tx pg.Tx, actor audit.Actor, in ExchangeInput) (BillingExchangeRate, error) {
	return setExchangeRate(ctx, tx, actor, in, "manual", nil)
}

func setExchangeRate(ctx context.Context, tx pg.Tx, actor audit.Actor, in ExchangeInput, source string, sourceAt *time.Time) (BillingExchangeRate, error) {
	r, err := in.normalized()
	if err != nil {
		return BillingExchangeRate{}, err
	}
	// The leading namespace byte separates these six-byte pair keys from other advisory locks.
	key := int64(0x46)
	for _, b := range []byte(r.Base + r.Quote) {
		key = key<<8 | int64(b)
	}
	if err = queries.LockExchangePair(ctx, tx, key); err != nil {
		return BillingExchangeRate{}, fmt.Errorf("billing: lock exchange pair: %w", err)
	}
	previous, err := queries.LatestExchangeRate(ctx, tx, LatestExchangeRateParams{Base: r.Base, Quote: r.Quote})
	if err != nil {
		return BillingExchangeRate{}, err
	}
	if err = checkExchangeChange(in, r, previous); err != nil {
		return BillingExchangeRate{}, err
	}
	row, err := queries.InsertExchangeRate(ctx, tx, InsertExchangeRateParams{
		ID: uuid.NewV7(), Base: r.Base, Quote: r.Quote, Version: in.ExpectedVersion + 1,
		Numerator: r.Numerator, Denominator: r.Denominator, Kind: in.Kind, Reason: in.Reason, CreatedBy: actor.String(), Source: source, SourceAt: sourceAt,
	})
	if err != nil {
		return row, fmt.Errorf("billing: append exchange rate: %w", err)
	}
	err = audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "exchange_rate." + in.Kind, Reason: in.Reason,
		Target: audit.Target{Module: "billing", Kind: "exchange_rate", ID: row.ID.String()},
		Detail: map[string]any{"base": row.Base, "quote": row.Quote, "version": row.Version, "numerator": row.Numerator, "denominator": row.Denominator},
	})
	return row, err
}

// ExchangeRates reads all current pairs in one statement, so a build never mixes versions of a pair.
func ExchangeRates(ctx context.Context, tx pg.Tx) ([]BillingExchangeRate, error) {
	return queries.CurrentExchangeRates(ctx, tx)
}

// ExchangeRateHistory reads at most 100 versions, newest first; before is an exclusive version cursor, zero for newest.
func ExchangeRateHistory(ctx context.Context, tx pg.Tx, base, quote string, before int64) ([]BillingExchangeRate, error) {
	base, quote, err := ExchangePair(base, quote)
	if err != nil {
		return nil, err
	}
	if before < 0 {
		return nil, &ExchangeError{"before_version", "must not be negative"}
	}
	return queries.ExchangeHistory(ctx, tx, ExchangeHistoryParams{Base: base, Quote: quote, BeforeVersion: before})
}

// Basis is the value shared with routing and suppliers, without importing billing's storage there.
func (r BillingExchangeRate) Basis() platform.ExchangeRate {
	return platform.ExchangeRate{ID: r.ID, Version: r.Version, Base: r.Base, Quote: r.Quote, Numerator: r.Numerator, Denominator: r.Denominator}
}
