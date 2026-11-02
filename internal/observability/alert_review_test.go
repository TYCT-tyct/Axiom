package observability

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestAlertRepeatAfterFailedDelivery(t *testing.T) {
	for _, failedSequence := range []int{0, 1} {
		t.Run(strconv.Itoa(failedSequence), func(t *testing.T) {
			db := openDB(t)
			configureAlerts(t, db, AlertRule{Threshold: 1, RepeatSeconds: 3600})
			now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
			persistAlert(t, db, "1", false)
			events := alertCycle(t, db, now)
			worker := alertWorker{db: db, scope: platformScope, now: func() time.Time { return now }}
			if failedSequence == 1 {
				reportAlertReceipt(t, &worker, events[0].Receipts[0].ID, true)
				now = now.Add(time.Hour)
				events = alertCycle(t, db, now)
			}
			reportAlertReceipt(t, &worker, events[0].Receipts[failedSequence].ID, false)
			events = alertCycle(t, db, now.Add(time.Hour-time.Second))
			if len(events[0].Receipts) != failedSequence+1 {
				t.Fatal("early retry")
			}
			events = alertCycle(t, db, now.Add(time.Hour))
			if len(events[0].Receipts) != failedSequence+2 || events[0].Receipts[failedSequence+1].Sequence != failedSequence+1 {
				t.Fatal("failed delivery stopped repeats")
			}
		})
	}
}

