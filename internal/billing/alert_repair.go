package billing

import (
	"context"
	"strconv"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// The ledger is the durable repair source for an immediate floor-loss signal lost with a node or a full bus.
// Positive-entry counts never decrease in the append-only ledger, including reversals, and never mix currencies.
// The self-check already scans the ledger daily; this adds no work to successful settlements.
func repairSettlementAlerts(ctx context.Context, tx pg.Tx) error {
	const source = "billing.platform_borne"
	raw, err := alert.LockPosition(ctx, tx, source)
	if err != nil {
		return err
	}
	previous := struct{ Entries int64 }{}
	if err = jsonx.Unmarshal(raw, &previous); err != nil {
		return err
	}
	var entries int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM billing.entries WHERE book='platform_borne' AND amount>0`).Scan(&entries); err != nil {
		return err
	}
	if entries > previous.Entries {
		if err = alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "billing.settlement_anomaly", Key: "platform_borne", Severity: "critical", Fields: map[string]string{"entries": strconv.FormatInt(entries, 10)}}); err != nil {
			return err
		}
	}
	previous.Entries = entries
	return alert.SavePosition(ctx, tx, source, previous)
}
