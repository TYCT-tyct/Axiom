package suppliers

import (
	"context"
	"slices"
	"unicode/utf8"
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
// key. Only suppliers that are served or about to be are read, and only keys that can still be called. A line a person
// set is in the balance's own unit. The default line is 5 of the settlement currency: a relay's native balance is
// converted at its latest confirmed unit value and compared exactly, never against a rounded native line; a relay with no
// confirmed unit value has no default line.
const balanceReadings = `WITH line AS (SELECT l.supplier_id, l.threshold::numeric FROM unnest($1::uuid[], $2::text[]) l(supplier_id, threshold)),
live AS (SELECT s.* FROM suppliers.suppliers s WHERE s.status IN ('connecting','active','low_balance','needs_attention'))
SELECT r.key, r.supplier_id, r.name, r.hint, trim_scale(r.balance)::text, trim_scale(r.threshold)::text, r.unit, r.balance < r.threshold
FROM (
 SELECT s.id::text AS key, s.id AS supplier_id, s.name, NULL::text AS hint,
  CASE WHEN l.threshold IS NULL THEN s.balance_native * u.value ELSE s.balance_native END AS balance,
  CASE WHEN l.threshold IS NULL THEN s.settlement_currency WHEN s.native_unit = 'usd' THEN 'USD' ELSE s.native_unit END AS unit,
  coalesce(l.threshold, CASE WHEN u.value IS NOT NULL THEN 5 END) AS threshold
 FROM live s LEFT JOIN line l ON l.supplier_id = s.id
 LEFT JOIN LATERAL (SELECT v.value FROM suppliers.unit_values v
  WHERE v.supplier_id = s.id AND v.currency = s.settlement_currency AND v.native_unit = s.native_unit ORDER BY v.id DESC LIMIT 1) u ON true
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
	signals, low := balanceCrossings(readings, p.Low)
	for _, signal := range signals {
		if err = alert.Persist(ctx, tx, uuid.NewV7(), signal); err != nil {
			return err
		}
	}
	return alert.SavePosition(ctx, tx, source, balanceAlertPosition{Initialized: true, Low: low})
}

// balanceCrossings is the signals of the keys whose side of the line differs from what was last reported, and the keys
// low now. A key reported low that is no longer read (removed, disabled, or its supplier paused) has no balance to be
// low, and is resolved.
func balanceCrossings(readings []balanceReading, reportedLow []string) ([]alert.Signal, []string) {
	reported := map[string]bool{}
	for _, key := range reportedLow {
		reported[key] = true
	}
	var signals []alert.Signal
	low := []string{}
	for _, r := range readings {
		was := reported[r.key]
		delete(reported, r.key)
		if r.low {
			low = append(low, r.key)
		}
		if r.low != was {
			signals = append(signals, alert.Signal{Kind: "suppliers.low_balance", Key: r.key, Severity: "warning", Fields: r.fields, Resolved: !r.low})
		}
	}
	gone := make([]string, 0, len(reported))
	for key := range reported {
		gone = append(gone, key)
	}
	slices.Sort(gone)
	for _, key := range gone {
		signals = append(signals, alert.Signal{Kind: "suppliers.low_balance", Key: key, Severity: "warning", Resolved: true})
	}
	return signals, low
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
		// Every field is cut to what a signal holds (platform/alert: 512 bytes a value), so no reading can make Persist
		// refuse it and roll back the whole pass; the supplier's ID names it whole.
		r.fields = map[string]string{
			"supplier": clip(name, 200), "supplier_id": supplier.String(), "balance": clip(balance, 512),
			"threshold": clip(threshold, 512), "unit": unit,
		}
		if hint != nil {
			r.fields["key_hint"] = "…" + *hint
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// clip is s cut to at most n bytes, on a character boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