func TestWriterLagCreatesOnlyThresholdIncidents(t *testing.T) {
	db := openDB(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	write := func(value string, resolved bool) {
		t.Helper()
		alertTx(t, db, func(tx pg.Tx) error {
			return applyAlert(t.Context(), tx, alert.Signal{Kind: "observability.write_lag", Key: "node", Severity: "warning", Fields: map[string]string{"value": value}, Resolved: resolved}, now)
		})
	}
	for range 10 {
		write("1", false)
		write("0", true)
	}
	if events := alertCycle(t, db, now); len(events) != 0 {
		t.Fatal("normal queue activity created incidents")
	}
	write("4000", false)
	write("1", false)
	events := alertCycle(t, db, now)
	if len(events) != 1 || events[0].Occurrences != 2 || events[0].Fields["value"] != "1" {
		t.Fatal("existing incident did not retain below-threshold sample")
	}
	write("0", true)
	write("1", false)
	events = alertCycle(t, db, now)
	if len(events) != 1 || events[0].State != "resolved" {
		t.Fatal("below-threshold sample reopened incident")
	}
}

func TestAlertReceiptPageGroupsAndBoundsEachIncident(t *testing.T) {
	db := openDB(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	alertTx(t, db, func(tx pg.Tx) error {
		for _, key := range []string{"first", "second", "empty"} {
			if err := applyAlert(t.Context(), tx, alert.Signal{Kind: "test", Key: key, Severity: "warning"}, now); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO observability.alert_receipts(id,event_id,rule_id,sequence,channel,quiet_start,quiet_end,state,at)
 SELECT uuidv7(),e.id,'page',n,'{}',0,0,'failed',$1::timestamptz+make_interval(secs=>n) FROM observability.alert_events e CROSS JOIN generate_series(0,149) n WHERE key<>'empty'`, now)
		return err
	})
	events := alertCycle(t, db, now)
	for _, e := range events {
		if e.Key == "empty" {
			if e.Receipts == nil || len(e.Receipts) != 0 {
				t.Fatal("empty receipts changed shape")
			}
			continue
		}
		if len(e.Receipts) != 100 || e.Receipts[0].Sequence != 50 || e.Receipts[99].Sequence != 149 {
			t.Fatalf("unbounded or mixed receipt page: %+v", e.Receipts)
		}
	}
}

func TestAlertEvaluationExcludesHistoryAndPrunesFinishedIncidents(t *testing.T) {
	db, database := openDBWithURL(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	old, pending, active, recent := seedAlertHistory(t, db, now)

	if err := pgtest.Run(t.Context(), database.Superuser, "ANALYZE observability.alert_events"); err != nil {
		t.Fatal(err)
	}
	assertAlertEvaluationPlan(t, db)

	worker := alertWorker{db: db, scope: platformScope, mail: &mail.Mailer{}, now: func() time.Time { return now }}
	if err := worker.work(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	alertTx(t, db, func(tx pg.Tx) error {
		var total, protected, children int
		if err := tx.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE id=ANY($1::uuid[])) FROM observability.alert_events`, []uuid.UUID{pending, active, recent}).Scan(&total, &protected); err != nil {
			return err
		}
		if total != 19904 || protected != 3 {
			t.Fatalf("retention total=%d protected=%d", total, protected)
		}
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM observability.alert_receipts WHERE event_id=$1)+(SELECT count(*) FROM observability.alert_windows WHERE event_id=$1)`, old).Scan(&children); err != nil {
			return err
		}
		if children != 0 {
			t.Fatal("expired incident retained children")
		}
		return nil
	})
}

func TestAlertConfigurationReplacementWaitsForEvaluation(t *testing.T) {
	db := openDB(t)
	configureAlerts(t, db, AlertRule{Threshold: 1, DurationSeconds: 60})
	now := time.Now()
	persistAlert(t, db, "1", false)
	events := alertCycle(t, db, now)
	release, locked := make(chan struct{}), make(chan struct{})
	unlock := sync.OnceFunc(func() { close(release) })
	defer unlock()
	held := make(chan error, 1)
	go func() {
		held <- db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
			if err := evaluateAlerts(t.Context(), tx, now); err != nil {
				return err
			}
			close(locked)
			<-release
			_, err := tx.Exec(t.Context(), `INSERT INTO observability.alert_windows(event_id,rule_id,since) VALUES($1,'late',$2)`, events[0].ID, now)
			return err
		})
	}()
	select {
	case <-locked:
	case err := <-held:
		t.Fatal(err)
	}
	changed := make(chan error, 1)
	go func() {
		changed <- db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
			c, err := GetAlertConfiguration(t.Context(), tx)
			if err != nil {
				return err
			}
			c.Rules[0].DurationSeconds = 120
			return SetAlertConfiguration(t.Context(), tx, c)
		})
	}()
	eventually(t, "configuration update to wait on the evaluation row lock", func() bool {
		var waiting bool
		alertTx(t, db, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'UPDATE observability.alert_configuration%')`).Scan(&waiting)
		})
		return waiting
	})
	unlock()
	for _, done := range []chan error{held, changed} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	alertTx(t, db, func(tx pg.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM observability.alert_windows`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("old evaluation left windows after configuration replacement")
		}
		return nil
	})
}

func seedAlertHistory(t *testing.T, db *pg.DB, now time.Time) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	old, pending, active, recent := uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	alertTx(t, db, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO observability.alert_events(id,kind,key,severity,fields,state,fired_at,resolved_at)
 SELECT uuidv7(),'history',n::text,'info','{}','resolved',$1::timestamptz,$1 FROM generate_series(1,20000) n`, now.Add(-31*24*time.Hour))
		if err != nil {
			return err
		}
		for _, id := range []uuid.UUID{old, pending, active, recent} {
			if err = applyAlert(t.Context(), tx, alert.Signal{Kind: "test", Key: id.String(), Severity: "warning"}, now); err != nil {
				return err
			}
			if _, err = tx.Exec(t.Context(), `UPDATE observability.alert_events SET id=$1 WHERE key=$2`, id, id.String()); err != nil {
				return err
			}
		}
		_, err = tx.Exec(t.Context(), `UPDATE observability.alert_events SET resolved_at=CASE WHEN id=$1 THEN $3::timestamptz ELSE $4::timestamptz END,state='resolved',pending_recovery=(id=$2) WHERE id<>$5 AND kind='test'`, recent, pending, now.Add(-time.Hour), now.Add(-60*24*time.Hour), active)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO observability.alert_receipts(id,event_id,rule_id,sequence,channel,quiet_start,quiet_end,state,at) SELECT uuidv7(),id,'test',0,'{}',0,0,CASE WHEN id=$1 THEN 'queued' ELSE 'failed' END,$3 FROM observability.alert_events WHERE id IN ($1,$2)`, pending, old, now)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO observability.alert_windows(event_id,rule_id,since) VALUES($1,'test',$2)`, old, now)
		return err
	})
	return old, pending, active, recent
}

