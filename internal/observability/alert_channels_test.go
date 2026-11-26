package observability

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

var botHosts = []string{"open.feishu.cn", "qyapi.weixin.qq.com", "api.telegram.org"}

const (
	feishuSecret = "feishu-signing-secret"
	botToken     = "123456:ABCdefGHIjklMNOpqrSTUvwxYZ012345678"
	adminOrigin  = "https://admin.example"
)

// botPost is one request the fake bots took: where it went and what it said.
type botPost struct {
	host, path string
	body       map[string]any
}

// fakeBots answers as the three bot APIs do, by their official names: a fake of each that accepts any message.
type fakeBots struct {
	server *httptest.Server
	cert   *x509.Certificate
	mu     sync.Mutex
	posts  []botPost
}

func newFakeBots(t *testing.T) *fakeBots {
	t.Helper()
	f := &fakeBots{}
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body) //nolint:errcheck // a test server
		var body map[string]any
		if err := jsonx.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		host := strings.Split(r.Host, ":")[0]
		f.mu.Lock()
		f.posts = append(f.posts, botPost{host, r.URL.RequestURI(), body})
		f.mu.Unlock()
		answer := map[string]string{"open.feishu.cn": `{"code":0,"msg":"success"}`, "qyapi.weixin.qq.com": `{"errcode":0,"errmsg":"ok"}`}[host]
		if answer == "" {
			answer = `{"ok":true,"result":{}}`
		}
		_, _ = io.WriteString(w, answer) //nolint:errcheck // a test server
	}))
	var leaf tls.Certificate
	leaf, f.cert = selfSigned(t, botHosts)
	f.server.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12}
	f.server.StartTLS()
	t.Cleanup(f.server.Close)
	return f
}

// exit reaches the fake by the bots' names; refuse routes them to the same place under a policy that allows nothing.
func (f *fakeBots) exit(t *testing.T, refuse bool) *egress.Client {
	t.Helper()
	at := netip.MustParseAddrPort(f.server.Listener.Addr().String())
	policy := egresstest.Loopback(t, f.cert)
	if refuse {
		policy = egresstest.Policy{}
	}
	for _, h := range botHosts {
		policy = policy.Route(h, at)
	}
	c := egress.NewForTesting(policy)
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func (f *fakeBots) sent() []botPost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]botPost(nil), f.posts...)
}

func selfSigned(t *testing.T, hosts []string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: hosts, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}

// configureBot makes ch the only channel, with a rule that tells it of pending approvals, and sets its secret.
func configureBot(t *testing.T, db *pg.DB, keys *secret.Keyring, ch AlertChannel, value string, now time.Time) {
	t.Helper()
	alertTx(t, db, func(tx pg.Tx) error {
		c, err := GetAlertConfiguration(t.Context(), tx)
		if err != nil {
			return err
		}
		c.Channels, c.AdminOrigin = []AlertChannel{ch}, adminOrigin
		c.Rules = []AlertRule{{ID: "approvals", Kind: "operations.approval_pending", Channel: ch.ID, MinimumSeverity: "info", Threshold: 1}}
		if err = SetAlertConfiguration(t.Context(), tx, c); err != nil || value == "" {
			return err
		}
		_, err = RotateAlertWebhookSecret(t.Context(), tx, keys, ch.ID, c.Rev+1, secret.New(value), now)
		return err
	})
}

