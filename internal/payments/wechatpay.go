package payments

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"axiom/internal/platform"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

const (
	wechatpayProductionGateway = "https://api.mch.weixin.qq.com"
	wechatpayMinKeyBits        = 2048
	wechatpayMaxSubjectRunes   = 127
	wechatpayMaxReasonBytes    = 80
	wechatpayCallbackWindow    = 5 * time.Minute
)

// WechatPay is the official WeChat Pay API v3 channel: Native and H5 orders, platform certificate or WeChat Pay public
// key verification, and AES-256-GCM callback decryption.
type WechatPay struct{}

// Kind is the plugin's name; a site's payment method may still be called wxpay.
func (WechatPay) Kind() Kind { return "wechatpay" }

type wechatSettings struct {
	AppID            string `json:"app_id"`
	MchID            string `json:"mch_id"`
	SerialNo         string `json:"serial_no"`
	PlatformKeyID    string `json:"platform_public_key_id,omitempty"`
	OldPlatformKeyID string `json:"old_platform_public_key_id,omitempty"`
	Gateway          string `json:"gateway,omitempty"`
}

func (s wechatSettings) normalized() wechatSettings {
	s.AppID = strings.TrimSpace(s.AppID)
	s.MchID = strings.TrimSpace(s.MchID)
	s.SerialNo = strings.ToUpper(strings.TrimSpace(s.SerialNo))
	s.PlatformKeyID = strings.ToUpper(strings.TrimSpace(s.PlatformKeyID))
	s.OldPlatformKeyID = strings.ToUpper(strings.TrimSpace(s.OldPlatformKeyID))
	s.Gateway = strings.TrimRight(strings.TrimSpace(s.Gateway), "/")
	if s.Gateway == "" {
		s.Gateway = wechatpayProductionGateway
	} else if gateway, err := wechatGateway(s.Gateway); err == nil {
		s.Gateway = gateway
	}
	return s
}

func (s wechatSettings) check() error {
	if !wechatToken(s.AppID, 6, 32) {
		return &FieldError{"/settings/app_id", "must be 6 to 32 letters or digits"}
	}
	if !wechatDigits(s.MchID, 6, 32) {
		return &FieldError{"/settings/mch_id", "must be 6 to 32 digits"}
	}
	if !wechatSerial(s.SerialNo) {
		return &FieldError{"/settings/serial_no", "must be 16 to 64 hexadecimal digits"}
	}
	if s.PlatformKeyID != "" && !wechatKeyID(s.PlatformKeyID) {
		return &FieldError{"/settings/platform_public_key_id", "must be a WeChat Pay public key ID"}
	}
	if s.OldPlatformKeyID != "" && !wechatKeyID(s.OldPlatformKeyID) {
		return &FieldError{"/settings/old_platform_public_key_id", "must be a WeChat Pay public key ID"}
	}
	gateway, err := wechatGateway(s.Gateway)
	if err != nil {
		return &FieldError{"/settings/gateway", err.Error()}
	}
	if gateway != s.Gateway {
		return &FieldError{"/settings/gateway", "must be a normalized HTTPS origin"}
	}
	return nil
}

