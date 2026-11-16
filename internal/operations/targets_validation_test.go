package operations

import (
	"testing"
	"time"
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
