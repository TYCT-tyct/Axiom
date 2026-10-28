package observability

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
)

func alertTx(t *testing.T, db *pg.DB, fn func(pg.Tx) error) {
	t.Helper()
	if err := db.Tx(t.Context(), platformScope, fn); err != nil {
		t.Fatal(err)
	}
}

func configureAlerts(t *testing.T, db *pg.DB, rule AlertRule) {
	t.Helper()
	alertTx(t, db, func(tx pg.Tx) error {
		c, err := GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		c.Channels = []AlertChannel{{ID: "ops", Email: "ops@example.com"}}
		rule.ID = "first"
		rule.Kind = "test.failure"
		rule.Channel = "ops"
		rule.MinimumSeverity = "warning"
		c.Rules = []AlertRule{rule}
		return SetAlertConfiguration(t.Context(), tx, c)
	})
}

func alertCycle(t *testing.T, db *pg.DB, now time.Time) []AlertEvent {
	t.Helper()
	var events []AlertEvent
	alertTx(t, db, func(tx pg.Tx) error {
		if err := alert.Consume(t.Context(), tx, func(s alert.Signal) error { return applyAlert(t.Context(), tx, s, now) }); err != nil {
			return err
		}
		if err := evaluateAlerts(t.Context(), tx, now); err != nil {
			return err
		}
		var err error
		events, err = ListAlertEvents(t.Context(), tx, uuid.Nil())
		return err
	})
	return events
}

func persistAlert(t *testing.T, db *pg.DB, value string, resolved bool) {
	t.Helper()
	alertTx(t, db, func(tx pg.Tx) error {
		return alert.Persist(t.Context(), tx, uuid.NewV7(), alert.Signal{Kind: "test.failure", Key: "same", Severity: "critical", Fields: map[string]string{"value": value}, Resolved: resolved})
	})
}

func TestAlertStormAcrossNodesAndRecovery(t *testing.T) {
	db, urls := openDBWithURL(t)
	other := openAlertPeer(t, urls.App)
	configureAlerts(t, db, AlertRule{Threshold: 1})
	raiseAlertStorm(t, db, other)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	events := alertCycle(t, db, now)
	if len(events) != 1 || events[0].Occurrences != 1000 || len(events[0].Receipts) != 1 {
		t.Fatalf("storm: %+v", events)
	}
	worker := alertWorker{db: db, scope: platformScope, now: func() time.Time { return now }}
	reportAlertReceipt(t, &worker, events[0].Receipts[0].ID, true)
	events = alertCycle(t, db, now)
	if events[0].State != "delivered" || len(events[0].Receipts) != 1 {
		t.Fatalf("delivered: %+v", events)
	}
	persistAlert(t, db, "1", true)
	events = alertCycle(t, db, now.Add(time.Minute))
	if events[0].State != "resolved" || len(events[0].Receipts) != 2 || events[0].Receipts[1].Sequence != -1 {
		t.Fatalf("recovery: %+v", events)
	}
	events = alertCycle(t, db, now.Add(2*time.Minute))
	if len(events[0].Receipts) != 2 {
		t.Fatal("duplicate recovery")
	}
	persistAlert(t, db, "1", false)
	events = alertCycle(t, db, now.Add(3*time.Minute))
	if len(events) != 2 || len(events[0].Receipts) != 1 {
		t.Fatal("new incident did not notify")
	}
}

