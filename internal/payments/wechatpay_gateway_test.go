package payments

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

const (
	wpTestAppID  = "wx1234567890abcdef"
	wpTestMchID  = "1900000001"
	wpTestSerial = "408B07E79B8269FEC3D5D3E6AB8ED163A6A380DB"
	wpTestKeyID  = "PUB_KEY_ID_0123456789ABCDEF0123456789ABCDEF"
	wpTestOldKey = "PUB_KEY_ID_FEDCBA9876543210FEDCBA9876543210"
	wpTestAPIKey = "0123456789abcdef0123456789abcdef"
)

type wpTestKeys struct {
	merchant, platform, old, other *rsa.PrivateKey
	platformCert, oldCert          *x509.Certificate
}

var (
	wpKeysOnce sync.Once
	wpTestPair wpTestKeys
)

func wpKeys(_ *testing.T) *wpTestKeys {
	wpKeysOnce.Do(func() {
		for _, key := range []**rsa.PrivateKey{&wpTestPair.merchant, &wpTestPair.platform, &wpTestPair.old, &wpTestPair.other} {
			generated, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			*key = generated
		}
		wpTestPair.platformCert = wpTestCertificate(&wpTestPair.platform.PublicKey, 0x408B07E7)
		wpTestPair.oldCert = wpTestCertificate(&wpTestPair.old.PublicKey, 0x5157A1B2)
	})
	return &wpTestPair
}

