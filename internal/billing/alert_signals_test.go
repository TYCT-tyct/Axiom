package billing

import (
	"testing"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

type settlementSignals []alert.Signal

func (s *settlementSignals) Raise(kind, key, severity string, fields map[string]string) error {
	*s = append(*s, alert.Signal{Kind: kind, Key: key, Severity: severity, Fields: fields})
	return nil
}

func (s *settlementSignals) RaiseOnce(source, kind, key, severity string, fields map[string]string) error {
	*s = append(*s, alert.Signal{Source: source, Kind: kind, Key: key, Severity: severity, Fields: fields})
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
	if len(signals) != 1 || signals[0].Kind != "billing.settlement_anomaly" || signals[0].Key != "platform_borne" || signals[0].Fields["settlements"] != "1" {
		t.Fatalf("ledger repair: %+v", signals)
	}
	// An offsetting reversal must not hide the newly recorded loss from the repair source.
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH t AS (INSERT INTO billing.transactions(id,site_id,account_id,kind,request_id)
 SELECT $2,site_id,account_id,'settle',$2 FROM billing.transactions WHERE request_id=$1 AND kind='settle' RETURNING *)
 INSERT INTO billing.entries(transaction_id,site_id,account_id,book,amount)
 SELECT t.id,t.site_id,t.account_id,'platform_borne',v.amount FROM t CROSS JOIN (VALUES(100),(-100)) v(amount)`, h.id, uuid.NewV7())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	repairBillingSignals(t, b)
	signals = takeBillingSignals(t, b)
	if len(signals) != 1 || signals[0].Fields["settlements"] != "1" {
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

func TestPlatformBorneBootstrapSkipsHistory(t *testing.T) {
	b := newBank(t, 2000, -100, Config{})
	_, h := b.hold(500), b.hold(1000)
	b.settleWith(h, outcome(Committed, FinishStop, used(t, 5000, 0, 0)))
	// A database with losses before the first version containing this signal was deployed.
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE platform.alert_positions SET position='{}' WHERE source=$1`, settlementAlertPosition)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		repairBillingSignals(t, b)
	}
	if got := takeBillingSignals(t, b); len(got) != 0 {
		t.Fatalf("historical losses became new incidents: %+v", got)
	}
}

func TestPlatformBorneImmediateAndRepairShareAnIdentity(t *testing.T) {
	for _, repairFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate first", true: "repair first"}[repairFirst], func(t *testing.T) {
			checkPlatformBorneIdentityOrder(t, repairFirst)
		})
	}
}

func checkPlatformBorneIdentityOrder(t *testing.T, repairFirst bool) {
	t.Helper()
	var immediate settlementSignals
	cfg := fallbackBankConfig
	cfg.Alerts = &immediate
	b := newBank(t, 2000, -100, cfg)
	_, h := b.hold(500), b.hold(1000)
	b.settleWith(h, outcome(Committed, FinishStop, used(t, 5000, 0, 0)))
	if len(immediate) != 1 || immediate[0].Source == "" {
		t.Fatalf("missing loss identity: %+v", immediate)
	}
	if repairFirst {
		repairBillingSignals(t, b)
	}
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		return alert.Persist(t.Context(), tx, uuid.NewV7(), immediate[0])
	}); err != nil {
		t.Fatal(err)
	}
	if got := takeBillingSignals(t, b); len(got) != 1 {
		t.Fatalf("initial loss: %+v", got)
	}
	// The operator can now close the incident; neither source may resurrect it.
	for range 2 {
		repairBillingSignals(t, b)
	}
	if got := takeBillingSignals(t, b); len(got) != 0 {
		t.Fatalf("consumed loss was reported again: %+v", got)
	}
}

func TestPlatformBorneLateRevisionHasItsOwnIdentity(t *testing.T) {
	var immediate settlementSignals
	cfg := fallbackBankConfig
	cfg.Alerts = &immediate
	b := newBank(t, 2000, -100, cfg)
	_, h := b.hold(500), b.hold(1000)
	b.settleWith(h, outcome(Committed, FinishStop, used(t, 5000, 0, 0)))
	// Legacy cleanup could charge before the node returned with the full outcome. Terminal rows are RLS-frozen.
	b.sql(`UPDATE billing.reservations SET state='cleanup_settled' WHERE request_id='` + h.id.String() + `'`)
	b.settleWith(h, outcome(Committed, FinishStop, used(t, 6000, 0, 0)))
	if len(immediate) != 2 || immediate[0].Source == immediate[1].Source {
		t.Fatalf("distinct ledger revisions shared an identity: %+v", immediate)
	}
	if err := b.app.Tx(t.Context(), b.platform, func(tx pg.Tx) error {
		for _, s := range immediate {
			if err := alert.Persist(t.Context(), tx, uuid.NewV7(), s); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := takeBillingSignals(t, b); len(got) != 2 {
		t.Fatalf("new settlement revision was suppressed: %+v", got)
	}
	repairBillingSignals(t, b)
	if got := takeBillingSignals(t, b); len(got) != 0 {
		t.Fatalf("repair reopened an acknowledged revision: %+v", got)
	}
}
