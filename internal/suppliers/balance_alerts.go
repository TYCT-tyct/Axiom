package suppliers

import (
	"context"
	"slices"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// The low-balance alert (docs/domains/suppliers-direct.md「模型重读与余额」): a relay account's balance, or a pasted key's
// own wallet, under its supplier's alert line raises suppliers.low_balance once, and a reading at or over the line
// resolves it. The line is in the balance's own unit; a supplier without one is held to 5 of its settlement currency
// (balanceReadings), converted for a relay at its latest confirmed unit value (none confirmed, no line).

// LowBalanceLines reads each supplier's alert line that a person set, as decimal text in the unit its balances are
// read in; cmd/axiom wires it to the alert configuration, which this module cannot read.
type LowBalanceLines func(context.Context, pg.Tx) (map[uuid.UUID]string, error)

// balanceAlertPosition is the keys last reported low: the set the next reading is compared with, so a crossing is told
// once and a balance that stays low or stays high sends nothing.
type balanceAlertPosition struct {
	Initialized bool
	Low         []string
}

type balanceReading struct {
	key    string
	low    bool
	fields map[string]string
}

// The balances read, each with the line it is held to. Relays read theirs onto the supplier; a type with wallets onto each
// key. Only suppliers that are served or about to be are read, and only keys that can still be called. The default line
// is 5 of the settlement currency.
const balanceReadings = `WITH line AS (SELECT l.supplier_id, l.threshold::numeric FROM unnest($1::uuid[], $2::text[]) l(supplier_id, threshold)),
live AS (SELECT s.* FROM suppliers.suppliers s WHERE s.status IN ('connecting','active','low_balance','needs_attention'))
SELECT r.key, r.supplier_id, r.name, r.hint, trim_scale(r.balance)::text, trim_scale(r.threshold)::text, r.unit, r.balance < r.threshold
FROM (
 SELECT s.id::text AS key, s.id AS supplier_id, s.name, NULL::text AS hint, s.balance_native AS balance,
  CASE s.native_unit WHEN 'usd' THEN 'USD' ELSE s.native_unit END AS unit,
  coalesce(l.threshold, round(5 / (SELECT u.value FROM suppliers.unit_values u
   WHERE u.supplier_id = s.id AND u.currency = s.settlement_currency AND u.native_unit = s.native_unit ORDER BY u.id DESC LIMIT 1), 6)) AS threshold
 FROM live s LEFT JOIN line l ON l.supplier_id = s.id
 WHERE s.mode = 'relay' AND s.balance_native IS NOT NULL
 UNION ALL
 SELECT s.id::text || '/' || c.id::text, s.id, s.name, c.hint, c.balance_native, s.settlement_currency, coalesce(l.threshold, 5)
 FROM suppliers.credentials c JOIN live s ON s.id = c.supplier_id LEFT JOIN line l ON l.supplier_id = s.id
 WHERE c.balance_native IS NOT NULL AND c.status IN ('active','quota_exhausted')
) r WHERE r.threshold IS NOT NULL ORDER BY r.key`

// emitBalanceAlerts compares every balance with its line and persists a signal for each key whose side of the line
// changed since the last reading. On its first run the relays the sync engine marked low (which the earlier bridge
// alerted on, under the same kind and key) count as reported, so those no longer low are resolved.
func emitBalanceAlerts(ctx context.Context, tx pg.Tx, lines LowBalanceLines) error {
	const source = "suppliers.balance_low"
	raw, err := alert.LockPosition(ctx, tx, source)
	if err != nil {
		return err
	}
	var p balanceAlertPosition
	if err = jsonx.Unmarshal(raw, &p); err != nil {
		return err
	}
	if !p.Initialized {
		marked, err := readSupplierAlertIDs(ctx, tx, `SELECT id FROM suppliers.suppliers WHERE status='low_balance' AND id>$1 ORDER BY id`, uuid.Nil())
		if err != nil {
			return err
		}
		for _, id := range marked {
			p.Low = append(p.Low, id.String())
		}
	}
	readings, err := readBalances(ctx, tx, lines)
	if err != nil {
		return err
	}
	reported := map[string]bool{}
	for _, key := range p.Low {
		reported[key] = true
	}
	low := []string{}
	for _, r := range readings {
		was := reported[r.key]
		delete(reported, r.key)
		if r.low {
			low = append(low, r.key)
		}
		if r.low == was {
			continue
		}
		signal := alert.Signal{Kind: "suppliers.low_balance", Key: r.key, Severity: "warning", Fields: r.fields, Resolved: !r.low}
		if err = alert.Persist(ctx, tx, uuid.NewV7(), signal); err != nil {
			return err
		}
	}
	// A key reported low that is no longer read (removed, disabled, or its supplier paused) has no balance to be low.
	gone := make([]string, 0, len(reported))
	for key := range reported {
		gone = append(gone, key)
	}
	slices.Sort(gone)
	for _, key := range gone {
		if err = alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "suppliers.low_balance", Key: key, Severity: "warning", Resolved: true}); err != nil {
			return err
		}
	}
	return alert.SavePosition(ctx, tx, source, balanceAlertPosition{Initialized: true, Low: low})
}

func readBalances(ctx context.Context, tx pg.Tx, lines LowBalanceLines) ([]balanceReading, error) {
	var ids []uuid.UUID
	var amounts []string
	if lines != nil {
		set, err := lines(ctx, tx)
		if err != nil {
			return nil, err
		}
		for id, amount := range set {
			ids, amounts = append(ids, id), append(amounts, amount)
		}
	}
	rows, err := tx.Query(ctx, balanceReadings, ids, amounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []balanceReading
	for rows.Next() {
		var r balanceReading
		var supplier uuid.UUID
		var name, balance, threshold, unit string
		var hint *string
		if err = rows.Scan(&r.key, &supplier, &name, &hint, &balance, &threshold, &unit, &r.low); err != nil {
			return nil, err
		}
		// Only the key's last characters, which the admin shows too, ever leave the credentials row.
		r.fields = map[string]string{"supplier": name, "supplier_id": supplier.String(), "balance": balance, "threshold": threshold, "unit": unit}
		if hint != nil {
			r.fields["key_hint"] = "…" + *hint
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
