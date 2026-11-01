package payments

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"axiom/internal/platform"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
)

// Alipay binds the official Alipay open-platform protocol; money and state stay in Payments.
type Alipay struct{}

// Kind is the protocol name stored on merchant accounts.
func (Alipay) Kind() Kind { return "alipay" }

// The official product codes the merchant's payment methods map to.
const (
	alipayPagePay   = "FAST_INSTANT_TRADE_PAY"
	alipayWapPay    = "QUICK_WAP_WAY"
	alipayPrecreate = "FACE_TO_FACE_PAYMENT"
)

const (
	alipayProductionGateway = "https://openapi.alipay.com/gateway.do"
	alipaySignType          = "RSA2"
	alipayMinKeyBits        = 2048
	alipayRefundGrace       = 10 * time.Minute
)

type alipaySettings struct {
	AppID    string `json:"app_id"`
	SellerID string `json:"seller_id,omitempty"`
	Gateway  string `json:"gateway,omitempty"`
}

// normalizeSettings makes equivalent spellings one stored identity before the settings hash is sealed.
func (Alipay) normalizeSettings(raw []byte) ([]byte, error) {
	var s alipaySettings
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil {
		return nil, errors.New("alipay: invalid settings JSON")
	}
	s.AppID, s.SellerID = strings.TrimSpace(s.AppID), strings.TrimSpace(s.SellerID)
	gateway, err := alipayGateway(s.Gateway)
	if err != nil {
		return nil, err
	}
	s.Gateway = gateway
	return jsonx.MarshalCanonical(s)
}

// identitySettings keeps what binds callbacks and gateways fixed while the merchant has orders in flight.
func (Alipay) identitySettings(raw []byte) ([]byte, error) {
	var s alipaySettings
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil {
		return nil, errors.New("alipay: invalid identity settings")
	}
	s.AppID, s.SellerID = strings.TrimSpace(s.AppID), strings.TrimSpace(s.SellerID)
	gateway, err := alipayGateway(s.Gateway)
	if err != nil {
		return nil, err
	}
	return jsonx.MarshalCanonical(struct {
		AppID, SellerID, Gateway string
	}{s.AppID, s.SellerID, gateway})
}

