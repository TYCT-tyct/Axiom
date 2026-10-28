package billing

import "testing"

func TestLedgerPoliciesPassAudit(t *testing.T) {
	t.Parallel()
	b := newBank(t, 0, 0, Config{})
	if err := b.app.Check(t.Context(), Migrations()); err != nil {
		t.Fatal(err)
	}
}
