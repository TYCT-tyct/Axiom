package operations

import (
	"strings"
	"testing"
	"time"

	"axiom/internal/platform"
)

// TestTargetsIgnoreOrRefuseBadStoredRows: a stored value that does not fit its metric is refused rather than shown as
// zero or truncated.
func TestTargetsIgnoreOrRefuseBadStoredRows(t *testing.T) {
	usd := "USD"
	notACurrency := "NOPE"
	month := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		row  storedTarget
	}{
		{"negative count", storedTarget{Month: month, Metric: Calls, Amount: "-1"}},
		{"rate above 100%", storedTarget{Month: month, Metric: GrossMargin, Amount: "10001"}},
		{"rate with currency", storedTarget{Month: month, Metric: Retention, Amount: "1", Currency: &usd}},
		{"money without currency", storedTarget{Month: month, Metric: Revenue, Amount: "1"}},
		{"money with an unknown currency", storedTarget{Month: month, Metric: Cost, Amount: "1", Currency: &notACurrency}},
		{"unknown metric", storedTarget{Month: month, Metric: "visits", Amount: "1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := targetsFrom([]storedTarget{c.row}, month); err == nil {
				t.Errorf("%+v was accepted", c.row)
			}
		})
	}
}

// TestExpectedRefusesBadStoredRows: the comparison refuses a count wider than 64 bits, a negative amount and an
// unknown metric instead of presenting them as real figures.
func TestExpectedRefusesBadStoredRows(t *testing.T) {
	month := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		row  ReadTargetsRow
	}{
		{"count wider than 64 bits", ReadTargetsRow{Month: month, Metric: string(Calls), Amount: strings.Repeat("9", 100)}},
		{"negative amount", ReadTargetsRow{Month: month, Metric: string(Calls), Amount: "-1"}},
		{"unknown metric", ReadTargetsRow{Month: month, Metric: "visits", Amount: "1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := expectedFrom([]ReadTargetsRow{c.row}, platform.Currency{}, month, month.AddDate(0, 1, 0)); err == nil {
				t.Errorf("%+v was accepted", c.row)
			}
		})
	}
}
