package observability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
)

func webhookTestKeys(t *testing.T) *secret.Keyring {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master")
	if err := os.WriteFile(path, []byte("1:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := secret.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func configureWebhook(t *testing.T, db *pg.DB, destination string, keys *secret.Keyring, now time.Time) AlertChannel {
	t.Helper()
	ch := AlertChannel{ID: "hook", WebhookURL: destination}
	alertTx(t, db, func(tx pg.Tx) error {
		c, err := GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		c.Channels = []AlertChannel{ch}
		c.Rules = []AlertRule{{ID: "hook-rule", Kind: "test.failure", Channel: ch.ID, MinimumSeverity: "warning", Threshold: 1}}
		if err = SetAlertConfiguration(t.Context(), tx, c); err != nil {
			return err
		}
		_, err = RotateAlertWebhookSecret(t.Context(), tx, keys, ch.ID, c.Rev+1, secret.New(strings.Repeat("s", 32)), now)
		return err
	})
	return ch
}

// A receiver verifies the raw body independently, rather than trusting the sender's signature helper.
func receiverAccepts(body []byte, header, key string) bool {
	parts := strings.Split(header, ",")
	stamp, ok := strings.CutPrefix(parts[0], "t=")
	if !ok {
		return false
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(stamp + "."))
	mac.Write(body)
	for _, part := range parts[1:] {
		raw, ok := strings.CutPrefix(part, "v1=")
		if !ok {
			continue
		}
		got, err := hex.DecodeString(raw)
		if err == nil && hmac.Equal(got, mac.Sum(nil)) {
			return true
		}
	}
	return false
}

type webhookClock struct{ at time.Time }

func (c *webhookClock) Now() time.Time       { return c.at }
func (c *webhookClock) NowOrNil() *time.Time { return &c.at }

func TestWebhookRetriesThroughRiverAndFreezesItsNotice(t *testing.T) {
	db, database := openDBWithURL(t)
	keys := webhookTestKeys(t)
	clock := &webhookClock{at: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	receiver := newWebhookReceiver(t)
	server := receiver.server
	exit := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	defer exit.CloseIdleConnections()
	configureWebhook(t, db, server.URL, keys, clock.Now())
	a := &alertWorker{db: db, scope: platformScope, now: clock.Now}
	worker := &webhookWorker{alertWorker: a, keys: keys, exit: exit}
	persistAlert(t, db, "1", false)
	events := alertCycle(t, db, clock.Now())
	id := events[0].Receipts[0].ID
	alertTx(t, db, func(tx pg.Tx) error {
		deliveries, err := pendingAlertDeliveries(t.Context(), tx, clock.Now())
		if err != nil {
			return err
		}
		body, err := webhookPayload(deliveries[0])
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE observability.alert_receipts SET state='queued',payload=$2 WHERE id=$1`, id, body)
		return err
	})
	args := webhookArgs{Receipt: id}
	first, err := pgtest.WorkJob(t, database, river.WorkFunc(worker.work), args, nil, clock)
	assertWebhookBackoff(t, first.Job, err, clock.Now())
	clock.at = clock.at.Add(6 * time.Minute)
	alertTx(t, db, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE observability.alert_events SET fields='{"value":"changed"}' WHERE kind='test.failure'`)
		return err
	})
	second, err := pgtest.WorkJob(t, database, river.WorkFunc(worker.work), args, first.Job, clock)
	if err != nil || second.Job.State != rivertype.JobStateCompleted || second.Job.Attempt != 2 {
		t.Fatalf("retry: %v %+v", err, second)
	}
	if err = worker.work(t.Context(), &river.Job[webhookArgs]{JobRow: &rivertype.JobRow{Attempt: 3, MaxAttempts: 5}, Args: args}); err != nil {
		t.Fatal(err)
	}
	receiver.assertDelivery(t)
	events = alertCycle(t, db, clock.Now())
	if events[0].State != "delivered" || events[0].Receipts[0].State != "delivered" {
		t.Fatalf("no receipt: %+v", events)
	}
}

func TestWebhookSignatureRotationAndDestinationIsolation(t *testing.T) {
	db := openDB(t)
	keys := webhookTestKeys(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	ch := configureWebhook(t, db, "https://receiver.example/hook", keys, now)
	alertTx(t, db, func(tx pg.Tx) error {
		c, err := GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		_, err = RotateAlertWebhookSecret(t.Context(), tx, keys, ch.ID, c.Rev, secret.New(strings.Repeat("n", 32)), now)
		return err
	})
	for _, tc := range []struct {
		at  time.Time
		old bool
	}{{now.Add(time.Hour - time.Second), true}, {now.Add(time.Hour), false}} {
		alertTx(t, db, func(tx pg.Tx) error {
			values, err := readWebhookKeys(t.Context(), tx, keys, ch, tc.at)
			if err != nil {
				return err
			}
			assertRotationSignature(t, values, tc.at, tc.old)
			other := ch
			other.WebhookURL = "https://other.example/hook"
			values, err = readWebhookKeys(t.Context(), tx, keys, other, tc.at)
			if len(values) != 0 {
				t.Error("key crossed destination")
			}
			return err
		})
	}
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		c, err := GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		_, err = RotateAlertWebhookSecret(t.Context(), tx, keys, ch.ID, c.Rev, secret.New(strings.Repeat("z", 32)), now)
		return err
	})
	if !errors.Is(err, ErrAlertRotationWindow) {
		t.Fatalf("overlapping rotation: %v", err)
	}
}

func TestWebhookRefusesPrivateAddressesAndRedirects(t *testing.T) {
	var redirected atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			redirected.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Location", "/final")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, url string
		exit      *egress.Client
	}{
		{"private", "https://127.0.0.1/hook", func() *egress.Client {
			c, err := egress.New(egress.Direct)
			if err != nil {
				t.Fatal(err)
			}
			return c
		}()},
		{"redirect", server.URL, egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer tc.exit.CloseIdleConnections()
			if tc.name == "private" && !errors.Is(tc.exit.Check(t.Context(), "127.0.0.1"), egress.ErrAddressDenied) {
				t.Fatal("private address did not fail address policy")
			}
			w := &webhookWorker{alertWorker: &alertWorker{now: func() time.Time { return time.Unix(1, 0) }}, exit: tc.exit}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := w.send(ctx, alertDelivery{ch: AlertChannel{ID: "hook", WebhookURL: tc.url}, payload: []byte(`{}`)}, []secret.Secret{secret.New(strings.Repeat("s", 32))}); err == nil {
				t.Fatal("unsafe destination acknowledged")
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

type webhookReceiver struct {
	server             *httptest.Server
	mu                 sync.Mutex
	bodies, signatures []string
	accepted           int
}

func newWebhookReceiver(t *testing.T) *webhookReceiver {
	t.Helper()
	r := &webhookReceiver{}
	r.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.bodies = append(r.bodies, string(body))
		r.signatures = append(r.signatures, req.Header.Get("X-Axiom-Signature"))
		if !receiverAccepts(body, req.Header.Get("X-Axiom-Signature"), strings.Repeat("s", 32)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if len(r.bodies) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		r.accepted++
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *webhookReceiver) assertDelivery(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) != 2 || r.accepted != 1 || r.bodies[0] != r.bodies[1] || r.signatures[0] == r.signatures[1] {
		t.Fatalf("delivery count/body/signature: accepted=%d bodies=%v signatures=%v", r.accepted, r.bodies, r.signatures)
	}
	if receiverAccepts([]byte(r.bodies[1]), r.signatures[1], strings.Repeat("x", 32)) || receiverAccepts([]byte(r.bodies[1]+" "), r.signatures[1], strings.Repeat("s", 32)) {
		t.Fatal("receiver accepted a wrong key or changed raw body")
	}
}

func assertRotationSignature(t *testing.T, values []secret.Secret, at time.Time, old bool) {
	t.Helper()
	body := []byte(`{"id":"notice"}`)
	header := webhookSignature(body, values, at)
	if !receiverAccepts(body, header, strings.Repeat("n", 32)) || receiverAccepts(body, header, strings.Repeat("s", 32)) != old {
		t.Error("rotation window not preserved")
	}
}

func TestWebhookRejectsInvalidChannelsAndSecrets(t *testing.T) {
	for _, ch := range []AlertChannel{
		{ID: "x", WebhookURL: "http://receiver.example/hook"},
		{ID: "x", WebhookURL: "https://user@receiver.example/hook"},
		{ID: "x", WebhookURL: "https://receiver.example/hook#fragment"},
		{ID: "x", WebhookURL: "https://receiver.example/hook", Email: "ops@example.com"},
	} {
		if err := validateAlertChannel(ch); !errors.Is(err, ErrAlertConfiguration) {
			t.Errorf("invalid channel accepted: %+v", ch)
		}
	}
	for _, value := range []string{strings.Repeat("s", 31), strings.Repeat("s", 513), strings.Repeat("界", 256)} {
		_, err := RotateAlertWebhookSecret(t.Context(), pg.Tx{}, nil, "hook", 1, secret.New(value), time.Time{})
		if !errors.Is(err, ErrAlertConfiguration) {
			t.Fatal("invalid byte length accepted")
		}
	}
}

func TestWebhookKeyCannotBeRelabelledToAnotherDestination(t *testing.T) {
	db := openDB(t)
	keys := webhookTestKeys(t)
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	ch := configureWebhook(t, db, "https://receiver.example/hook", keys, now)
	other := ch
	other.WebhookURL = "https://other.example/hook"
	alertTx(t, db, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE observability.webhook_keys SET destination=$1`, other.WebhookURL)
		return err
	})
	err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error { _, err := readWebhookKeys(t.Context(), tx, keys, other, now); return err })
	if err == nil {
		t.Fatal("relabelled signing key opened at a different destination")
	}
}

func assertWebhookBackoff(t *testing.T, job *rivertype.JobRow, err error, now time.Time) {
	t.Helper()
	if err == nil {
		t.Fatal("failed webhook acknowledged")
	}
	if job.State != rivertype.JobStateRetryable && job.State != rivertype.JobStateAvailable {
		t.Fatalf("unexpected failed job state: %+v", job)
	}
	if !job.ScheduledAt.After(now) || job.Attempt != 1 || job.MaxAttempts != 5 {
		t.Fatalf("failure lost backoff or attempts: %+v", job)
	}
}