// alipayGateway accepts the sandbox and test gateways as well as the production one, never a plaintext or ambiguous URL.
func alipayGateway(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return alipayProductionGateway, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("alipay: gateway must be an https address without user info, query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

type alipayChannel struct {
	settings    alipaySettings
	currency    platform.Currency
	private     *rsa.PrivateKey
	public, old *rsa.PublicKey
	client      *egress.Client
	now         func() time.Time
}

// Open validates the protocol configuration without making network calls.
func (Alipay) Open(c MerchantConfig) (Channel, error) {
	s, err := alipaySettingsOf(c)
	if err != nil {
		return nil, err
	}
	private, public, old, err := alipayKeysOf(c)
	if err != nil {
		return nil, err
	}
	return &alipayChannel{settings: s, currency: c.Currency, private: private, public: public, old: old, client: c.Client, now: time.Now}, nil
}

func alipaySettingsOf(c MerchantConfig) (alipaySettings, error) {
	var s alipaySettings
	if err := jsonx.UnmarshalKnown(c.Settings, &s); err != nil {
		return s, errors.New("alipay: invalid settings JSON")
	}
	s.AppID, s.SellerID = strings.TrimSpace(s.AppID), strings.TrimSpace(s.SellerID)
	if !alipayNumber(s.AppID) {
		return s, errors.New("alipay: require a numeric app_id")
	}
	if s.SellerID != "" && !alipayNumber(s.SellerID) {
		return s, errors.New("alipay: seller_id must be numeric")
	}
	gateway, err := alipayGateway(s.Gateway)
	if err != nil {
		return s, err
	}
	s.Gateway = gateway
	if c.Client == nil || c.Currency == (platform.Currency{}) || c.Currency.String() != "CNY" {
		return s, errors.New("alipay: require client and CNY")
	}
	return s, nil
}

func alipayKeysOf(c MerchantConfig) (*rsa.PrivateKey, *rsa.PublicKey, *rsa.PublicKey, error) {
	for name := range c.Secrets {
		if name != "private_key" && name != "public_key" && name != "old_public_key" {
			return nil, nil, nil, errors.New("alipay: unknown secret name")
		}
	}
	private, err := alipayPrivateKey(c.Secrets["private_key"].Reveal())
	if err != nil {
		return nil, nil, nil, err
	}
	public, err := alipayPublicKey(c.Secrets["public_key"].Reveal())
	if err != nil {
		return nil, nil, nil, err
	}
	var old *rsa.PublicKey
	if value := c.Secrets["old_public_key"].Reveal(); value != "" {
		if old, err = alipayPublicKey(value); err != nil {
			return nil, nil, nil, errors.New("alipay: invalid old public key")
		}
	}
	return private, public, old, nil
}

// alipayNumber is the shape of an app_id and seller_id: digits only, neither empty nor unbounded.
func alipayNumber(s string) bool {
	if len(s) < 6 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// alipayDER reads a PEM block or the bare base64 Alipay's console hands out.
func alipayDER(text string) (block *pem.Block, der []byte, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil, errors.New("alipay: key is missing")
	}
	if block, _ = pem.Decode([]byte(text)); block != nil {
		if strings.Contains(block.Type, "ENCRYPTED") {
			return nil, nil, errors.New("alipay: encrypted keys are not supported")
		}
		return block, block.Bytes, nil
	}
	der, err = base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, nil, errors.New("alipay: invalid key encoding")
	}
	return nil, der, nil
}

type alipayKey interface {
	*rsa.PrivateKey | *rsa.PublicKey
}

// alipayKeyOf decodes one key and enforces the minimum RSA size the protocol requires.
func alipayKeyOf[T alipayKey](text, kind string, parse func(*pem.Block, []byte) (T, bool)) (T, error) {
	var zero T
	block, der, err := alipayDER(text)
	if err != nil {
		return zero, fmt.Errorf("alipay: invalid %s", kind)
	}
	key, ok := parse(block, der)
	if !ok || alipayBits(key) < alipayMinKeyBits {
		return zero, fmt.Errorf("alipay: invalid %s", kind)
	}
	return key, nil
}

func alipayBits[T alipayKey](key T) int {
	switch k := any(key).(type) {
	case *rsa.PrivateKey:
		return k.N.BitLen()
	case *rsa.PublicKey:
		return k.N.BitLen()
	}
	return 0
}

func alipayPrivateKey(text string) (*rsa.PrivateKey, error) {
	return alipayKeyOf(text, "private key", alipayPrivate)
}

func alipayPrivate(block *pem.Block, der []byte) (*rsa.PrivateKey, bool) {
	switch {
	case block == nil:
		if key, ok := alipayPKCS8Private(der); ok {
			return key, true
		}
		key, err := x509.ParsePKCS1PrivateKey(der)
		return key, err == nil
	case block.Type == "PRIVATE KEY":
		return alipayPKCS8Private(der)
	case block.Type == "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(der)
		return key, err == nil
	}
	return nil, false
}

func alipayPKCS8Private(der []byte) (*rsa.PrivateKey, bool) {
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, false
	}
	key, ok := parsed.(*rsa.PrivateKey)
	return key, ok
}

func alipayPublicKey(text string) (*rsa.PublicKey, error) {
	return alipayKeyOf(text, "public key", alipayPublic)
}

func alipayPublic(block *pem.Block, der []byte) (*rsa.PublicKey, bool) {
	switch {
	case block == nil:
		if key, ok := alipayPKIXPublic(der); ok {
			return key, true
		}
		key, err := x509.ParsePKCS1PublicKey(der)
		return key, err == nil
	case block.Type == "PUBLIC KEY":
		return alipayPKIXPublic(der)
	case block.Type == "RSA PUBLIC KEY":
		key, err := x509.ParsePKCS1PublicKey(der)
		return key, err == nil
	}
	return nil, false
}

func alipayPKIXPublic(der []byte) (*rsa.PublicKey, bool) {
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, false
	}
	key, ok := parsed.(*rsa.PublicKey)
	return key, ok
}

// Caps is what every Alipay merchant can do; statements are not part of the core yet.
func (*alipayChannel) Caps() Caps {
	return CapQuery | CapClose | CapRefund | CapQueryRefund | CapRefundKey
}

// Methods nil permits any configured method; the product code lives in its upstream mapping.
func (*alipayChannel) Methods() []Method { return nil }

func (*alipayChannel) validateMerchant(in MerchantInput) error {
	for i, m := range in.Methods {
		switch m.Upstream {
		case alipayPagePay, alipayWapPay, alipayPrecreate:
		default:
			return &FieldError{Field: "/methods/" + strconv.Itoa(i) + "/upstream", Reason: "must be an Alipay product code"}
		}
	}
	return nil
}

