//go:build sandbox

package payments

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/secret"
)

// TestSandbox runs one real cycle against a provider's sandbox: the order is created through the plugin on the
// provider's test API, a person pays it in the browser with the sandbox's test card or buyer account, the provider's
// notification (or, when it cannot reach this machine, the query fallback) credits the order once, a replay of that
// notification credits nothing more, and a full refund succeeds. `just payments-sandbox` runs it; each provider is
// skipped, naming the variables it needs, when its credentials are missing. It refuses live keys.
func TestSandbox(t *testing.T) {
	t.Run("stripe", sandboxStripe)
	t.Run("alipay", sandboxAlipay)
}

func sandboxEnv(t *testing.T, names ...string) map[string]string {
	t.Helper()
	values := map[string]string{}
	var missing []string
	for _, name := range names {
		if values[name] = strings.TrimSpace(os.Getenv(name)); values[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		t.Skipf("skipped: set %s to run this sandbox (docs/domains/payments.md「沙箱实测」)", strings.Join(missing, ", "))
	}
	return values
}

func sandboxFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // the owner names the key file to read
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// sandboxRun is one world whose callback route listens on AXIOM_SANDBOX_LISTEN for the provider (directly for Stripe
// CLI forwarding, or behind the HTTPS tunnel named by AXIOM_SANDBOX_PUBLIC_URL).
type sandboxRun struct {
	w      *world
	mu     sync.Mutex
	notify *Callback // the last notification the core acknowledged
}

func newSandboxRun(t *testing.T, plugin Plugin) *sandboxRun {
	t.Helper()
	s := &sandboxRun{w: newTestWorld(t, plugin)}
	s.w.p.client = egress.NewDirect()
	s.w.open(s.w.siteA, RoundRobin)
	listen := os.Getenv("AXIOM_SANDBOX_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8787"
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatalf("listen on %s: %v", listen, err)
	}
	server := &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return s
}

func (s *sandboxRun) serve(rw http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, callbackMax+1))
	if err != nil {
		http.Error(rw, "read", http.StatusBadRequest)
		return
	}
	merchant, err := ParseMerchantID(strings.TrimPrefix(r.URL.Path, NotifyPath))
	if err != nil || !strings.HasPrefix(r.URL.Path, NotifyPath) {
		http.NotFound(rw, r)
		return
	}
	cb := Callback{Method: r.Method, RawQuery: r.URL.RawQuery, Body: body, Header: r.Header.Clone()}
	response := s.w.deliver(merchant, cb)
	s.w.t.Logf("notification %s %s answered %d", r.Method, r.URL.Path, response.Code)
	if response.Code >= 200 && response.Code < 300 {
		s.mu.Lock()
		s.notify = &cb
		s.mu.Unlock()
	}
	for k, v := range response.Header() {
		rw.Header()[k] = v
	}
	rw.WriteHeader(response.Code)
	_, _ = rw.Write(response.Body.Bytes())
}

// returnURL is where the payer comes back to; the core derives the notification URL from its host.
func sandboxReturnURL(t *testing.T) string {
	t.Helper()
	base := strings.TrimRight(os.Getenv("AXIOM_SANDBOX_PUBLIC_URL"), "/")
	if base == "" {
		return "https://localhost/topup"
	}
	if u, err := url.Parse(base); err != nil || u.Scheme != "https" {
		t.Fatal("AXIOM_SANDBOX_PUBLIC_URL must be an https URL")
	}
	return base + "/topup"
}

// cycle places an order, waits for the person to pay it, then checks the credit, a replay and a full refund.
func (s *sandboxRun) cycle(t *testing.T, merchant Merchant, order CreateOrder, waitForNotify bool) {
	t.Helper()
	w := s.w
	o, checkout, err := w.placeAs(w.accountA(), order)
	if err != nil {
		t.Fatalf("create the sandbox order: %v", err)
	}
	link := checkout.PayURL
	if link == "" {
		link = checkout.QRCode
	}
	t.Logf("pay order %s (%s) here within the wait: %s", o.ID.TypeID(), formatMinorText(o.Total), link)
	s.waitPaid(t, o, waitForNotify)
	w.assertCredit(o, o.Credits)
	s.mu.Lock()
	notified := s.notify
	s.mu.Unlock()
	if notified != nil {
		if r := w.deliver(MerchantID(merchant.ID), *notified); r.Code < 200 || r.Code > 299 {
			t.Fatalf("replayed notification answered %d", r.Code)
		}
		w.assertCredit(o, o.Credits)
		t.Log("credited once by the provider's notification; its replay credited nothing more")
	} else {
		t.Log("credited once by the query fallback; no notification reached this machine")
	}
	refund := w.approve(w.mustRefund(o, o.Credits, "sandbox-refund-"+o.ID.TypeID()))
	deadline := time.Now().Add(2 * time.Minute)
	for {
		w.dueRefunds()
		stored, err := w.p.GetRefund(t.Context(), w.accountA(), refund.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.State == "succeeded" {
			t.Logf("refund %s succeeded at the provider (%s)", stored.ID.TypeID(), stored.ProviderRef)
			return
		}
		if stored.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("refund ended %s", stored.State)
		}
		time.Sleep(5 * time.Second)
	}
}