// deliverAll freezes every pending notification as dispatch does and works each one's post at its last attempt, so a
// failure is recorded at once; it returns the receipts' states by rule.
func deliverAll(t *testing.T, db *pg.DB, worker *webhookWorker, now time.Time) map[string]string {
	t.Helper()
	states := map[string]string{}
	var ids []uuid.UUID
	alertTx(t, db, func(tx pg.Tx) error {
		deliveries, err := pendingAlertDeliveries(t.Context(), tx, now)
		for _, d := range deliveries {
			body, err := postedNotice(d, adminOrigin)
			if err == nil {
				_, err = tx.Exec(t.Context(), `UPDATE observability.alert_receipts SET state='queued',payload=$2 WHERE id=$1`, d.id, body)
			}
			if err != nil {
				return err
			}
			ids = append(ids, d.id)
		}
		return err
	})
	for _, id := range ids {
		job := &river.Job[webhookArgs]{JobRow: &rivertype.JobRow{Attempt: 5, MaxAttempts: 5}, Args: webhookArgs{Receipt: id}}
		if err := worker.work(t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}
	alertTx(t, db, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT rule_id,state FROM observability.alert_receipts`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rule, state string
			if err = rows.Scan(&rule, &state); err != nil {
				return err
			}
			states[rule] = state
		}
		return rows.Err()
	})
	return states
}

// botText is the message text a post carried, wherever its API puts it.
func botText(p botPost) string {
	for _, path := range [][]string{{"content", "text"}, {"text", "content"}, {"text"}} {
		var v any = p.body
		for _, k := range path {
			m, _ := v.(map[string]any)
			v = m[k]
		}
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// TestBotChannelsDeliverTestsAndApprovals: each bot channel's self-test and an approval reach its API as that API
// takes them (address, signature, token, chat), the approval with its request and the admin link that decides it; the
// secret is never part of the configuration read back; and a target egress refuses fails the receipt, not the alert.
func TestBotChannelsDeliverTestsAndApprovals(t *testing.T) {
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	approval := uuid.NewV7()
	for _, tc := range []struct {
		name   string
		ch     AlertChannel
		secret string
		refuse bool
		check  func(t *testing.T, p botPost)
	}{
		{"feishu signed", AlertChannel{ID: "fs", FeishuURL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc"}, feishuSecret, false, func(t *testing.T, p botPost) {
			stamp, _ := p.body["timestamp"].(string)
			mac := hmac.New(sha256.New, []byte(stamp+"\n"+feishuSecret))
			if p.path != "/open-apis/bot/v2/hook/abc" || p.body["msg_type"] != "text" || stamp != "1795683600" ||
				p.body["sign"] != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
				t.Errorf("feishu post: %+v", p)
			}
		}},
		{"feishu unsigned", AlertChannel{ID: "fs", FeishuURL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc"}, "", false, func(t *testing.T, p botPost) {
			if _, signed := p.body["sign"]; signed {
				t.Errorf("signed without a secret: %+v", p)
			}
		}},
		{"wecom", AlertChannel{ID: "wc", WeComURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k1"}, "", false, func(t *testing.T, p botPost) {
			if p.path != "/cgi-bin/webhook/send?key=k1" || p.body["msgtype"] != "text" {
				t.Errorf("wecom post: %+v", p)
			}
		}},
		{"telegram", AlertChannel{ID: "tg", TelegramChatID: "-1001234"}, botToken, false, func(t *testing.T, p botPost) {
			if p.path != "/bot"+botToken+"/sendMessage" || p.body["chat_id"] != "-1001234" {
				t.Errorf("telegram post: %+v", p)
			}
		}},
		{"refused", AlertChannel{ID: "wc", WeComURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k1"}, "", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			keys := webhookTestKeys(t)
			bots := newFakeBots(t)
			configureBot(t, db, keys, tc.ch, tc.secret, now)
			alertTx(t, db, func(tx pg.Tx) error {
				if _, err := SendAlertTest(t.Context(), tx, tc.ch.ID, now); err != nil {
					return err
				}
				return alert.Persist(t.Context(), tx, uuid.NewV7(), alert.Signal{Kind: "operations.approval_pending", Key: approval.String(), Severity: "warning",
					Fields: map[string]string{"tool": "refund_create", "target": "order:ord_1", "expires_at": "2026-11-26T09:30:00Z"}})
			})
			alertCycle(t, db, now)
			worker := &webhookWorker{alertWorker: &alertWorker{db: db, scope: platformScope, now: func() time.Time { return now }}, keys: keys, exit: bots.exit(t, tc.refuse)}
			states := deliverAll(t, db, worker, now)
			want := "delivered"
			if tc.refuse {
				want = "failed"
			}
			if states["test"] != want || states["approvals"] != want {
				t.Fatalf("receipts %v, want %s", states, want)
			}
			events := alertCycle(t, db, now)
			if tc.refuse {
				if len(bots.sent()) != 0 || len(events) == 0 {
					t.Fatalf("refused target: posts %v, events %v", bots.sent(), events)
				}
				return
			}
			posts := bots.sent()
			if len(posts) != 2 {
				t.Fatalf("posts: %+v", posts)
			}
			var texts []string
			for _, p := range posts {
				tc.check(t, p)
				texts = append(texts, botText(p))
			}
			all := strings.Join(texts, "\n")
			for _, want := range []string{"Axiom alert test", "Tool: refund_create", "Target: order:ord_1", "Decide: " + adminOrigin + "/approvals/" + approval.String()} {
				if !strings.Contains(all, want) {
					t.Errorf("messages lack %q: %q", want, all)
				}
			}
			alertTx(t, db, func(tx pg.Tx) error {
				c, err := GetAlertConfiguration(t.Context(), tx)
				raw, _ := jsonx.MarshalCanonical(c) //nolint:errcheck // a test reading its own value
				if tc.secret != "" && strings.Contains(string(raw), tc.secret) {
					t.Error("the configuration returned the secret")
				}
				return err
			})
		})
	}
}

// TestChannelKindsCheckTheirDestinationAndSecret: a channel names exactly one valid destination, the bots only their
// official hosts, and each kind takes only its own secret.
func TestChannelKindsCheckTheirDestinationAndSecret(t *testing.T) {
	for _, ch := range []AlertChannel{
		{ID: "x"},
		{ID: "x", WebhookURL: "http://receiver.example/hook"},
		{ID: "x", WebhookURL: "https://user@receiver.example/hook"},
		{ID: "x", WebhookURL: "https://receiver.example/hook#fragment"},
		{ID: "x", WebhookURL: "https://receiver.example/hook", Email: "ops@example.com"},
		{ID: "x", FeishuURL: "https://open.feishu.cn.evil.example/open-apis/bot/v2/hook/a"},
		{ID: "x", FeishuURL: "https://open.feishu.cn:8443/open-apis/bot/v2/hook/a"},
		{ID: "x", WeComURL: "http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=a"},
		{ID: "x", TelegramChatID: "chat"},
		{ID: "x", TelegramChatID: "123", WeComURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=a"},
	} {
		if err := validateAlertChannel(ch); !errors.Is(err, ErrAlertConfiguration) {
			t.Errorf("invalid channel accepted: %+v", ch)
		}
	}
	db := openDB(t)
	keys := webhookTestKeys(t)
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		ch     AlertChannel
		values []string
	}{
		{AlertChannel{ID: "hook", WebhookURL: "https://receiver.example/hook"}, []string{strings.Repeat("s", 31), strings.Repeat("s", 513), strings.Repeat("界", 256)}},
		{AlertChannel{ID: "tg", TelegramChatID: "@axiom_ops"}, []string{"not-a-token", strings.Repeat("s", 40)}},
		{AlertChannel{ID: "wc", WeComURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=a"}, []string{strings.Repeat("s", 40)}},
	} {
		configureBot(t, db, keys, tc.ch, "", now)
		for _, value := range tc.values {
			err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
				c, err := GetAlertConfiguration(t.Context(), tx)
				if err == nil {
					_, err = RotateAlertWebhookSecret(t.Context(), tx, keys, tc.ch.ID, c.Rev, secret.New(value), now)
				}
				return err
			})
			if !errors.Is(err, ErrAlertConfiguration) {
				t.Errorf("%s took the secret %q: %v", tc.ch.ID, value, err)
			}
		}
	}
}
