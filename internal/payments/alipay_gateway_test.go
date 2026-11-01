package payments

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

const (
	alipayTestAppID    = "2021000000000001"
	alipayTestSellerID = "2088000000000001"
)

type alipayTestKeys struct {
	app, alipay, old, other *rsa.PrivateKey
}

var (
	alipayKeysOnce sync.Once
	alipayTestPair alipayTestKeys
)

func alipayKeys(t *testing.T) *alipayTestKeys {
	t.Helper()
	alipayKeysOnce.Do(func() {
		for _, key := range []**rsa.PrivateKey{&alipayTestPair.app, &alipayTestPair.alipay, &alipayTestPair.old, &alipayTestPair.other} {
			generated, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			*key = generated
		}
	})
	return &alipayTestPair
}

func alipayPrivatePEM(key *rsa.PrivateKey) string {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func alipayPublicPEM(key *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func alipayBarePublic(key *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

func alipayTestSign(text []byte, key *rsa.PrivateKey) string {
	sum := sha256.Sum256(text)
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(signature)
}

func alipayTestConfig(t *testing.T, gateway string, certificates ...*x509.Certificate) MerchantConfig {
	t.Helper()
	raw, err := jsonx.MarshalCanonical(alipaySettings{AppID: alipayTestAppID, SellerID: alipayTestSellerID, Gateway: gateway})
	if err != nil {
		t.Fatal(err)
	}
	client := egress.NewForTesting(egresstest.Loopback(t, certificates...))
	t.Cleanup(client.CloseIdleConnections)
	return MerchantConfig{
		ID: MerchantID(uuid.NewV7()), Currency: currency(t, "CNY"), Settings: raw,
		Secrets: map[string]secret.Secret{
			"private_key": secret.New(alipayPrivatePEM(alipayKeys(t).app)),
			"public_key":  secret.New(alipayPublicPEM(&alipayKeys(t).alipay.PublicKey)),
		},
		Client: client,
	}
}

func alipayOpen(t *testing.T, cfg MerchantConfig) Channel {
	t.Helper()
	c, err := (Alipay{}).Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func alipayRequest(t *testing.T) CreateRequest {
	t.Helper()
	return CreateRequest{
		Order: OrderID(uuid.NewV7()), Method: "alipay", Upstream: alipayPagePay,
		NotifyURL: "https://callbacks.example.com/generated", ReturnURL: "https://console.example.com/topup",
		Total: Money{currency(t, "CNY"), 12345}, Subject: "Top-up & credits",
	}
}

// alipayTestGateway is a local implementation of the official protocol: it verifies every request signature and signs
// every answer over the exact node bytes, so a test can prove both directions.
type alipayTestGateway struct {
	t                *testing.T
	mu               sync.Mutex
	respond          func(method string, biz map[string]jsonx.Raw, form url.Values) (string, bool)
	corruptSignature bool
	omitSignature    bool
	status           int
	queries          int
	closes           int
	refunds          int
}

//nolint:cyclop // one branch per gateway method; the test checks each branch
func (g *alipayTestGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status != 0 {
		http.Error(w, "gateway down", g.status)
		return
	}
	if r.Method != http.MethodPost {
		g.t.Error("gateway got a non-POST request")
		return
	}
	if err := r.ParseForm(); err != nil {
		g.t.Error(err)
		return
	}
	form := r.PostForm
	keys := alipayKeys(g.t)
	if form.Get("app_id") != alipayTestAppID || form.Get("sign_type") != alipaySignType ||
		form.Get("format") != "JSON" || form.Get("charset") != "utf-8" || form.Get("version") != "1.0" ||
		!alipayVerify([]byte(alipaySignText(form, true)), form.Get("sign"), &keys.app.PublicKey) {
		g.t.Error("invalid gateway request")
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	var biz map[string]jsonx.Raw
	if err := jsonx.Unmarshal([]byte(form.Get("biz_content")), &biz); err != nil {
		g.t.Errorf("biz_content: %v", err)
		return
	}
	method := form.Get("method")
	switch method {
	case "alipay.trade.query":
		g.queries++
	case "alipay.trade.close":
		g.closes++
	case "alipay.trade.refund":
		g.refunds++
	}
	node, signed := g.respond(method, biz, form)
	if !signed {
		_, writeErr := io.WriteString(w, `{"error_response":`+node+`}`)
		if writeErr != nil {
			g.t.Errorf("gateway write: %v", writeErr)
		}
		return
	}
	name := strings.ReplaceAll(method, ".", "_") + "_response"
	var body string
	switch {
	case g.omitSignature:
		body = fmt.Sprintf(`{"%s":%s}`, name, node)
	case g.corruptSignature:
		body = fmt.Sprintf(`{"%s":%s,"sign":"AAAA"}`, name, node)
	default:
		body = fmt.Sprintf(`{"%s":%s,"sign":%q}`, name, node, alipayTestSign([]byte(node), keys.alipay))
	}
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	_, writeErr := io.WriteString(w, body) //nolint:gosec // the test gateway writes the response it just signed
	if writeErr != nil {
		g.t.Errorf("gateway write: %v", writeErr)
	}
}

func alipayBiz(t *testing.T, biz map[string]jsonx.Raw, name string) string {
	t.Helper()
	raw, ok := biz[name]
	if !ok {
		return ""
	}
	var value string
	if err := jsonx.Unmarshal(raw, &value); err != nil {
		t.Fatalf("biz %s: %v", name, err)
	}
	return value
}

func alipayNoSecrets(t *testing.T, text string, forbidden ...string) {
	t.Helper()
	for _, secretText := range forbidden {
		if strings.Contains(text, secretText) {
			t.Fatalf("leaked %q in %q", secretText, text)
		}
	}
}

func alipayChannelWith(t *testing.T, respond func(method string, biz map[string]jsonx.Raw, form url.Values) (string, bool)) (Channel, *alipayTestGateway) {
	t.Helper()
	return alipayChannelWithGateway(t, &alipayTestGateway{t: t, respond: respond})
}

func alipayChannelWithGateway(t *testing.T, gateway *alipayTestGateway) (Channel, *alipayTestGateway) {
	t.Helper()
	server := httptest.NewTLSServer(gateway)
	t.Cleanup(server.Close)
	return alipayOpen(t, alipayTestConfig(t, server.URL, server.Certificate())), gateway
}

func (g *alipayTestGateway) counts() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.queries
}