func TestAlertThresholdDurationRepeatAndRuleRemoval(t *testing.T) {
	db := openDB(t)
	configureAlerts(t, db, AlertRule{Threshold: 2, DurationSeconds: 60, RepeatSeconds: 60})
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	persistAlert(t, db, "1", false)
	alertCycle(t, db, now)
	persistAlert(t, db, "2", false)
	alertCycle(t, db, now.Add(time.Minute))
	events := alertCycle(t, db, now.Add(119*time.Second))
	if len(events[0].Receipts) != 0 {
		t.Fatal("duration began below threshold")
	}
	now = now.Add(120 * time.Second)
	events = alertCycle(t, db, now)
	if len(events[0].Receipts) != 1 {
		t.Fatal("duration did not fire")
	}
	worker := alertWorker{db: db, scope: platformScope, now: func() time.Time { return now }}
	reportAlertReceipt(t, &worker, events[0].Receipts[0].ID, true)
	events = alertCycle(t, db, now.Add(59*time.Second))
	if len(events[0].Receipts) != 1 {
		t.Fatal("early repeat")
	}
	events = alertCycle(t, db, now.Add(time.Minute))
	if len(events[0].Receipts) != 2 {
		t.Fatal("missing repeat")
	}
	if err := worker.mailReceipt(t.Context(), events[0].Receipts[1].ID, true); err != nil {
		t.Fatal(err)
	}
	alertTx(t, db, func(tx pg.Tx) error {
		c, err := GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		c.Rules = nil
		if err = SetAlertConfiguration(t.Context(), tx, c); err != nil {
			return err
		}
		return ResolveAlertEvent(t.Context(), tx, events[0].ID, now)
	})
	events = alertCycle(t, db, now.Add(2*time.Minute))
	if len(events[0].Receipts) != 3 {
		t.Fatal("removing a rule lost recovery")
	}
}

func TestAlertQuietHours(t *testing.T) {
	for _, tc := range []struct {
		hour, minute, start, end int
		want                     time.Duration
	}{
		{12, 0, 0, 0, 0}, {12, 0, 11 * 60, 13 * 60, time.Hour}, {23, 30, 22 * 60, 7 * 60, 7*time.Hour + 30*time.Minute}, {6, 0, 22 * 60, 7 * 60, time.Hour}, {7, 0, 22 * 60, 7 * 60, 0},
	} {
		now := time.Date(2026, 10, 28, tc.hour, tc.minute, 0, 0, time.UTC)
		if got := alertQuiet(now, tc.start, tc.end); got != tc.want {
			t.Errorf("%+v got %v", tc, got)
		}
	}
}

func TestAlertConsumeRollbackAndQuarantine(t *testing.T) {
	db := openDB(t)
	persistAlert(t, db, "1", false)
	sentinel := errors.New("database unavailable")
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return alert.Consume(t.Context(), tx, func(alert.Signal) error { return sentinel })
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	alertTx(t, db, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO platform.alert_signals(id,body) VALUES($1,'{}')`, uuid.NewV7())
		return err
	})
	events := alertCycle(t, db, time.Now())
	if len(events) != 1 || events[0].Occurrences != 1 {
		t.Fatal("rollback lost or duplicated signal")
	}
	alertTx(t, db, func(tx pg.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM platform.alert_signals WHERE quarantined`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("poison signal not quarantined")
		}
		return nil
	})
}

