package billing

import "testing"

// BenchmarkReserveManySettle covers the database work of one complete call through the batch admission path, including
// the ledger and reserved counters. It is renamed from the benchmark of the removed single-reservation path: a
// one-element batch is what production runs, and its allocation profile is not comparable with the deleted path.
func BenchmarkReserveManySettle(b *testing.B) {
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
