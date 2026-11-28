package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/observability"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers"
)

type supplierAlertPass struct{}

func (supplierAlertPass) Kind() string { return "suppliers.sync_alerts" }
func (supplierAlertPass) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications}
}

type operatorAlertPass struct{}

func (operatorAlertPass) Kind() string { return "observability.alerts" }
func (operatorAlertPass) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications}
}

type supplierAlertTest struct {
	t                *testing.T
	db               *pg.DB
	queue            *pg.Queue
	supplier         uuid.UUID
	firing, recovery atomic.Int32
	eventID          atomic.Value
	received         chan struct{}
}

// TestSupplierLowBalanceSendsOneNoticeAndOneRecovery: a relay's balance falling under the line set for it in the alert
// configuration reaches the channel once, and its rise back over the line once as the same incident's recovery.
func TestSupplierLowBalanceSendsOneNoticeAndOneRecovery(t *testing.T) {
	t.Parallel()
	w := newSupplierAlertTest(t)
	w.enqueue(supplierAlertPass{})
	w.waitLow(false)
	w.balance("40")
	w.enqueue(supplierAlertPass{})
	w.waitLow(true)
	w.enqueue(operatorAlertPass{})
	w.waitNotice()
	w.balance("60")
	w.enqueue(supplierAlertPass{})
	w.waitLow(false)
	w.waitReceipt(0)
	w.enqueue(operatorAlertPass{})
	w.waitNotice()
	w.waitReceipt(-1)
	if w.firing.Load() != 1 || w.recovery.Load() != 1 {
		t.Fatalf("the crossing sent %d notices and %d recoveries", w.firing.Load(), w.recovery.Load())
	}
}

