package accounts

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
)

func TestUserWebhookPayloadRetryAndEgress(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var bodies []string
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, string(raw))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()
	exit := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	defer exit.CloseIdleConnections()
	w := noticeWorker{exit: exit}
	n := userNotice{ID: uuid.NewV7(), Kind: "low_balance", Value: "-1", At: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC), url: server.URL}
	if err := w.send(t.Context(), n); err == nil {
		t.Fatal("503 accepted")
	}
	if err := w.send(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatalf("retry changed payload: %v", bodies)
	}
	mu.Unlock()
	// Redirects and private destinations stay subject to the production egress policy.
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	redirectExit := egress.NewForTesting(egresstest.Loopback(t, redirect.Certificate(), server.Certificate()))
	defer redirectExit.CloseIdleConnections()
	w.exit = redirectExit
	n.url = redirect.URL
	if err := w.send(t.Context(), n); err == nil {
		t.Fatal("redirect accepted")
	}
	production, err := egress.New(egress.Direct)
	if err != nil {
		t.Fatal(err)
	}
	defer production.CloseIdleConnections()
	w.exit = production
	n.url = server.URL
	before := connections.Load()
	if err = w.send(t.Context(), n); err == nil {
		t.Fatal("private address accepted")
	}
	if connections.Load() != before {
		t.Fatal("production egress dialed a private destination")
	}
}

func TestNotificationDestinationValidation(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"http://example.com", "https://user:password@example.com", "https://example.com/#fragment", "https:///missing", "https:opaque"} {
		if _, err := ValidateNotificationWebhookURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	if host, err := ValidateNotificationWebhookURL("https://example.com/notice"); err != nil || host != "example.com" {
		t.Fatalf("valid URL: %q %v", host, err)
	}
}

func TestUserWebhookWorkerPersistsTerminalResults(t *testing.T) {
	t.Parallel()
	db := noticeWorkerDB(t)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	exit := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	defer exit.CloseIdleConnections()
	w := noticeWorker{db: db, scope: pg.NewScope(true, nil, nil), exit: exit}
	id := uuid.NewV7()
	if err := db.Tx(t.Context(), w.scope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO accounts.user_notices(id,site_id,account_id,user_id,source,kind,value,dispatched,webhook_url,webhook_state)
 SELECT $1,site_id,account_id,id,'test','top_up',100,true,$2,'queued' FROM accounts.users`, id, server.URL)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	job := &river.Job[noticeArgs]{JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 5}, Args: noticeArgs{ID: id}}
	for attempt := 1; attempt <= 5; attempt++ {
		job.Attempt = attempt
		err := w.work(t.Context(), job)
		if (err != nil) != (attempt < 5) {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if err := w.work(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 {
		t.Fatalf("terminal job sent again: %d", calls.Load())
	}
	var state string
	if err := db.ReadTx(t.Context(), w.scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT webhook_state FROM accounts.user_notices WHERE id=$1`, id).Scan(&state)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("receipt: %s", state)
	}
}

func noticeWorkerDB(t *testing.T) *pg.DB {
	t.Helper()
	database := pgtest.New(t, Module())
	site, tier, account, user := uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	sql := fmt.Sprintf(`INSERT INTO accounts.sites(id,name,status,key_prefix,registration,default_tier_id) VALUES('%s','Notices','active','sk-notice-','closed','%s');
 INSERT INTO accounts.tiers(id,site_id,name) VALUES('%s','%s','Standard');
 INSERT INTO accounts.accounts(id,site_id,kind,name,status,tier_id) VALUES('%s','%s','personal','Notices','active','%s');
 INSERT INTO accounts.users(id,site_id,account_id,email,password_hash,factor_version,status,invite_code) VALUES('%s','%s','%s','notice@example.com','$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$aGFzaA',1,'active','notice');`, site, tier, tier, site, account, site, tier, user, site, account)
	if err := pgtest.Run(t.Context(), database.Superuser, sql); err != nil {
		t.Fatal(err)
	}
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

// TestSpendAnomalyNoticeMail: a region/model-only incident has no amount, a spend incident shows the exact amount,
// and both tell the user what to do without leaking call content.
func TestSpendAnomalyNoticeMail(t *testing.T) {
	t.Parallel()
	n := userNotice{Kind: "spend_anomaly", Value: "0", Currency: "USD", language: "en", brand: "Site", email: "user@example.com"}
	m := noticeMail(n)
	if m.Subject != "Site — Unusual account activity" || strings.Contains(m.Text, "USD") || !strings.Contains(m.Text, "disable any API keys") {
		t.Fatalf("no amount: %q", m.Text)
	}
	n.Value = "5000000000"
	m = noticeMail(n)
	if !strings.Contains(m.Text, "5.000000000 USD") {
		t.Fatalf("amount: %q", m.Text)
	}
	n.language = "zh"
	m = noticeMail(n)
	if !strings.Contains(m.Text, "账户异常活动") || !strings.Contains(m.Text, "5.000000000 USD") || !strings.Contains(m.Text, "停用可疑的 API key") {
		t.Fatalf("Chinese: %q", m.Text)
	}
}