func wechatToken(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func wechatDigits(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func wechatSerial(s string) bool {
	if len(s) < 16 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func wechatKeyID(s string) bool {
	if len(s) < 12 || len(s) > 80 || !strings.HasPrefix(s, "PUB_KEY_ID_") {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// wechatGateway normalizes the API origin and refuses anything that could carry credentials or change the request
// path. The production and test endpoints are both HTTPS origins without a path.
func wechatGateway(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || wechatGatewayUnsafe(u) {
		return "", errors.New("must be an HTTPS origin without user info, query, fragment or path")
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	u.Path, u.RawPath = "", ""
	return u.String(), nil
}

func wechatGatewayUnsafe(u *url.URL) bool {
	return u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.RawFragment != "" ||
		u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawPath != ""
}

func wechatSettingsOf(raw []byte) (wechatSettings, error) {
	var s wechatSettings
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil {
		return wechatSettings{}, &FieldError{"/settings", "must be a JSON object with only the channel's members"}
	}
	s = s.normalized()
	if err := s.check(); err != nil {
		return wechatSettings{}, err
	}
	return s, nil
}

// normalizeSettings canonicalizes names and the gateway before the core hashes and seals the configuration. Secrets
// are checked in Open, where they are available.
func (WechatPay) normalizeSettings(raw []byte) ([]byte, error) {
	s, err := wechatSettingsOf(raw)
	if err != nil {
		return nil, err
	}
	return jsonx.MarshalCanonical(s)
}

// identitySettings is the part of the configuration that cannot change while an order is in flight. The method
// mapping and upstream may change; the merchant, certificate serial and verification keys may not.
func (WechatPay) identitySettings(raw []byte) ([]byte, error) {
	s, err := wechatSettingsOf(raw)
	if err != nil {
		return nil, err
	}
	return jsonx.MarshalCanonical(struct {
		AppID            string `json:"app_id"`
		MchID            string `json:"mch_id"`
		SerialNo         string `json:"serial_no"`
		PlatformKeyID    string `json:"platform_public_key_id,omitempty"`
		OldPlatformKeyID string `json:"old_platform_public_key_id,omitempty"`
		Gateway          string `json:"gateway"`
	}{s.AppID, s.MchID, s.SerialNo, s.PlatformKeyID, s.OldPlatformKeyID, s.Gateway})
}

var wechatSecretNames = map[string]bool{
	"private_key": true, "api_v3_key": true,
	"platform_certificate": true, "platform_public_key": true,
	"old_platform_certificate": true, "old_platform_public_key": true,
}

// Open binds one merchant account. All parsing is local: saving a merchant does not call WeChat, so a gateway outage
// cannot block a configuration change and secrets never leave the process.
func (WechatPay) Open(c MerchantConfig) (Channel, error) {
	s, err := wechatSettingsOf(c.Settings)
	if err != nil {
		return nil, err
	}
	if c.Currency.String() != "CNY" {
		return nil, &FieldError{"/currency", "WeChat Pay domestic merchants settle in CNY"}
	}
	for name := range c.Secrets {
		if !wechatSecretNames[name] {
			return nil, &FieldError{"/secrets", "unknown secret name " + name}
		}
	}
	private, err := wechatPrivateKey(c.Secrets["private_key"].Reveal())
	if err != nil {
		return nil, &FieldError{"/secrets/private_key", err.Error()}
	}
	apiV3 := c.Secrets["api_v3_key"].Reveal()
	if len(apiV3) != 32 {
		return nil, &FieldError{"/secrets/api_v3_key", "must be exactly 32 bytes"}
	}
	keys, err := wechatPlatformKeys(c.Secrets, s)
	if err != nil {
		return nil, &FieldError{"/secrets", err.Error()}
	}
	if c.Client == nil {
		return nil, errors.New("wechatpay: an egress client is required")
	}
	return &wechatChannel{
		settings: s, currency: c.Currency, private: private, apiV3: []byte(apiV3),
		keys: keys, client: c.Client, now: time.Now,
	}, nil
}

// wechatPlatformKeys builds the serial-to-key map used for both responses and callbacks. Certificates carry their
// serial; a WeChat Pay public key carries the ID in settings. Keeping both kinds in one map lets a merchant rotate one
// key at a time without a second verification path.
func wechatPlatformKeys(secrets map[string]secret.Secret, s wechatSettings) (map[string]*rsa.PublicKey, error) {
	keys := make(map[string]*rsa.PublicKey)
	for _, name := range []string{"platform_certificate", "old_platform_certificate"} {
		if err := wechatAddCertificate(keys, secrets, name); err != nil {
			return nil, err
		}
	}
	for _, item := range []struct{ name, id string }{
		{"platform_public_key", s.PlatformKeyID},
		{"old_platform_public_key", s.OldPlatformKeyID},
	} {
		if err := wechatAddPublicKey(keys, secrets, item.name, item.id); err != nil {
			return nil, err
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("a platform certificate or WeChat Pay public key is required")
	}
	return keys, nil
}

func wechatAddCertificate(keys map[string]*rsa.PublicKey, secrets map[string]secret.Secret, name string) error {
	text := secrets[name].Reveal()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	cert, err := wechatCertificate(text)
	if err != nil {
		return fmt.Errorf("%s is not a platform certificate", name)
	}
	serial := strings.ToUpper(cert.SerialNumber.Text(16))
	if _, ok := keys[serial]; ok {
		return fmt.Errorf("%s repeats a platform certificate serial", name)
	}
	key, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("%s is not an RSA certificate", name)
	}
	keys[serial] = key
	return nil
}

func wechatAddPublicKey(keys map[string]*rsa.PublicKey, secrets map[string]secret.Secret, name, id string) error {
	text := secrets[name].Reveal()
	if strings.TrimSpace(text) == "" {
		if id != "" {
			return fmt.Errorf("%s is missing while its ID is set", name)
		}
		return nil
	}
	if id == "" {
		return fmt.Errorf("%s requires its public key ID in settings", name)
	}
	key, err := wechatPublicKey(text)
	if err != nil {
		return fmt.Errorf("%s is not a public key", name)
	}
	if _, ok := keys[id]; ok {
		return fmt.Errorf("%s repeats a public key ID", name)
	}
	keys[id] = key
	return nil
}

func wechatCertificate(text string) (*x509.Certificate, error) {
	der, err := wechatDER(text)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	key, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || key.N.BitLen() < wechatpayMinKeyBits {
		return nil, errors.New("certificate does not hold an RSA key of at least 2048 bits")
	}
	return cert, nil
}

func wechatPublicKey(text string) (*rsa.PublicKey, error) {
	der, err := wechatDER(text)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err == nil {
		if key, ok := parsed.(*rsa.PublicKey); ok {
			if key.N.BitLen() < wechatpayMinKeyBits {
				return nil, errors.New("public key is shorter than 2048 bits")
			}
			return key, nil
		}
		return nil, errors.New("public key is not RSA")
	}
	key, err := x509.ParsePKCS1PublicKey(der)
	if err != nil || key.N.BitLen() < wechatpayMinKeyBits {
		return nil, errors.New("public key is not a valid RSA key of at least 2048 bits")
	}
	return key, nil
}

func wechatPrivateKey(text string) (*rsa.PrivateKey, error) {
	der, err := wechatDER(text)
	if err != nil {
		return nil, errors.New("must be a PKCS#8 or PKCS#1 PEM private key")
	}
	if parsed, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if key, ok := parsed.(*rsa.PrivateKey); ok && key.N.BitLen() >= wechatpayMinKeyBits {
			return key, nil
		}
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil && key.N.BitLen() >= wechatpayMinKeyBits {
		return key, nil
	}
	return nil, errors.New("must be an RSA private key of at least 2048 bits")
}

// wechatDER accepts a PEM block or the bare base64 body WeChat's console downloads. The caller decides which key or
// certificate type the bytes must contain.
func wechatDER(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("empty key")
	}
	if block, _ := pem.Decode([]byte(text)); block != nil {
		if strings.Contains(block.Type, "ENCRYPTED") {
			return nil, errors.New("encrypted PEM is not supported")
		}
		return block.Bytes, nil
	}
	compact := strings.Join(strings.Fields(text), "")
	der, err := base64.StdEncoding.DecodeString(compact)
	if err != nil || len(der) == 0 {
		return nil, errors.New("not PEM or base64")
	}
	return der, nil
}

type wechatChannel struct {
	settings wechatSettings
	currency platform.Currency
	private  *rsa.PrivateKey
	apiV3    []byte
	keys     map[string]*rsa.PublicKey
	client   *egress.Client
	now      func() time.Time
}

func (*wechatChannel) Caps() Caps {
	return CapQuery | CapClose | CapRefund | CapQueryRefund | CapRefundKey
}

func (*wechatChannel) Methods() []Method { return nil }

func (*wechatChannel) validateMerchant(in MerchantInput) error {
	for i, m := range in.Methods {
		if m.Upstream != "native" && m.Upstream != "h5" {
			return &FieldError{Field: "/methods/" + strconv.Itoa(i) + "/upstream", Reason: "must be native or h5"}
		}
	}
	return nil
}

func (c *wechatChannel) Create(ctx context.Context, r CreateRequest) (Checkout, error) {
	if r.Total.Currency != c.currency || r.Total.Minor <= 0 {
		return Checkout{}, &Rejected{Code: "invalid_amount", Message: "the order amount must be positive CNY"}
	}
	if !wechatNotifyURL(r.NotifyURL) {
		return Checkout{}, &Rejected{Code: "invalid_notify_url", Message: "the notification address must be an HTTPS URL without a query"}
	}
	subject := wechatText(r.Subject, wechatpayMaxSubjectRunes)
	if subject == "" {
		return Checkout{}, &Rejected{Code: "invalid_subject", Message: "the order needs a description"}
	}
	body := map[string]any{
		"appid":        c.settings.AppID,
		"mchid":        c.settings.MchID,
		"description":  subject,
		"out_trade_no": r.Order.TypeID(),
		"notify_url":   r.NotifyURL,
		"amount":       map[string]any{"total": r.Total.Minor, "currency": "CNY"},
	}
	switch r.Upstream {
	case "native":
		return c.create(ctx, "/v3/pay/transactions/native", body, "native", "")
	case "h5":
		if !r.ClientIP.IsValid() {
			return Checkout{}, &Rejected{Code: "invalid_client_ip", Message: "H5 orders need the payer's IP address"}
		}
		if !wechatReturnURL(r.ReturnURL) {
			return Checkout{}, &Rejected{Code: "invalid_return_url", Message: "the return address must be an HTTPS URL"}
		}
		body["scene_info"] = map[string]any{
			"payer_client_ip": r.ClientIP.String(),
			"h5_info":         map[string]any{"type": "Wap"},
		}
		return c.create(ctx, "/v3/pay/transactions/h5", body, "h5", r.ReturnURL)
	default:
		return Checkout{}, &Rejected{Code: "invalid_method", Message: "the merchant method must map to native or h5"}
	}
}

func (c *wechatChannel) create(ctx context.Context, path string, body map[string]any, product, returnURL string) (Checkout, error) {
	raw, err := jsonx.MarshalCanonical(body)
	if err != nil {
		return Checkout{}, errors.New("wechatpay: encode create request")
	}
	status, response, err := c.do(ctx, http.MethodPost, path, "", raw)
	if err != nil {
		return Checkout{}, err
	}
	if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
		return Checkout{}, wechatFailureOf(status, response).rejected()
	}
	if status != http.StatusOK {
		return Checkout{}, wechatFailureOf(status, response)
	}
	var created struct {
		CodeURL string `json:"code_url"`
		H5URL   string `json:"h5_url"`
	}
	if err := jsonx.Unmarshal(response, &created); err != nil {
		return Checkout{}, errors.New("wechatpay: invalid create response")
	}
	if product == "native" {
		if !wechatCodeURL(created.CodeURL) {
			return Checkout{}, errors.New("wechatpay: invalid code_url")
		}
		return Checkout{QRCode: created.CodeURL}, nil
	}
	if !wechatReturnURL(created.H5URL) {
		return Checkout{}, errors.New("wechatpay: invalid h5_url")
	}
	separator := "?"
	if strings.Contains(created.H5URL, "?") {
		separator = "&"
	}
	return Checkout{PayURL: created.H5URL + separator + "redirect_url=" + url.QueryEscape(returnURL)}, nil
}

func wechatNotifyURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && !u.ForceQuery && u.RawQuery == "" && u.Fragment == ""
}

func wechatReturnURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == ""
}

func wechatCodeURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "weixin" && u.Host != ""
}

// wechatText removes NUL and invalid UTF-8 and cuts by runes. WeChat counts description in characters, not bytes.
func wechatText(s string, maxRunes int) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > maxRunes {
		s = string(runes[:maxRunes])
	}
	return s
}

// wechatReason cuts to the protocol's 80 bytes without splitting a rune.
func wechatReason(s string) string {
	s = wechatText(s, 1000)
	var b strings.Builder
	for _, r := range s {
		if b.Len()+utf8.RuneLen(r) > wechatpayMaxReasonBytes {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func (*wechatChannel) Ack(ok bool) (int, []byte) {
	if ok {
		return http.StatusOK, nil
	}
	return http.StatusInternalServerError, []byte(`{"code":"FAIL","message":"失败"}`)
}

// testConfig asks for a random order that cannot exist. A signed ORDER_NOT_EXIST proves both the merchant signature
// and the configured verification key without creating an order or moving money.
func (c *wechatChannel) testConfig(ctx context.Context) error {
	_, err := c.query(ctx, "probe_"+rand.Text())
	if errors.Is(err, errWechatOrderMissing) {
		return nil
	}
	if err == nil {
		return errors.New("wechatpay: merchant probe found an order")
	}
	return err
}
