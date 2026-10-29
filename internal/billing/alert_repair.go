package billing

import (
	"context"
	"strconv"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

const settlementAlertPosition = "billing.platform_borne"

func settlementAlertSource(e event) string {
	return settlementAlertPosition + "/" + e.request.String() + "/" + strconv.FormatInt(int64(e.seq), 10) + "/" + strconv.FormatInt(e.revision, 10)
}

// Initialize before this node accepts calls, so historical losses are not new incidents and a lost first signal
// can still be repaired. Restarting another node does not rescan the ledger.
func initializeSettlementAlerts(ctx context.Context, tx pg.Tx) (bool, error) {
	raw, err := alert.LockPosition(ctx, tx, settlementAlertPosition)
	if err != nil {
		return false, err
	}
	var position struct{ Initialized bool }
	if err = jsonx.Unmarshal(raw, &position); err != nil || position.Initialized {
		return false, err
	}
	sources, err := settlementAlertSources(ctx, tx)
	if err != nil {
		return false, err
	}
	if _, err = alert.ClaimSources(ctx, tx, sources); err != nil {
		return false, err
	}
	position.Initialized = true
	return true, alert.SavePosition(ctx, tx, settlementAlertPosition, position)
}

// The immediate writer and repair claim the same ledger identity. A consumed or manually closed incident must
// not make that loss new again; claiming and publishing commit together, outside the successful call path.
func repairSettlementAlerts(ctx context.Context, tx pg.Tx) error {
	initialized, err := initializeSettlementAlerts(ctx, tx)
	if err != nil || initialized {
		return err
	}
	sources, err := settlementAlertSources(ctx, tx)
	if err != nil {
		return err
	}
	claimed, err := alert.ClaimSources(ctx, tx, sources)
	if err != nil || claimed == 0 {
		return err
	}
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "billing.settlement_anomaly", Key: "platform_borne", Severity: "critical", Fields: map[string]string{"settlements": strconv.FormatInt(claimed, 10)}})
}

func settlementAlertSources(ctx context.Context, tx pg.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT t.request_id,t.attempt_seq,t.revision FROM billing.entries e JOIN billing.transactions t ON t.id=e.transaction_id
 WHERE t.kind='settle' AND e.book='platform_borne' AND e.amount>0 ORDER BY t.request_id,t.attempt_seq,t.revision`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []string
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.request, &e.seq, &e.revision); err != nil {
			return nil, err
		}
		sources = append(sources, settlementAlertSource(e))
	}
	return sources, rows.Err()
}
