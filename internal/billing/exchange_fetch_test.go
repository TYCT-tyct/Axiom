package billing

import (
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
	"uuid"
)

const exchangeSample = `<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref"><Cube><Cube time="2026-10-29"><Cube currency="USD" rate="1.1234"/><Cube currency="CNY" rate="7.8765"/><Cube currency="GBP" rate="0.83825"/><Cube currency="JPY" rate="177.36"/><Cube currency="HKD" rate="8.72"/><Cube currency="SGD" rate="1.46"/><Cube currency="AUD" rate="1.72"/><Cube currency="CAD" rate="1.56"/><Cube currency="CHF" rate="0.94"/></Cube></Cube></gesmes:Envelope>`

var exchangeAt = time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)

func TestExchangeSourceValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, raw string }{
		{"valid", exchangeSample},
		{"malformed", exchangeSample[:len(exchangeSample)-5]},
		{"trailing", exchangeSample + `<unexpected/>`},
		{"future", strings.ReplaceAll(exchangeSample, "2026-10-29", "2026-10-30")},
		{"no date", strings.ReplaceAll(exchangeSample, `time="2026-10-29"`, "")},
		{"duplicate", strings.ReplaceAll(exchangeSample, `currency="CHF"`, `currency="USD"`)},
		{"missing", strings.ReplaceAll(exchangeSample, `currency="CHF"`, `currency="ZZZ"`)},
		{"zero", strings.ReplaceAll(exchangeSample, `rate="0.94"`, `rate="0"`)},
		{"fraction", strings.ReplaceAll(exchangeSample, `rate="0.94"`, `rate="47/50"`)},
		{"exponent", strings.ReplaceAll(exchangeSample, `rate="0.94"`, `rate="94e-2"`)},
		{"negative", strings.ReplaceAll(exchangeSample, `rate="0.94"`, `rate="-0.94"`)},
		{"out of range", strings.ReplaceAll(exchangeSample, `rate="0.94"`, `rate="0.00000000001"`)},
		{"huge decimal", strings.ReplaceAll(exchangeSample, `rate="0.94"`, `rate="`+strings.Repeat("9", 100)+`"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs, date, err := parseExchange([]byte(tc.raw), exchangeAt)
			if tc.name != "valid" {
				if err == nil {
					t.Fatal("accepted invalid source")
				}
				return
			}
			checkExchangeSourcePairs(t, inputs, date, err)
		})
	}
}

func checkExchangeSourcePairs(t *testing.T, inputs []ExchangeInput, date time.Time, err error) {
	t.Helper()
	if err != nil || date.Format(time.DateOnly) != "2026-10-29" {
		t.Fatalf("parse: %v %v", date, err)
	}
	if len(inputs) != len(minorDigits)*(len(minorDigits)-1)/2 {
		t.Fatalf("pairs: %d", len(inputs))
	}
	for _, in := range inputs {
		if in.Base == "CNY" && in.Quote == "USD" {
			got := big.NewRat(mustNormalize(t, in).Numerator, mustNormalize(t, in).Denominator)
			want := big.NewRat(11234, 78765)
			if got.Cmp(want) != 0 {
				t.Fatalf("cross %s want %s", got, want)
			}
		}
	}
}

func TestExchangeSourceRoundTrips(t *testing.T) {
	t.Parallel()
	inputs, _, err := parseExchange([]byte(exchangeSample), exchangeAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range inputs {
		rate := mustNormalize(t, in)
		rate.ID = uuid.NewV7()
		from, to := currency(t, rate.Base), currency(t, rate.Quote)
		forward, ok := rate.Between(from, to)
		if !ok {
			t.Fatal("missing forward")
		}
		reverse, ok := rate.Between(to, from)
		if !ok {
			t.Fatal("missing reverse")
		}
		for i := int64(1); i <= 100; i++ {
			amount := big.NewRat(i*123456789, i+17)
			got := new(big.Rat).Mul(amount, forward)
			got.Mul(got, reverse)
			if got.Cmp(amount) != 0 {
				t.Fatalf("%s/%s round trip %s != %s", in.Base, in.Quote, got, amount)
			}
		}
	}
}

// Recorded from the free ECB daily endpoint on 2026-10-29; source date 2026-10-28.
func TestExchangeSourceRecorded(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/ecb-daily.xml")
	if err != nil {
		t.Fatal(err)
	}
	inputs, date, err := parseExchange(raw, exchangeAt)
	if err != nil || date.Format(time.DateOnly) != "2026-10-28" || len(inputs) != len(minorDigits)*(len(minorDigits)-1)/2 {
		t.Fatalf("recorded source: date %v pairs %d error %v", date, len(inputs), err)
	}
}