func newSupplierAlertTest(t *testing.T) *supplierAlertTest {
	t.Helper()
	f := newAdminFixture(t)
	w := &supplierAlertTest{t: t, db: f.db, supplier: uuid.NewV7(), received: make(chan struct{}, 4)}
	receiver := httptest.NewTLSServer(http.HandlerFunc(w.receive))
	t.Cleanup(receiver.Close)
	exit := egress.NewForTesting(egresstest.Loopback(t, receiver.Certificate()))
	t.Cleanup(exit.CloseIdleConnections)
	keys := masterKeys(t)
	w.seed(receiver.URL, keys)
	logger := slog.New(slog.DiscardHandler)
	w.queue = w.db.NewQueue(logger, pg.PlatformScope(), nil)
	m, err := mail.Register(t.Context(), w.db, w.queue, mail.Config{}, nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err = suppliers.RegisterSyncAlerts(suppliers.SyncConfig{DB: w.db, Scope: pg.PlatformScope(), Queue: w.queue, LowBalance: observability.LowBalanceLines}); err != nil {
		t.Fatal(err)
	}
	if err = observability.RegisterAlerts(w.db, pg.PlatformScope(), w.queue, m, keys, exit); err != nil {
		t.Fatal(err)
	}
	if err = w.queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := w.queue.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return w
}

func (w *supplierAlertTest) seed(destination string, keys *secret.Keyring) {
	w.t.Helper()
	ctx := w.t.Context()
	w.tx(func(tx pg.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO suppliers.suppliers(id,owner_scope,name,kind,origin,upstream_account,native_unit,settlement_currency,mgmt_egress_id) VALUES($1,'platform','alert source','newapi','https://supplier.example','sync','quota','USD','direct')`, w.supplier); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE suppliers.suppliers SET status='active', balance_native=100 WHERE id=$1`, w.supplier); err != nil {
			return err
		}
		c, err := observability.GetAlertConfiguration(ctx, tx)
		if err != nil {
			return err
		}
		c.Channels = []observability.AlertChannel{{ID: "hook", WebhookURL: destination}}
		c.Rules = []observability.AlertRule{{ID: "balance", Kind: "suppliers.low_balance", MinimumSeverity: "warning", Threshold: 1, Channel: "hook"}}
		c.LowBalanceThresholds = &[]observability.LowBalanceThreshold{{SupplierID: w.supplier, Threshold: "50"}}
		if err = observability.SealAlertAddresses(ctx, tx, keys, &c); err != nil {
			return err
		}
		if err = observability.SetAlertConfiguration(ctx, tx, c); err != nil {
			return err
		}
		_, err = observability.RotateAlertWebhookSecret(ctx, tx, keys, "hook", c.Rev+1, secret.New("receiver test signing value 32 bytes"), time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
		return err
	})
}

func (w *supplierAlertTest) receive(out http.ResponseWriter, r *http.Request) {
	var notice struct {
		Kind    string `json:"kind"`
		State   string `json:"state"`
		EventID string `json:"event_id"`
	}
	if err := jsonx.Decode(r.Body, &notice); err != nil {
		w.t.Error(err)
		out.WriteHeader(http.StatusBadRequest)
		return
	}
	if notice.Kind == "suppliers.low_balance" {
		switch notice.State {
		case "firing":
			w.eventID.Store(notice.EventID)
			w.firing.Add(1)
			w.received <- struct{}{}
		case "resolved":
			if notice.EventID == "" || w.eventID.Load() != notice.EventID {
				w.t.Error("recovery belongs to a different incident")
			}
			w.recovery.Add(1)
			w.received <- struct{}{}
		}
	}
	out.WriteHeader(http.StatusNoContent)
}

func (w *supplierAlertTest) tx(fn func(pg.Tx) error) {
	w.t.Helper()
	if err := w.db.Tx(w.t.Context(), pg.PlatformScope(), fn); err != nil {
		w.t.Fatal(err)
	}
}

func (w *supplierAlertTest) enqueue(args river.JobArgs) {
	w.t.Helper()
	w.tx(func(tx pg.Tx) error {
		_, err := w.queue.InsertManyTx(w.t.Context(), tx, []river.InsertManyParams{{Args: args}})
		return err
	})
}

func (w *supplierAlertTest) balance(amount string) {
	w.t.Helper()
	w.tx(func(tx pg.Tx) error {
		_, err := tx.Exec(w.t.Context(), `UPDATE suppliers.suppliers SET balance_native=$2::text::numeric WHERE id=$1`, w.supplier, amount)
		return err
	})
}

// waitLow waits for the balance alerts' last reading to have the relay on the given side of its line.
func (w *supplierAlertTest) waitLow(low bool) {
	w.t.Helper()
	w.waitQuery(`SELECT EXISTS(SELECT FROM platform.alert_positions WHERE source='suppliers.balance_low' AND (position->'Low' ? $1)=$2)`, w.supplier.String(), low)
}

func (w *supplierAlertTest) waitReceipt(seq int) {
	w.t.Helper()
	w.waitQuery(`SELECT EXISTS(SELECT FROM observability.alert_receipts r JOIN observability.alert_events e ON e.id=r.event_id WHERE e.kind='suppliers.low_balance' AND r.sequence=$1 AND r.state='delivered')`, seq)
}

func (w *supplierAlertTest) waitQuery(query string, args ...any) {
	w.t.Helper()
	// The row is the signal. A loaded machine may take longer to reach this state, so there is no wall-clock deadline:
	// a state that never arrives is a hang the test binary's timeout reports with every goroutine, while a deadline
	// here would fail a correct run (#624).
	for {
		var ok bool
		err := w.db.ReadTx(w.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error { return tx.QueryRow(w.t.Context(), query, args...).Scan(&ok) })
		if err != nil {
			w.t.Fatal(err)
		}
		if ok {
			return
		}
	}
}

func (w *supplierAlertTest) waitNotice() {
	w.t.Helper()
	// The receiver's delivery is the signal, not a wall clock: under load the queue may take longer than the 15 seconds
	// this wait used to allow, and a missing notice is a hang the test binary's timeout reports (#624).
	select {
	case <-w.received:
	case <-w.t.Context().Done():
		w.t.Fatal("no supplier notification")
	}
}
