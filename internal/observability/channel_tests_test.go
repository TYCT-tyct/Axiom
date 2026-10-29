package observability

import (
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
)

func TestPeriodicChannelTestsDeduplicateAndIgnoreStaleResults(t *testing.T) {
	db := openDB(t)
	configureAlerts(t, db, AlertRule{Threshold: 1})
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error { return scheduleAlertTests(t.Context(), tx, now) }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	latest := func() uuid.UUID {
		t.Helper()
		var id uuid.UUID
		alertTx(t, db, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT receipt_id FROM observability.channel_tests WHERE channel_id='ops'`).Scan(&id)
		})
		return id
	}
	first := latest()
	alertTx(t, db, func(tx pg.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM observability.alert_receipts WHERE rule_id='test'`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("%d tests scheduled concurrently", n)
		}
		return scheduleAlertTests(t.Context(), tx, now.Add(2*time.Hour))
	})
	if latest() != first {
		t.Fatal("stacked pending test")
	}
	alertTx(t, db, func(tx pg.Tx) error { return recordAlertReceipt(t.Context(), tx, first, false, now) })

	if channelHealth(t, db, now) != "firing" {
		t.Fatal("failed test did not alert")
	}
	alertTx(t, db, func(tx pg.Tx) error { return scheduleAlertTests(t.Context(), tx, now.Add(time.Hour-time.Second)) })
	if latest() != first {
		t.Fatal("early scheduled test")
	}
	now = now.Add(time.Hour)
	alertTx(t, db, func(tx pg.Tx) error { return scheduleAlertTests(t.Context(), tx, now) })
	second := latest()
	if second == first {
		t.Fatal("no new periodic test")
	}
	alertTx(t, db, func(tx pg.Tx) error { return recordAlertReceipt(t.Context(), tx, second, true, now) })
	alertTx(t, db, func(tx pg.Tx) error { return recordAlertReceipt(t.Context(), tx, first, false, now) })
	if channelHealth(t, db, now) != "resolved" {
		t.Fatal("stale failure reopened healthy channel")
	}
	// The reverse ordering must not let an old success hide a newer failed probe.
	now = now.Add(time.Hour)
	alertTx(t, db, func(tx pg.Tx) error { return scheduleAlertTests(t.Context(), tx, now) })
	third := latest()
	alertTx(t, db, func(tx pg.Tx) error { return recordAlertReceipt(t.Context(), tx, third, false, now) })
	alertTx(t, db, func(tx pg.Tx) error { return recordAlertReceipt(t.Context(), tx, first, true, now) })
	if channelHealth(t, db, now) != "firing" {
		t.Fatal("stale success hid a new failure")
	}
}

func TestAbandonedWebhookReceiptAndRescuerFailureRemainVisible(t *testing.T) {
	db := openDB(t)
	keys := webhookTestKeys(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	configureWebhook(t, db, "https://receiver.example/hook", keys, now)
	q := db.NewQueue(quiet, platformScope, nil)
	m, err := mail.Register(t.Context(), db, q, mail.Config{}, nil, nil, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err = RegisterAlerts(db, platformScope, q, m, keys, nil); err != nil {
		t.Fatal(err)
	}
	alertTx(t, db, func(tx pg.Tx) error { return scheduleAlertTests(t.Context(), tx, now) })
	var id uuid.UUID
	alertTx(t, db, func(tx pg.Tx) error {
		if err := tx.QueryRow(t.Context(), `UPDATE observability.alert_receipts SET state='queued' WHERE rule_id='test' RETURNING id`).Scan(&id); err != nil {
			return err
		}
		return repairAlertReceipts(t.Context(), tx, now.Add(25*time.Hour))
	})
	assertAbandonedReceipt(t, db, id, now)
	alertTx(t, db, func(tx pg.Tx) error {
		return q.OnDiscard(t.Context(), tx, "observability.webhook", []byte(`{"receipt":"`+id.String()+`"}`))
	})
	events := alertCycle(t, db, now)
	var discard bool
	for _, e := range events {
		discard = discard || e.Kind == "queue.discarded"
	}
	if !discard {
		t.Fatal("rescued discard lost signal")
	}
}

func TestWebhookKeysAndProbeRowsArePlatformOnly(t *testing.T) {
	db := openDB(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	configureWebhook(t, db, "https://receiver.example/hook", webhookTestKeys(t), now)
	alertTx(t, db, func(tx pg.Tx) error { return scheduleAlertTests(t.Context(), tx, now) })
	alertTx(t, db, func(tx pg.Tx) error { _, err := alert.LockPosition(t.Context(), tx, "rls-check"); return err })
	if err := db.ReadTx(t.Context(), accountScope(uuid.NewV7()), func(tx pg.Tx) error {
		for _, table := range []string{"observability.webhook_keys", "observability.channel_tests", "platform.alert_positions"} {
			var n int
			if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Error("tenant read platform secrets/probes")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Check(t.Context(), Module()); err != nil {
		t.Fatal(err)
	}
}

func channelHealth(t *testing.T, db *pg.DB, now time.Time) string {
	t.Helper()
	for _, e := range alertCycle(t, db, now) {
		if e.Kind == "alert.channel_unavailable" {
			return e.State
		}
	}
	return ""
}

func assertAbandonedReceipt(t *testing.T, db *pg.DB, id uuid.UUID, now time.Time) {
	t.Helper()
	events := alertCycle(t, db, now)
	var health bool
	for _, e := range events {
		if e.Kind == "alert.channel_unavailable" {
			health = true
		}
		for _, r := range e.Receipts {
			if r.ID == id && r.State != "failed" {
				t.Fatal("orphan remained queued")
			}
		}
	}
	if !health {
		t.Fatal("lost job hid failed channel")
	}
}