func assertAlertEvaluationPlan(t *testing.T, db *pg.DB) {
	t.Helper()
	alertTx(t, db, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE,BUFFERS) "+alertEvaluation)
		if err != nil {
			return err
		}
		var lines []string
		for rows.Next() {
			var line string
			if err = rows.Scan(&line); err != nil {
				break
			}
			lines = append(lines, line)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		plan := strings.Join(lines, "\n")
		if !strings.Contains(plan, "alert_evaluate") || buffers(t, plan) > 100 {
			t.Fatalf("history entered evaluation scan:\n%s", plan)
		}
		return nil
	})
}

func TestAlertDispatchClaimsExcludeAnotherTransaction(t *testing.T) {
	db := openDB(t)
	configureAlerts(t, db, AlertRule{Threshold: 1})
	now := time.Now()
	persistAlert(t, db, "1", false)
	alertCycle(t, db, now)
	alertTx(t, db, func(tx pg.Tx) error {
		first, err := pendingAlertDeliveries(t.Context(), tx, now)
		if err != nil {
			return err
		}
		if len(first) != 1 {
			t.Fatal("first dispatcher did not claim the pending receipt")
		}
		return db.Tx(t.Context(), platformScope, func(other pg.Tx) error {
			second, err := pendingAlertDeliveries(t.Context(), other, now)
			if err == nil && len(second) != 0 {
				t.Fatal("both dispatchers claimed the same receipt")
			}
			return err
		})
	})
}

func TestSecuritySpikeUsesConfiguredThresholdAndRecovers(t *testing.T) {
	db := openDB(t)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	alertTx(t, db, func(tx pg.Tx) error {
		c, err := GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		c.Channels = []AlertChannel{{ID: "ops", Email: "ops@example.com"}}
		c.Rules = []AlertRule{{ID: "auth", Kind: "accounts.authentication_failures", MinimumSeverity: "warning", Threshold: 20, Channel: "ops"}}
		return SetAlertConfiguration(t.Context(), tx, c)
	})
	write := func(value string, resolved bool) {
		t.Helper()
		alertTx(t, db, func(tx pg.Tx) error {
			return alert.Persist(t.Context(), tx, uuid.NewV7(), alert.Signal{Kind: "accounts.authentication_failures", Key: "site-test", Severity: "warning", Fields: map[string]string{"value": value}, Resolved: resolved})
		})
	}
	write("19", false)
	if got := alertCycle(t, db, now); len(got) != 0 {
		t.Fatal("low failure rate created incident", got)
	}
	write("20", false)
	write("21", false)
	got := alertCycle(t, db, now)
	if len(got) != 1 || got[0].Occurrences != 2 || len(got[0].Receipts) != 1 {
		t.Fatal("spike missing or duplicate notices", got)
	}
	write("0", true)
	got = alertCycle(t, db, now)
	if len(got) != 1 || got[0].State != "resolved" {
		t.Fatal("not recovered", got)
	}
}

