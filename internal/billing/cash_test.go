package billing

import (
	"fmt"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// TestListCashMovements: cash follows the transaction's time, not an order's, and names where it came from; a refund of
// an older batch is still a movement of this window, and the cursor continues from the last row.
func TestListCashMovements(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	site, account := b.admitted.Site, b.admitted.Account
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	offline, online := uuid.NewV7(), uuid.NewV7()
	b.sql(
		fmt.Sprintf(`INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, currency, paid, fee,
			offline_channel, created_at) VALUES ('%s', '%s', '%s', 'paid', 2000, 'USD', 2000, 0, 'bank', '%s')`,
			offline, site, account, start.Format(time.RFC3339)),
		fmt.Sprintf(`INSERT INTO billing.credit_batches (id, site_id, account_id, source, amount, currency, paid, fee,
			merchant_id, created_at) VALUES ('%s', '%s', '%s', 'paid', 3000, 'USD', 3000, 0, '%s', '%s')`,
			online, site, account, uuid.NewV7(), start.Format(time.RFC3339)),
		fmt.Sprintf(`INSERT INTO billing.transactions (id, site_id, account_id, kind, request_id, created_at) VALUES
			('%s', '%s', '%s', 'credit', '%s', '%s'),
			('%s', '%s', '%s', 'refund', '%s', '%s'),
			('%s', '%s', '%s', 'credit', '%s', '%s')`,
			uuid.NewV7(), site, account, offline, start.Add(3*time.Hour).Format(time.RFC3339),
			uuid.NewV7(), site, account, offline, start.Add(4*time.Hour).Format(time.RFC3339),
			uuid.NewV7(), site, account, online, start.Add(5*time.Hour).Format(time.RFC3339)),
		fmt.Sprintf(`INSERT INTO billing.entries (transaction_id, site_id, account_id, book, amount, currency)
			SELECT t.id, t.site_id, t.account_id, 'cash', CASE t.kind WHEN 'refund' THEN -500 ELSE 2000 END, 'USD'
			FROM billing.transactions t WHERE t.site_id = '%s'`, site),
	)
	read := func(after CashCursor, limit int) ([]CashMovement, *CashCursor) {
		t.Helper()
		var rows []CashMovement
		var next *CashCursor
		if err := b.app.ReadTx(t.Context(), b.platform, func(tx pg.Tx) (err error) {
			rows, next, err = ListCashMovements(t.Context(), tx, CashQuery{
				Site: site, Since: start, Until: start.Add(24 * time.Hour), After: after, Limit: limit,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return rows, next
	}
	first, next := read(CashCursor{At: start.Add(24 * time.Hour)}, 2)
	if len(first) != 2 || first[0].Kind != "credit" || first[1].Kind != "refund" || !first[0].Online || first[1].Online {
		t.Fatalf("first page = %+v, want the online credit then the online refund", first)
	}
	if next == nil {
		t.Fatal("no cursor after a full page")
	}
	second, end := read(*next, 2)
	if len(second) != 1 || second[0].OfflineChannel != "bank" || second[0].Amount != "2000" || second[0].Currency != "USD" {
		t.Fatalf("second page = %+v, want the offline credit", second)
	}
	if end != nil {
		t.Fatalf("cursor after the last page: %+v", end)
	}
}