func wpTestCertificate(public *rsa.PublicKey, serial int64) *x509.Certificate {
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "Tenpay"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, wpKeysForCertificate())
	if err != nil {
		panic(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return cert
}

// wpKeysForCertificate returns any valid signing key for the self-signed test certificate. The certificate's
// signature is not verified by the plugin; only its public key is used.
func wpKeysForCertificate() *rsa.PrivateKey {
	if wpTestPair.platform != nil {
		return wpTestPair.platform
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}

func wpPrivatePEM(key *rsa.PrivateKey) string {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func wpPublicPEM(key *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func wpCertificatePEM(cert *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
}

func wpTestPlatformSerial(t *testing.T) string {
	t.Helper()
	return strings.ToUpper(wpKeys(t).platformCert.SerialNumber.Text(16))
}

func wpTestSign(text string, key *rsa.PrivateKey) string {
	sum := sha256.Sum256([]byte(text))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(signature)
}

func wpTestSettings(t *testing.T, gateway, keyID, oldKeyID string) []byte {
	t.Helper()
	raw, err := jsonx.MarshalCanonical(wechatSettings{
		AppID: wpTestAppID, MchID: wpTestMchID, SerialNo: wpTestSerial,
		PlatformKeyID: keyID, OldPlatformKeyID: oldKeyID, Gateway: gateway,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func wpTestConfig(t *testing.T, serverCert *x509.Certificate, settings []byte, secrets map[string]secret.Secret) MerchantConfig {
	t.Helper()
	client := egress.NewForTesting(egresstest.Loopback(t, serverCert))
	t.Cleanup(client.CloseIdleConnections)
	return MerchantConfig{
		ID: MerchantID(uuid.NewV7()), Currency: currency(t, "CNY"), Settings: settings, Secrets: secrets, Client: client,
	}
}

func wpCertConfig(t *testing.T, gateway string, serverCert *x509.Certificate, old bool) MerchantConfig {
	t.Helper()
	keys := wpKeys(t)
	secrets := map[string]secret.Secret{
		"private_key":          secret.New(wpPrivatePEM(keys.merchant)),
		"api_v3_key":           secret.New(wpTestAPIKey),
		"platform_certificate": secret.New(wpCertificatePEM(keys.platformCert)),
	}
	if old {
		secrets["old_platform_certificate"] = secret.New(wpCertificatePEM(keys.oldCert))
	}
	return wpTestConfig(t, serverCert, wpTestSettings(t, gateway, "", ""), secrets)
}

func wpPublicConfig(t *testing.T, gateway string, serverCert *x509.Certificate, old bool) MerchantConfig {
	t.Helper()
	keys := wpKeys(t)
	secrets := map[string]secret.Secret{
		"private_key":         secret.New(wpPrivatePEM(keys.merchant)),
		"api_v3_key":          secret.New(wpTestAPIKey),
		"platform_public_key": secret.New(wpPublicPEM(&keys.platform.PublicKey)),
	}
	oldID := ""
	if old {
		secrets["old_platform_public_key"] = secret.New(wpPublicPEM(&keys.old.PublicKey))
		oldID = wpTestOldKey
	}
	return wpTestConfig(t, serverCert, wpTestSettings(t, gateway, wpTestKeyID, oldID), secrets)
}

func wpOpen(t *testing.T, cfg MerchantConfig) Channel {
	t.Helper()
	channel, err := (WechatPay{}).Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

func wpTestRequest(t *testing.T) CreateRequest {
	return CreateRequest{
		Order: OrderID(uuid.NewV7()), Method: "wxpay", Upstream: "native",
		NotifyURL: "https://callbacks.example.com/generated", ReturnURL: "https://console.example.com/topup",
		Total: Money{Currency: currency(t, "CNY")}, ClientIP: netip.MustParseAddr("203.0.113.7"),
	}
}

// wpTestGateway verifies every request signature and signs every response over the exact bytes, so a test can
// prove both directions. A response body of nil means the status carries the whole answer (204).
type wpTestGateway struct {
	t       *testing.T
	respond func(r *http.Request, body []byte) (int, []byte)

	mu       sync.Mutex
	queries  int
	lastBody []byte

	corrupt bool
	omit    bool
	serial  string
	signer  *rsa.PrivateKey
}

//nolint:cyclop // one branch per response shape keeps the fixture readable
func (g *wpTestGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		g.t.Errorf("read request: %v", err)
		return
	}
	if err := wpTestVerifyRequest(r, body, &wpKeys(g.t).merchant.PublicKey); err != nil {
		g.t.Errorf("invalid gateway request %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	g.lastBody = bytes.Clone(body)
	if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v3/pay/transactions/out-trade-no/") {
		g.queries++
	}
	signer := g.signer
	if signer == nil {
		signer = wpKeys(g.t).platform
	}
	serial := g.serial
	if serial == "" {
		serial = strings.ToUpper(wpKeys(g.t).platformCert.SerialNumber.Text(16))
	}
	corrupt, omit := g.corrupt, g.omit
	g.mu.Unlock()

	status, response := g.respond(r, body)
	if status == http.StatusNoContent || len(response) == 0 {
		w.WriteHeader(status)
		return
	}
	if omit {
		w.WriteHeader(status)
		if _, err := w.Write(response); err != nil {
			g.t.Errorf("write response: %v", err)
		}
		return
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "testnonce1234567890"
	signature := wpTestSign(timestamp+"\n"+nonce+"\n"+string(response)+"\n", signer)
	if corrupt {
		signature = "AAAA"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Wechatpay-Serial", serial)
	w.Header().Set("Wechatpay-Timestamp", timestamp)
	w.Header().Set("Wechatpay-Nonce", nonce)
	w.Header().Set("Wechatpay-Signature", signature)
	w.WriteHeader(status)
	if _, err := w.Write(response); err != nil {
		g.t.Errorf("write response: %v", err)
	}
}

func (g *wpTestGateway) queryCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.queries
}

func wpTestVerifyRequest(r *http.Request, body []byte, key *rsa.PublicKey) error {
	auth := r.Header.Get("Authorization")
	const prefix = "WECHATPAY2-SHA256-RSA2048 "
	if !strings.HasPrefix(auth, prefix) {
		return fmt.Errorf("authorization is not WeChat Pay v3")
	}
	fields := map[string]string{}
	for part := range strings.SplitSeq(strings.TrimPrefix(auth, prefix), ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return fmt.Errorf("malformed authorization")
		}
		fields[name] = strings.Trim(value, `"`)
	}
	if fields["mchid"] != wpTestMchID || fields["serial_no"] != wpTestSerial ||
		fields["timestamp"] == "" || fields["nonce_str"] == "" || fields["signature"] == "" {
		return fmt.Errorf("wrong authorization fields: %v", fields)
	}
	if r.Header.Get("Accept") != "application/json" {
		return fmt.Errorf("missing accept header")
	}
	if len(body) > 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("missing content type")
	}
	text := r.Method + "\n" + r.URL.RequestURI() + "\n" + fields["timestamp"] + "\n" + fields["nonce_str"] + "\n" + string(body) + "\n"
	return wechatVerify(text, fields["signature"], key)
}

// wpTestCallback builds the encrypted, signed callback WeChat would send.
func wpTestCallback(t *testing.T, transaction map[string]any, signer *rsa.PrivateKey, serial string) Callback {
	t.Helper()
	return wpTestCallbackAt(t, transaction, signer, serial, time.Now())
}

func wpTestCallbackAt(t *testing.T, transaction map[string]any, signer *rsa.PrivateKey, serial string, at time.Time) Callback {
	t.Helper()
	return wpTestCallbackEventAt(t, "TRANSACTION.SUCCESS", transaction, signer, serial, at)
}

func wpTestCallbackEvent(t *testing.T, eventType string, transaction map[string]any, signer *rsa.PrivateKey, serial string) Callback {
	t.Helper()
	return wpTestCallbackEventAt(t, eventType, transaction, signer, serial, time.Now())
}

func wpTestCallbackEventAt(t *testing.T, eventType string, transaction map[string]any, signer *rsa.PrivateKey, serial string, at time.Time) Callback {
	t.Helper()
	plaintext, err := jsonx.MarshalCanonical(transaction)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher([]byte(wpTestAPIKey))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := rand.Text()[:12]
	ciphertext := gcm.Seal(nil, []byte(nonce), plaintext, []byte("transaction"))
	envelope, err := jsonx.MarshalCanonical(map[string]any{
		"id":            "EV-" + uuid.NewV7().String(),
		"create_time":   time.Now().UTC().Format(time.RFC3339),
		"event_type":    eventType,
		"resource_type": "encrypt-resource",
		"resource": map[string]any{
			"algorithm":       "AEAD_AES_256_GCM",
			"ciphertext":      base64.StdEncoding.EncodeToString(ciphertext),
			"nonce":           nonce,
			"associated_data": "transaction",
			"original_type":   "transaction",
		},
		"summary": "支付成功",
	})
	if err != nil {
		t.Fatal(err)
	}
	timestamp := strconv.FormatInt(at.Unix(), 10)
	nonceHeader := "headernonce12345"
	signature := wpTestSign(timestamp+"\n"+nonceHeader+"\n"+string(envelope)+"\n", signer)
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	header.Set("Wechatpay-Serial", serial)
	header.Set("Wechatpay-Timestamp", timestamp)
	header.Set("Wechatpay-Nonce", nonceHeader)
	header.Set("Wechatpay-Signature", signature)
	return Callback{Method: http.MethodPost, Body: envelope, Header: header}
}

func wpTransaction(order, trade string, total int64, state string) map[string]any {
	return map[string]any{
		"appid":          wpTestAppID,
		"mchid":          wpTestMchID,
		"out_trade_no":   order,
		"transaction_id": trade,
		"trade_state":    state,
		"amount":         map[string]any{"total": total, "currency": "CNY"},
	}
}

func wpTestJSONBody(v any) []byte {
	body, err := jsonx.MarshalCanonical(v)
	if err != nil {
		panic(err)
	}
	return body
}

func wpQueryJSON(order, state string, amount map[string]any) []byte {
	body := map[string]any{"appid": wpTestAppID, "mchid": wpTestMchID, "out_trade_no": order, "trade_state": state}
	if amount != nil {
		body["amount"] = amount
	}
	return wpTestJSONBody(body)
}

func wpTestServer(t *testing.T, gateway http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(gateway)
	t.Cleanup(server.Close)
	return server
}
