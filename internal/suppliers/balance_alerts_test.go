package suppliers

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/store"
)

// walletKey is the pasted key of the world's wallet supplier; only its last four characters may leave its row.
const walletKey = "sk-wallet-secret-wxyz"

// addWallet connects a supplier settled in CNY whose one key reads its own wallet, holding balance yuan.
func addWallet(w *world, balance string) (supplier, credential uuid.UUID, sealed secret.Sealed) {
	w.t.Helper()
	credential = uuid.NewV7()
	sealed = w.keys.Seal(secret.New(walletKey), secret.Location{Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key"})
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		if supplier, err = store.New().InsertSupplier(ctx, tx, store.InsertSupplierParams{
			OwnerScope: "platform", Name: "wallets", Kind: "fake", Origin: "https://wallet.example", UpstreamAccount: "-",
			NativeUnit: "usd", SettlementCurrency: "CNY", MgmtEgressID: "direct",
		}); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE suppliers.suppliers SET status='active' WHERE id=$1`, supplier); err != nil {
			return err
		}
		var group uuid.UUID
		if err = tx.QueryRow(ctx, `INSERT INTO suppliers.supplier_groups(supplier_id,upstream_ref,name,ratio) VALUES($1,'line','line',1) RETURNING id`, supplier).Scan(&group); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO suppliers.credentials(id,supplier_id,supplier_group_id,upstream_key_ref,ciphertext,nonce,wrapped_dek,key_version,status,max_concurrency,hint,balance_native,balance_observed_at)
 VALUES($1,$2,$3,'ref',$4,$5,$6,$7,'active',1,'wxyz',$8,now())`, credential, supplier, group, sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, int64(sealed.KeyVersion), balance)
		return err
	})
	return supplier, credential, sealed
}

// TestLowBalanceAlertsFireOncePerCrossing: a relay's balance and a key's wallet each alert once when they fall under
// their supplier's line, in their own unit, send nothing while they stay on one side, and resolve when they are back
// at or over it (docs/domains/suppliers-direct.md「模型重读与余额」).
func TestLowBalanceAlertsFireOncePerCrossing(t *testing.T) {
	w := newWorld(t, 2)
	wallet, key, _ := addWallet(w, "20")
	relayKey, walletAlert := w.supplier.String(), wallet.String()+"/"+key.String()
	lines := map[uuid.UUID]string{w.supplier: "50"}
	read := LowBalanceLines(func(context.Context, pg.Tx) (map[uuid.UUID]string, error) { return lines, nil })
	exec := func(query string, args ...any) {
		t.Helper()
		w.tx(func(ctx context.Context, tx pg.Tx) error { _, err := tx.Exec(ctx, query, args...); return err })
	}
	type want struct {
		key      string
		resolved bool
		fields   string // balance threshold unit [key hint], for a raise
	}
	steps := []struct {
		name   string
		change func()
		want   []want
	}{
		{"the first run clears what the earlier bridge raised for a relay that is no longer low", func() {
			exec(`UPDATE suppliers.suppliers SET status='low_balance' WHERE id=$1`, w.supplier)
		}, []want{{key: relayKey, resolved: true}}},
		{"balances over their lines send nothing", func() {}, nil},
		{"a relay falling under its line fires once", func() {
			exec(`UPDATE suppliers.suppliers SET balance_native=40 WHERE id=$1`, w.supplier)
		}, []want{{key: relayKey, fields: "40 50 quota"}}},
		{"a relay still under its line does not fire again", func() {
			exec(`UPDATE suppliers.suppliers SET balance_native=30 WHERE id=$1`, w.supplier)
		}, nil},
		{"a wallet in yuan is held to the default line in yuan", func() {
			exec(`UPDATE suppliers.credentials SET balance_native=4.99 WHERE id=$1`, key)
		}, []want{{key: walletAlert, fields: "4.99 5 CNY …wxyz"}}},
		{"a relay back at its line resolves", func() {
			exec(`UPDATE suppliers.suppliers SET balance_native=50 WHERE id=$1`, w.supplier)
		}, []want{{key: relayKey, resolved: true}}},
		{"a relay without a line is held to 5 of its settlement currency at its unit value", func() {
			delete(lines, w.supplier)
			exec(`UPDATE suppliers.suppliers SET balance_native=2.4 WHERE id=$1`, w.supplier)
		}, []want{{key: relayKey, fields: "2.4 2.5 quota"}}},
		{"a line set in the wallet's currency moves the wallet above it", func() {
			lines[wallet] = "4.5"
		}, []want{{key: walletAlert, resolved: true}}},
		{"a line raised over the wallet fires again", func() {
			lines[wallet] = "10"
		}, []want{{key: walletAlert, fields: "4.99 10 CNY …wxyz"}}},
		{"a key that can no longer be called is no longer low", func() {
			exec(`UPDATE suppliers.credentials SET status='disabled' WHERE id=$1`, key)
		}, []want{{key: walletAlert, resolved: true}}},
	}
	for _, step := range steps {
		step.change()
		w.tx(func(ctx context.Context, tx pg.Tx) error { return emitBalanceAlerts(ctx, tx, read) })
		got := consumeSupplierSignals(w)
		if len(got) != len(step.want) {
			t.Fatalf("%s: got %+v, want %+v", step.name, got, step.want)
		}
		for i, s := range got {
			fields := strings.TrimSpace(strings.Join([]string{s.Fields["balance"], s.Fields["threshold"], s.Fields["unit"], s.Fields["key_hint"]}, " "))
			if s.Kind != "suppliers.low_balance" || s.Key != step.want[i].key || s.Resolved != step.want[i].resolved ||
				!s.Resolved && (fields != step.want[i].fields || s.Fields["supplier_id"] == "" || s.Fields["supplier"] == "") {
				t.Errorf("%s: signal %d is %+v, want %+v", step.name, i, s, step.want[i])
			}
		}
	}
}

// TestLowBalanceSignalHoldsNoSecret: what the alert carries of a key, and so every notification made of it, is its last
// four characters, never the key or its sealed form.
func TestLowBalanceSignalHoldsNoSecret(t *testing.T) {
	w := newWorld(t, 2)
	_, _, sealed := addWallet(w, "1")
	w.tx(func(ctx context.Context, tx pg.Tx) error { return emitBalanceAlerts(ctx, tx, nil) })
	var bodies []string
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		rows, err := tx.Query(ctx, `SELECT body::text FROM platform.alert_signals`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body string
			if err = rows.Scan(&body); err != nil {
				return err
			}
			bodies = append(bodies, body)
		}
		return rows.Err()
	})
	if len(bodies) != 1 || !strings.Contains(bodies[0], "…wxyz") {
		t.Fatalf("the wallet's alert: %q", bodies)
	}
	for _, leak := range []string{walletKey, walletKey[:len(walletKey)-4], hex.EncodeToString(sealed.Ciphertext), hex.EncodeToString(sealed.WrappedDEK)} {
		if strings.Contains(bodies[0], leak) {
			t.Errorf("the alert holds %q: %s", leak, bodies[0])
		}
	}
	if s := consumeSupplierSignals(w); len(s) != 1 || s[0].Kind != "suppliers.low_balance" {
		t.Fatalf("signals: %+v", s)
	}
}
