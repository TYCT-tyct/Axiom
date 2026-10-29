package billing

import "testing"

// BenchmarkReserveSettle covers the database work of a complete call, including the ledger and reserved counters.
func BenchmarkReserveSettle(b *testing.B) {
	bank := newBank(b, 1e12, 0, Config{})
	o := outcome(Committed, FinishStop, used(b, 50, 20, 0))
	b.ReportAllocs()
	for b.Loop() {
		h, err := bank.tryHold(100)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := bank.billing.Settle(b.Context(), h, o); err != nil {
			b.Fatal(err)
		}
	}
}