//nolint:cyclop,gocognit // one batch with a high window and either a low window or a recovery is the point.
func TestPaymentCompletedWindowsSurviveConsumerDelay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		next     string
		resolved bool
	}{
		{name: "low value", next: "1"},
		{name: "recovery", resolved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
			alertTx(t, db, func(tx pg.Tx) error {
				c, err := GetAlertConfiguration(t.Context(), tx)
				if err != nil {
					return err
				}
				c.Channels = []AlertChannel{{ID: "ops", Email: "ops@example.com"}}
				c.Rules = []AlertRule{{ID: "verify", Kind: "payments.verify_failed", MinimumSeverity: "warning", Threshold: 10, Channel: "ops"}}
				return SetAlertConfiguration(t.Context(), tx, c)
			})
			alertTx(t, db, func(tx pg.Tx) error {
				if err := alert.Persist(t.Context(), tx, uuid.NewV7(), alert.Signal{Kind: "payments.verify_failed", Key: "node/callbacks", Severity: "warning", Fields: map[string]string{"value": "12"}}); err != nil {
					return err
				}
				next := alert.Signal{Kind: "payments.verify_failed", Key: "node/callbacks", Severity: "warning", Resolved: tc.resolved}
				if !tc.resolved {
					next.Fields = map[string]string{"value": tc.next}
				}
				return alert.Persist(t.Context(), tx, uuid.NewV7(), next)
			})
			got := alertCycle(t, db, now)
			if len(got) != 1 || len(got[0].Receipts) != 1 || got[0].Receipts[0].Sequence != 0 {
				t.Fatalf("completed window was lost: %+v", got)
			}
			eventValue := "1"
			if tc.resolved {
				eventValue = "12"
			}
			if got[0].Fields["value"] != eventValue {
				t.Fatalf("event did not keep the current sample: %+v", got[0])
			}
			var raw []byte
			alertTx(t, db, func(tx pg.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT fields FROM observability.alert_receipts WHERE event_id=$1 AND sequence=0`, got[0].ID).Scan(&raw)
			})
			var display map[string]string
			if err := jsonx.Unmarshal(raw, &display); err != nil || display["value"] != "12" {
				t.Fatalf("receipt did not freeze the burst: %s %v", raw, err)
			}
			if tc.resolved && got[0].State != "resolved" {
				t.Fatalf("recovery did not resolve: %+v", got[0])
			}
		})
	}
}

func TestPaymentLowWindowResetsDuration(t *testing.T) {
	db := openDB(t)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	alertTx(t, db, func(tx pg.Tx) error {
		c, err := GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		c.Channels = []AlertChannel{{ID: "ops", Email: "ops@example.com"}}
		c.Rules = []AlertRule{{ID: "verify", Kind: "payments.verify_failed", MinimumSeverity: "warning", Threshold: 10, DurationSeconds: 60, Channel: "ops"}}
		return SetAlertConfiguration(t.Context(), tx, c)
	})
	write := func(value string, at time.Time) []AlertEvent {
		t.Helper()
		alertTx(t, db, func(tx pg.Tx) error {
			return alert.Persist(t.Context(), tx, uuid.NewV7(), alert.Signal{Kind: "payments.verify_failed", Key: "node/callbacks", Severity: "warning", Fields: map[string]string{"value": value}})
		})
		return alertCycle(t, db, at)
	}
	got := write("12", now)
	if len(got) != 1 || len(got[0].Receipts) != 0 {
		t.Fatalf("duration fired early: %+v", got)
	}
	got = write("1", now.Add(time.Minute))
	if len(got) != 1 || len(got[0].Receipts) != 0 {
		t.Fatalf("low window kept the historical duration: %+v", got)
	}
}

func TestSecurityCompletedSamplesSurviveConsumerDelay(t *testing.T) {
	for _, next := range []string{"1", "0"} {
		t.Run(next, func(t *testing.T) {
			db := openDB(t)
			now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
			alertTx(t, db, func(tx pg.Tx) error {
				c, err := GetAlertConfiguration(t.Context(), tx)
				if err != nil {
					return err
				}
				c.Channels = []AlertChannel{{ID: "ops", Email: "ops@example.com"}}
				c.Rules = []AlertRule{{ID: "auth", Kind: "accounts.authentication_failures", MinimumSeverity: "warning", Threshold: 20, Channel: "ops"}}
				return SetAlertConfiguration(t.Context(), tx, c)
			})
			for _, value := range []string{"30", next} {
				alertTx(t, db, func(tx pg.Tx) error {
					return alert.Persist(t.Context(), tx, uuid.NewV7(), alert.Signal{Kind: "accounts.authentication_failures", Key: "site", Severity: "warning", Fields: map[string]string{"value": value}, Resolved: value == "0"})
				})
			}
			got := alertCycle(t, db, now)
			if len(got) != 1 || got[0].State != "resolved" || len(got[0].Receipts) != 1 || got[0].Receipts[0].Sequence != 0 {
				t.Fatal("completed spike overwritten before notification", got)
			}
		})
	}
}