// waitPaid waits for the order to be paid. With a notification route it waits for the notification alone, so the
// run proves that path; without one it drives the query fallback the poll job would run.
func (s *sandboxRun) waitPaid(t *testing.T, o Order, waitForNotify bool) {
	t.Helper()
	wait := 10 * time.Minute
	if v := os.Getenv("AXIOM_SANDBOX_WAIT"); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal("AXIOM_SANDBOX_WAIT must be a duration such as 10m")
		}
		wait = parsed
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if !waitForNotify {
			s.w.exec(fmt.Sprintf("UPDATE payments.orders SET next_query_at = clock_timestamp() WHERE id = '%s'", uuid.UUID(o.ID)))
			s.w.p.pollOrder(t.Context(), o.ID)
		}
		if state, _, _ := s.w.stored(o.ID); state != string(Pending) {
			if state != string(Paid) {
				t.Fatalf("the paid order ended %s", state)
			}
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatal(errors.New("the order was not paid within AXIOM_SANDBOX_WAIT"))
}

// sandboxStripe uses Stripe test mode. The hosted Checkout page is paid with the test card 4242 4242 4242 4242; the
// webhook reaches this machine through `stripe listen`, whose signing secret is AXIOM_SANDBOX_STRIPE_WEBHOOK_SECRET.
func sandboxStripe(t *testing.T) {
	env := sandboxEnv(t, "AXIOM_SANDBOX_STRIPE_SECRET_KEY")
	key := env["AXIOM_SANDBOX_STRIPE_SECRET_KEY"]
	if !strings.HasPrefix(key, "sk_test_") && !strings.HasPrefix(key, "rk_test_") {
		t.Fatal("AXIOM_SANDBOX_STRIPE_SECRET_KEY must be a test-mode key (sk_test_ or rk_test_)")
	}
	webhook := os.Getenv("AXIOM_SANDBOX_STRIPE_WEBHOOK_SECRET")
	s := newSandboxRun(t, Stripe{})
	in := s.w.input(s.w.siteA, MethodInput{Method: "card", Upstream: "card", Fee: Fee{PPM: 30_000}, Enabled: true})
	in.Kind, in.Settings, in.Currency = "stripe", []byte("{}"), currency(t, "USD")
	in.RefundEnabled, in.UserRefund = true, true
	in.Secrets = map[string]secret.Secret{"secret_key": secret.New(key), "webhook_secret": secret.New("whsec_not_forwarded")}
	if webhook != "" {
		in.Secrets["webhook_secret"] = secret.New(webhook)
	}
	merchant := s.w.mustSave(siteScope(s.w.siteA), in)
	if webhook != "" {
		listen := os.Getenv("AXIOM_SANDBOX_LISTEN")
		if listen == "" {
			listen = "127.0.0.1:8787"
		}
		t.Logf("forward webhooks now: stripe listen --forward-to http://%s%s%s", listen, NotifyPath, MerchantID(merchant.ID).TypeID())
	}
	s.cycle(t, merchant, CreateOrder{
		Site: s.w.site(s.w.siteA, "USD"), Account: s.w.account, User: s.w.user, Method: "card", Credits: 10 * yuan,
		Key: "sandbox-stripe", ReturnURL: sandboxReturnURL(t), ClientIP: netip.MustParseAddr("203.0.113.7"),
	}, webhook != "")
}

// sandboxAlipay uses the 开放平台 sandbox gateway. The page-pay link is paid by logging in with the sandbox buyer
// account; Alipay's notification reaches this machine only through the HTTPS tunnel in AXIOM_SANDBOX_PUBLIC_URL.
func sandboxAlipay(t *testing.T) {
	env := sandboxEnv(t, "AXIOM_SANDBOX_ALIPAY_APP_ID", "AXIOM_SANDBOX_ALIPAY_PRIVATE_KEY_FILE", "AXIOM_SANDBOX_ALIPAY_PUBLIC_KEY_FILE")
	settings := fmt.Appendf(nil, `{"app_id":%q,"gateway":"https://openapi-sandbox.dl.alipaydev.com/gateway.do"}`, env["AXIOM_SANDBOX_ALIPAY_APP_ID"])
	s := newSandboxRun(t, Alipay{})
	in := s.w.input(s.w.siteA, MethodInput{Method: "alipay", Upstream: alipayPagePay, Fee: Fee{PPM: 6_000}, Enabled: true})
	in.Kind, in.Settings = "alipay", settings
	in.RefundEnabled, in.UserRefund = true, true
	in.Secrets = map[string]secret.Secret{
		"private_key": secret.New(sandboxFile(t, env["AXIOM_SANDBOX_ALIPAY_PRIVATE_KEY_FILE"])),
		"public_key":  secret.New(sandboxFile(t, env["AXIOM_SANDBOX_ALIPAY_PUBLIC_KEY_FILE"])),
	}
	merchant := s.w.mustSave(siteScope(s.w.siteA), in)
	public := os.Getenv("AXIOM_SANDBOX_PUBLIC_URL") != ""
	if !public {
		t.Log("AXIOM_SANDBOX_PUBLIC_URL is not set: Alipay cannot reach this machine, so the query fallback credits")
	}
	s.cycle(t, merchant, CreateOrder{
		Site: s.w.site(s.w.siteA, "CNY"), Account: s.w.account, User: s.w.user, Method: "alipay", Credits: yuan,
		Key: "sandbox-alipay", ReturnURL: sandboxReturnURL(t), ClientIP: netip.MustParseAddr("203.0.113.7"),
	}, public)
}
