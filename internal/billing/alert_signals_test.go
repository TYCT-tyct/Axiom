package billing

import (
	"testing"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

type settlementSignals []alert.Signal

func (s *settlementSignals) Raise(kind, key, severity string, fields map[string]string) error {
	*s = append(*s, alert.Signal{Kind: kind, Key: key, Severity: severity, Fields: fields})
	return nil
}

func (s *settlementSignals) Resolve(kind, key string) error {
	*s = append(*s, alert.Signal{Kind: kind, Key: key, Resolved: true})
	return nil
}

func TestPlatformBorneSignalsRepairFromTheLedger(t *testing.T) {
	b := newBank(t, 2000, -100, Config{})
	_, h := b.hold(500), b.hold(1000)
	b.settleWith(h, outcome(Committed, FinishStop, used(t, 5000, 0, 0)))
	for range 2 {
		repairBillingSignals(t, b)
	}
	signals := takeBillingSignals(t, b)
	if len(signals) != 1 || signals[0].Kind != "billing.settlement_anomaly" || signals[0].Key != "platform_borne" || signals[0].Fields["entries"] != "1" {
		t.Fatalf("ledger repair: %+v", signals)
	}
	// An offsetting reversal must not hide the newly recorded loss from the repair source.
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO billing.entries(transaction_id,site_id,account_id,book,amount)
 SELECT t.id,t.site_id,t.account_id,'platform_borne',v.amount FROM billing.transactions t CROSS JOIN (VALUES(100),(-100)) v(amount) WHERE t.request_id=$1 AND t.kind='settle'`, h.id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	repairBillingSignals(t, b)
	signals = takeBillingSignals(t, b)
	if len(signals) != 1 || signals[0].Fields["entries"] != "2" {
		t.Fatalf("offsetting reversal hid a new loss: %+v", signals)
	}
}

func takeBillingSignals(t *testing.T, b *bank) []alert.Signal {
	t.Helper()
	var signals []alert.Signal
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		return alert.Consume(t.Context(), tx, func(s alert.Signal) error { signals = append(signals, s); return nil })
	}); err != nil {
		t.Fatal(err)
	}
	return signals
}

func repairBillingSignals(t *testing.T, b *bank) {
	t.Helper()
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error { return repairSettlementAlerts(t.Context(), tx) }); err != nil {
		t.Fatal(err)
	}
}
