package payments

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"axiom/internal/platform"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

// EasyPay binds the configurable EasyPay protocol; money and state stay in Payments.
type EasyPay struct{}

// Kind is the protocol name stored on merchant accounts.
func (EasyPay) Kind() Kind { return "easypay" }

// normalizeSettings runs before identity comparison and sealing so endpoint aliases have one stored identity.
func (EasyPay) normalizeSettings(raw []byte) ([]byte, error) {
	var s easyPaySettings
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil {
		return nil, errors.New("easypay: invalid settings JSON")
	}
	base, err := easyPayBase(s.Gateway)
	if err != nil {
		return nil, err
	}
	s.Gateway = base
	s = s.withDefaults()
	if s.SignType == "" {
		s.SignType = "MD5"
	}
	return jsonx.MarshalCanonical(s)
}

// identitySettings keeps protocol identity fixed while allowing operational settings to change.
func (EasyPay) identitySettings(raw []byte) ([]byte, error) {
	var s easyPaySettings
	if err := jsonx.UnmarshalKnown(raw, &s); err != nil {
		return nil, errors.New("easypay: invalid identity settings")
	}
	if s.SignType == "" {
		s.SignType = "MD5"
	}
	return jsonx.MarshalCanonical(struct {
		Preset   string
		Gateway  string
		PID      string
		SignType string
	}{s.Preset, s.Gateway, s.PID, s.SignType})
}

type easyPaySettings struct {
	Preset   string            `json:"preset,omitempty"`
	Gateway  string            `json:"gateway"`
	PID      string            `json:"pid"`
	Mode     string            `json:"mode"`
	SignType string            `json:"sign_type,omitempty"`
	CIDs     map[Method]string `json:"cids,omitempty"`
	Query    bool              `json:"query,omitempty"`
	Close    bool              `json:"close,omitempty"`
	Refund   bool              `json:"refund,omitempty"`
}

type easyPayChannel struct {
	settings    easyPaySettings
	currency    platform.Currency
	key, oldKey secret.Secret
	client      *egress.Client
}

// Open validates the protocol configuration without making network calls.
func (EasyPay) Open(c MerchantConfig) (Channel, error) {
	var s easyPaySettings
	if err := jsonx.UnmarshalKnown(c.Settings, &s); err != nil {
		return nil, errors.New("easypay: invalid settings JSON")
	}
	base, err := easyPayBase(s.Gateway)
	if err != nil {
		return nil, err
	}
	s.Gateway = base
	s = s.withDefaults()
	if err := s.check(); err != nil {
		return nil, err
	}
	if c.Client == nil || c.Currency == (platform.Currency{}) || c.Secrets["key"].Reveal() == "" {
		return nil, errors.New("easypay: require client, currency and key")
	}
	if err := s.checkPreset(c.Currency); err != nil {
		return nil, err
	}
	for name := range c.Secrets {
		if name != "key" && name != "old_key" {
			return nil, errors.New("easypay: unknown secret name")
		}
	}
	return &easyPayChannel{settings: s, currency: c.Currency, key: c.Secrets["key"], oldKey: c.Secrets["old_key"], client: c.Client}, nil
}

func (s easyPaySettings) withDefaults() easyPaySettings {
	if s.Preset == "epusdt" {
		if s.Mode == "" {
			s.Mode = "redirect"
		}
		s.Query = true
	}
	return s
}

func (s easyPaySettings) check() error {
	if !easyPayToken(s.PID) || (s.Mode != "redirect" && s.Mode != "api") || (s.SignType != "" && s.SignType != "MD5") {
		return errors.New("easypay: require pid, mode redirect/api and MD5 signing")
	}
	for method, cid := range s.CIDs {
		if !easyPayToken(string(method)) || !easyPayToken(cid) {
			return errors.New("easypay: invalid channel ID")
		}
	}
	return nil
}

func easyPayToken(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "&=\x00\r\n")
}

func easyPayBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Scheme != "https" {
		return "", errors.New("easypay: invalid gateway URL")
	}
	u.RawQuery, u.Fragment, u.RawFragment, u.RawPath = "", "", "", ""
	u.ForceQuery = false
	u.Path = strings.TrimRight(u.Path, "/")
	for _, endpoint := range []string{"/submit.php", "/mapi.php", "/api.php"} {
		if strings.HasSuffix(strings.ToLower(u.Path), endpoint) {
			u.Path = strings.TrimRight(u.Path[:len(u.Path)-len(endpoint)], "/")
			break
		}
	}
	return u.String(), nil
}

func easyPayHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Opaque == ""
}

func (c *easyPayChannel) Caps() Caps {
	var caps Caps
	if c.settings.Query {
		caps |= CapQuery
	}
	if c.settings.Close {
		caps |= CapClose
	}
	if c.settings.Refund {
		caps |= CapRefund
	}
	return caps
}

func (c *easyPayChannel) Methods() []Method {
	if c.settings.Preset == "epusdt" {
		return []Method{"usdt.tron"}
	}
	return nil
}

func (c *easyPayChannel) Create(ctx context.Context, r CreateRequest) (Checkout, error) {
	if !easyPayToken(r.Upstream) || r.Total.Currency != c.currency || r.Total.Minor <= 0 || !easyPayHTTPS(r.ReturnURL) {
		return Checkout{}, &Rejected{Code: "invalid_order", Message: "invalid method, amount or return URL"}
	}
	v := c.createParams(r)
	if c.settings.Mode == "redirect" {
		result := Checkout{PayURL: c.settings.Gateway + "/submit.php?" + v.Encode()}
		return easyPayCheckout(result)
	}
	body, err := c.post(ctx, "/mapi.php", v, false)
	if err != nil {
		return Checkout{}, err
	}
	return c.createResponse(body, r.Mobile)
}

func (c *easyPayChannel) createParams(r CreateRequest) url.Values {
	v := url.Values{"pid": {c.settings.PID}, "type": {r.Upstream}, "out_trade_no": {r.Order.TypeID()}, "notify_url": {r.NotifyURL}, "return_url": {r.ReturnURL}, "name": {r.Subject}, "money": {formatMinorText(r.Total)}}
	if c.settings.Preset == "epusdt" {
		v.Set("currency", strings.ToLower(c.currency.String()))
	}
	if cid := c.settings.CIDs[r.Method]; cid != "" {
		v.Set("cid", cid)
	}
	if r.Mobile {
		v.Set("device", "mobile")
	}
	if c.settings.Mode == "api" && r.ClientIP.IsValid() {
		v.Set("clientip", r.ClientIP.String())
	}
	v.Set("sign", easyPaySign(v, c.key.Reveal()))
	v.Set("sign_type", "MD5")
	return v
}

func (c *easyPayChannel) createResponse(body []byte, mobile bool) (Checkout, error) {
	var response struct {
		Code    jsonx.Raw `json:"code"`
		TradeNo string    `json:"trade_no"`
		PayURL  string    `json:"payurl"`
		PayURL2 string    `json:"payurl2"`
		QRCode  string    `json:"qrcode"`
	}
	if err := jsonx.Unmarshal(body, &response); err != nil {
		return Checkout{}, errors.New("easypay: invalid create JSON")
	}
	if err := easyPayCode(response.Code, true); err != nil {
		return Checkout{}, err
	}
	link := response.PayURL
	if mobile && response.PayURL2 != "" {
		link = response.PayURL2
	}
	pay, err := c.link(link)
	if err != nil {
		return Checkout{}, err
	}
	qr, err := c.link(response.QRCode)
	if err != nil {
		return Checkout{}, err
	}
	return easyPayCheckout(Checkout{TradeNo: response.TradeNo, PayURL: pay, QRCode: qr})
}

func (c *easyPayChannel) link(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "//") {
		return "", &Rejected{Code: "unsafe_link", Message: "protocol-relative link"}
	}
	if strings.HasPrefix(ref, "/") {
		base, err := url.Parse(c.settings.Gateway)
		if err != nil {
			return "", errors.New("easypay: invalid gateway URL")
		}
		relative, err := url.Parse(ref)
		if err != nil {
			return "", &Rejected{Code: "unsafe_link", Message: "invalid relative link"}
		}
		ref = base.ResolveReference(relative).String()
	}
	return ref, nil
}

func easyPayCheckout(result Checkout) (Checkout, error) {
	if err := result.check(); err != nil {
		return Checkout{}, &Rejected{Code: "unsafe_link", Message: "invalid checkout link"}
	}
	if result.PayURL == "" && result.QRCode == "" {
		return Checkout{}, errors.New("easypay: checkout has no payment link")
	}
	return result, nil
}

func (*easyPayChannel) Ack(ok bool) (int, []byte) {
	if ok {
		return http.StatusOK, []byte("success")
	}
	return http.StatusInternalServerError, []byte("fail")
}