func TestAlertConfigurationAndScope(t *testing.T) {
	db := openDB(t)
	configureAlerts(t, db, AlertRule{Threshold: 1})
	persistAlert(t, db, "1", false)
	alertCycle(t, db, time.Now())
	alertTx(t, db, func(tx pg.Tx) error {
		c, err := GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		old := c.Rev
		if err = SetAlertConfiguration(t.Context(), tx, c); err != nil {
			return err
		}
		if err = SetAlertConfiguration(t.Context(), tx, c); !errors.Is(err, ErrAlertRevision) {
			t.Fatal("stale update accepted", err)
		}
		c, err = GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		if c.Rev != old+1 {
			t.Fatal("wrong revision")
		}
		_, err = SendAlertTest(t.Context(), tx, "ops", time.Now())
		return err
	})
	if err := db.ReadTx(t.Context(), accountScope(uuid.NewV7()), func(tx pg.Tx) error {
		events, err := ListAlertEvents(t.Context(), tx, uuid.Nil())
		if len(events) != 0 {
			t.Fatal("tenant read platform events")
		}
		if _, err := GetAlertConfiguration(t.Context(), tx); !errors.Is(err, pg.ErrNoRows) {
			t.Fatal("tenant read configuration", err)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAlertFailedReceiptDoesNotClaimRecoveryDelivery(t *testing.T) {
	db := openDB(t)
	configureAlerts(t, db, AlertRule{Threshold: 1})
	now := time.Now()
	persistAlert(t, db, "1", false)
	events := alertCycle(t, db, now)
	worker := alertWorker{db: db, scope: platformScope, now: func() time.Time { return now }}
	if err := worker.mailReceipt(context.Background(), events[0].Receipts[0].ID, false); err != nil {
		t.Fatal(err)
	}
	persistAlert(t, db, "1", true)
	events = alertCycle(t, db, now)
	if len(events[0].Receipts) != 2 || events[0].Receipts[1].State != "failed" {
		t.Fatalf("failed recovery: %+v", events)
	}
}

func raiseAlertStorm(t *testing.T, db, other *pg.DB) {
	t.Helper()
	left, right := alert.New(t.Context(), db, platformScope, quiet), alert.New(t.Context(), other, platformScope, quiet)
	for i := range 1000 {
		bus := left
		if i%2 == 1 {
			bus = right
		}
		if err := bus.Raise("test.failure", "same", "critical", map[string]string{"value": "1"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, bus := range []*alert.Bus{left, right} {
		if err := bus.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func reportAlertReceipt(t *testing.T, worker *alertWorker, id uuid.UUID, sent bool) {
	t.Helper()
	if err := worker.mailReceipt(t.Context(), id, sent); err != nil {
		t.Fatal(err)
	}
}

func openAlertPeer(t *testing.T, url string) *pg.DB {
	t.Helper()
	db, err := pg.Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestAlertQuietDispatchAndDisabledMail(t *testing.T) {
	db := openDB(t)
	configureAlerts(t, db, AlertRule{Threshold: 1, QuietStart: 22 * 60, QuietEnd: 7 * 60})
	now := time.Date(2026, 10, 28, 23, 0, 0, 0, time.UTC)
	persistAlert(t, db, "1", false)
	alertCycle(t, db, now)
	q := db.NewQueue(quiet, platformScope, nil)
	m, err := mail.Register(t.Context(), db, q, mail.Config{}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	worker := alertWorker{db: db, scope: platformScope, mail: m, now: func() time.Time { return now }}
	alertTx(t, db, func(tx pg.Tx) error { return worker.dispatch(t.Context(), tx, now) })
	events := alertCycle(t, db, now)
	if events[0].Receipts[0].State != "pending" {
		t.Fatal("quiet receipt dispatched")
	}
	now = now.Add(8 * time.Hour)
	alertTx(t, db, func(tx pg.Tx) error { return worker.dispatch(t.Context(), tx, now) })
	events = alertCycle(t, db, now)
	if events[0].Receipts[0].State != "failed" {
		t.Fatal("disabled mail blocked processing")
	}
}

func TestRescuedMailDiscardPersistsFailedReceiptAndSignal(t *testing.T) {
	db := openDB(t)
	configureAlerts(t, db, AlertRule{Threshold: 1})
	persistAlert(t, db, "1", false)
	events := alertCycle(t, db, time.Now())
	q := db.NewQueue(quiet, platformScope, nil)
	m, err := mail.Register(t.Context(), db, q, mail.Config{}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err = RegisterAlerts(db, platformScope, q, m); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"receipt":"` + events[0].Receipts[0].ID.String() + `"}`)
	alertTx(t, db, func(tx pg.Tx) error { return q.OnDiscard(t.Context(), tx, "mail.send_tracked", raw) })
	events = alertCycle(t, db, time.Now())
	if len(events) != 2 {
		t.Fatal("discard signal lost")
	}
	var failed bool
	for _, e := range events {
		if e.Kind == "test.failure" {
			failed = e.Receipts[0].State == "failed"
		}
	}
	if !failed {
		t.Fatal("rescued mail receipt not failed")
	}
}
