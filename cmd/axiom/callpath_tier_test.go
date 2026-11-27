package main

import (
	"net/http"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// TestTiersPayTheirOwnPrice: one Messages call, made by an account on the site's default tier and by an account on a
// tier whose rule multiplies the price for all by 1.2, is charged the site's price to the first and a fifth more to
// the second, through admission, the supplier's reply and settlement (docs/domains/billing.md「定价」).
func TestTiersPayTheirOwnPrice(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	pro := uuid.NewV7()
	account := w.text("SELECT account_id::text FROM accounts.users WHERE email = 'broke@example.com'")
	if err := w.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO accounts.tiers (id, site_id, name) VALUES ($1, $2, 'Pro')`, pro, w.site); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE accounts.accounts SET tier_id = $1 WHERE id = $2`, pro, uuid.MustParse(account)); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO billing.pricing_rules
			(id, site_id, enabled, target_kind, target_id, audience_kind, audience_id, method, constraints)
			VALUES ($1, $2, true, 'all', '', 'tier', $3, '{"kind":"default_multiplier","multiplier":"1.2"}'::jsonb, '{}'::jsonb)`,
			uuid.NewV7(), w.site, accounts.TierID(pro).TypeID())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	proKey := w.fundedKey("broke@example.com", 10_000_000_000)
	w.price()
	n := w.start()
	waitUntil(t, 15*time.Second, func() bool { return n.ready(t) })
	for _, c := range []struct {
		name, key string
		charge    int64
	}{
		{"the default tier", w.richKey, messagesCharge},
		{"a tier that pays a fifth more", proKey, messagesCharge * 6 / 5},
	} {
		r := w.call(n, "/v1/messages", c.key, messagesCall)
		if r.status != http.StatusOK {
			t.Fatalf("%s: status %d: %s", c.name, r.status, r.body)
		}
		w.checkSettled(r, c.charge)
	}
	w.checkBalanced()
	if err := n.stop(); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}