// Create signs a page or wap URL locally, or asks the gateway for a QR code with precreate.
func (c *alipayChannel) Create(ctx context.Context, r CreateRequest) (Checkout, error) {
	if r.Total.Currency != c.currency || r.Total.Minor <= 0 {
		return Checkout{}, &Rejected{Code: "invalid_amount", Message: "unsupported amount or currency"}
	}
	if !alipayHTTPS(r.ReturnURL) || !alipayHTTPS(r.NotifyURL) {
		return Checkout{}, &Rejected{Code: "unsafe_link", Message: "return and notify URLs must be https"}
	}
	biz := map[string]any{"out_trade_no": r.Order.TypeID(), "total_amount": formatMinorText(r.Total), "subject": r.Subject}
	switch r.Upstream {
	case alipayPagePay:
		biz["product_code"] = alipayPagePay
		return c.redirect("alipay.trade.page.pay", r, biz, true)
	case alipayWapPay:
		biz["product_code"] = alipayWapPay
		return c.redirect("alipay.trade.wap.pay", r, biz, true)
	case alipayPrecreate:
		biz["product_code"] = alipayPrecreate
		return c.precreate(ctx, r, biz)
	default:
		return Checkout{}, &Rejected{Code: "invalid_method", Message: "unsupported Alipay product"}
	}
}

func alipayHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func (c *alipayChannel) redirect(method string, r CreateRequest, biz any, returnURL bool) (Checkout, error) {
	v, err := c.params(method, r, biz, returnURL)
	if err != nil {
		return Checkout{}, err
	}
	return Checkout{PayURL: c.settings.Gateway + "?" + v.Encode()}, nil
}

func (c *alipayChannel) precreate(ctx context.Context, r CreateRequest, biz any) (Checkout, error) {
	v, err := c.params("alipay.trade.precreate", r, biz, false)
	if err != nil {
		return Checkout{}, err
	}
	node, failure, err := c.post(ctx, "alipay.trade.precreate", v)
	if err != nil {
		return Checkout{}, err
	}
	if failure.found() {
		return Checkout{}, failure
	}
	var response struct {
		QRCode     string `json:"qr_code"`
		OutTradeNo string `json:"out_trade_no"`
	}
	if err = jsonx.Unmarshal(node, &response); err != nil {
		return Checkout{}, errors.New("alipay: invalid create response")
	}
	if response.OutTradeNo != r.Order.TypeID() || !alipayHTTPS(response.QRCode) {
		return Checkout{}, errors.New("alipay: invalid create response")
	}
	return Checkout{QRCode: response.QRCode}, nil
}

func (c *alipayChannel) params(method string, r CreateRequest, biz any, returnURL bool) (url.Values, error) {
	content, err := jsonx.MarshalCanonical(biz)
	if err != nil {
		return nil, errors.New("alipay: cannot encode the order")
	}
	v := url.Values{
		"app_id": {c.settings.AppID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"},
		"sign_type": {alipaySignType}, "timestamp": {c.now().In(alipayZone).Format("2006-01-02 15:04:05")},
		"version": {"1.0"}, "notify_url": {r.NotifyURL}, "biz_content": {string(content)},
	}
	if returnURL {
		v.Set("return_url", r.ReturnURL)
	}
	return c.signed(v)
}

// signed appends the RSA2 signature to the parameters; sign itself is never part of the signed text.
func (c *alipayChannel) signed(v url.Values) (url.Values, error) {
	sign, err := alipaySign(v, c.private)
	if err != nil {
		return nil, err
	}
	v.Set("sign", sign)
	return v, nil
}

var alipayZone = time.FixedZone("CST", 8*60*60)

// alipaySign signs the official request text: every non-empty parameter except sign, sorted by name, values unescaped.
func alipaySign(v url.Values, key *rsa.PrivateKey) (string, error) {
	sum := sha256.Sum256([]byte(alipaySignText(v, true)))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", errors.New("alipay: cannot sign the request")
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

// alipaySignText excludes sign always, and sign_type too when verifying a notification, as the official documents say.
func alipaySignText(v url.Values, includeSignType bool) string {
	keys := make([]string, 0, len(v))
	for name, values := range v {
		if name == "sign" || (!includeSignType && name == "sign_type") || len(values) == 0 || values[0] == "" {
			continue
		}
		keys = append(keys, name)
	}
	slices.Sort(keys)
	var b strings.Builder
	for i, name := range keys {
		if i != 0 {
			b.WriteByte('&')
		}
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(v.Get(name))
	}
	return b.String()
}

// alipayVerify checks a SHA256withRSA signature over the exact bytes the gateway signed.
func alipayVerify(text []byte, signature string, key *rsa.PublicKey) bool {
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(text)
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], raw) == nil
}

func (c *alipayChannel) verify(text []byte, signature string) bool {
	if alipayVerify(text, signature, c.public) {
		return true
	}
	return c.old != nil && alipayVerify(text, signature, c.old)
}

func (*alipayChannel) Ack(ok bool) (int, []byte) {
	if ok {
		return http.StatusOK, []byte("success")
	}
	return http.StatusInternalServerError, []byte("fail")
}
