package operations

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform"
)

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func targetRow(t *testing.T, month string, metric Metric, amount, currency string) ReadTargetsRow {
	t.Helper()
	row := ReadTargetsRow{Month: at(t, month+"T00:00:00Z"), Metric: string(metric), Amount: amount}
	if currency != "" {
		row.Currency = &currency
	}
	return row
}

func count(value int64) *int64 { return &value }

func usd(t *testing.T) platform.Currency {
	t.Helper()
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	return currency
}

func TestExpectedFromProratesEachMonthBySeconds(t *testing.T) {
	t.Parallel()
	jan := "2026-01-01"
	feb := "2026-02-01"
	cases := []struct {
		name     string
		rows     []ReadTargetsRow
		since    string
		until    string
		currency string
		want     Expected
	}{
		{
			name: "a window across two months sums each month's share",
			rows: []ReadTargetsRow{
				targetRow(t, jan, Calls, "3100", ""),
				targetRow(t, feb, Calls, "2800", ""),
			},
			since: "2026-01-31T12:00:00Z", until: "2026-02-01T12:00:00Z", currency: "USD",
			want: Expected{Calls: count(100)},
		},
		{
			name:  "six hours of a 31-day month",
			rows:  []ReadTargetsRow{targetRow(t, jan, Calls, "3100", "")},
			since: "2026-01-01T00:00:00Z", until: "2026-01-01T06:00:00Z", currency: "USD",
			want: Expected{Calls: count(25)},
		},
		{
			name:  "a count rounds half up",
			rows:  []ReadTargetsRow{targetRow(t, jan, Errors, "1", "")},
			since: "2026-01-01T00:00:00Z", until: "2026-01-16T12:00:00Z", currency: "USD",
			want: Expected{Errors: count(1)},
		},
		{
			name:  "a count below half rounds down",
			rows:  []ReadTargetsRow{targetRow(t, jan, Calls, "1", "")},
			since: "2026-01-01T00:00:00Z", until: "2026-01-01T01:00:00Z", currency: "USD",
			want: Expected{Calls: count(0)},
		},
		{
			name:  "a leap February has 29 days",
			rows:  []ReadTargetsRow{targetRow(t, "2028-02-01", Calls, "29", "")},
			since: "2028-02-01T00:00:00Z", until: "2028-02-02T00:00:00Z", currency: "USD",
			want: Expected{Calls: count(1)},
		},
		{
			name:  "money rounds to the billionth",
			rows:  []ReadTargetsRow{targetRow(t, jan, Revenue, "1000000000", "USD")},
			since: "2026-01-01T00:00:00Z", until: "2026-01-02T00:00:00Z", currency: "USD",
			want: Expected{Revenue: &Money{Amount: "32258065", Currency: usd(t)}},
		},
		{
			name: "money sums whole months",
			rows: []ReadTargetsRow{
				targetRow(t, jan, Cost, "100000000000", "USD"),
				targetRow(t, feb, Cost, "200000000000", "USD"),
			},
			since: "2026-01-01T00:00:00Z", until: "2026-03-01T00:00:00Z", currency: "USD",
			want: Expected{Cost: &Money{Amount: "300000000000", Currency: usd(t)}},
		},
		{
			name:  "a money target in another currency is not compared",
			rows:  []ReadTargetsRow{targetRow(t, jan, Revenue, "1000000000", "USD")},
			since: "2026-01-01T00:00:00Z", until: "2026-02-01T00:00:00Z", currency: "CNY",
			want: Expected{},
		},
		{
			name:  "a month without a target contributes nothing",
			rows:  []ReadTargetsRow{targetRow(t, jan, Calls, "3100", "")},
			since: "2026-01-31T12:00:00Z", until: "2026-02-01T12:00:00Z", currency: "USD",
			want: Expected{Calls: count(50)},
		},
		{
			name:  "zero is a target, not an absence",
			rows:  []ReadTargetsRow{targetRow(t, jan, Calls, "0", "")},
			since: "2026-01-01T00:00:00Z", until: "2026-02-01T00:00:00Z", currency: "USD",
			want: Expected{Calls: count(0)},
		},
		{
			name:  "a target outside the window is ignored",
			rows:  []ReadTargetsRow{targetRow(t, "2026-03-01", Calls, "3100", "")},
			since: "2026-01-01T00:00:00Z", until: "2026-02-01T00:00:00Z", currency: "USD",
			want: Expected{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			currency, err := platform.ParseCurrency(tc.currency)
			if err != nil {
				t.Fatal(err)
			}
			got, err := expectedFrom(tc.rows, currency, at(t, tc.since), at(t, tc.until))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(platform.Currency{})); diff != "" {
				t.Errorf("expected targets differ (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExpectedFromRefusesACountWiderThanItsType(t *testing.T) {
	t.Parallel()
	_, err := expectedFrom(
		[]ReadTargetsRow{targetRow(t, "2026-01-01", Calls, "9223372036854775808", "")},
		usd(t), at(t, "2026-01-01T00:00:00Z"), at(t, "2026-02-01T00:00:00Z"),
	)
	if err == nil {
		t.Fatal("a count target wider than int64 was accepted")
	}
}

func TestTargetsCheck(t *testing.T) {
	t.Parallel()
	valid := func() Targets {
		return Targets{
			Month: at(t, "2026-01-01T00:00:00Z"), Calls: count(1), Errors: count(0),
			Revenue: &Money{Amount: "1000000000", Currency: usd(t)},
			Cost:    &Money{Amount: "0", Currency: usd(t)},
		}
	}
	if err := valid().Check(); err != nil {
		t.Fatalf("a valid target was refused: %v", err)
	}
	cases := map[string]func(*Targets){
		"a month that is not its first day": func(targets *Targets) { targets.Month = at(t, "2026-01-02T00:00:00Z") },
		"a zero month":                      func(targets *Targets) { targets.Month = time.Time{} },
		"a negative count":                  func(targets *Targets) { targets.Calls = count(-1) },
		"a negative amount":                 func(targets *Targets) { targets.Revenue.Amount = "-1" },
		"an amount that is not an integer":  func(targets *Targets) { targets.Cost.Amount = "1.5" },
		"a money target without a currency": func(targets *Targets) { targets.Revenue.Currency = platform.Currency{} },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			targets := valid()
			spoil(&targets)
			if err := targets.Check(); err == nil {
				t.Fatal("an invalid target was accepted")
			}
		})
	}
}
