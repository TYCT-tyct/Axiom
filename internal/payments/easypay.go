package payments

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
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
	return jsonx.MarshalCanonical(s)
}

type easyPayMethod struct {
	Type string `json:"type"`
	CID  string `json:"cid,omitempty"`
}

type easyPaySettings struct {
	Gateway  string                   `json:"gateway"`
	PID      string                   `json:"pid"`
	Mode     string                   `json:"mode"`
	SignType string                   `json:"sign_type,omitempty"`
	Methods  map[Method]easyPayMethod `json:"methods"`
	Query    bool                     `json:"query,omitempty"`
	Close    bool                     `json:"close,omitempty"`
	Refund   bool                     `json:"refund,omitempty"`
}

type easyPayChannel struct {
	settings    easyPaySettings
	merchant    MerchantID
	currency    platform.Currency
	notifyURL   string
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
	if err := s.check(); err != nil {
		return nil, err
	}
	if c.Client == nil || c.Currency == (platform.Currency{}) || c.Secrets["key"].Reveal() == "" {
		return nil, errors.New("easypay: require client, currency and key")
	}
	if c.NotifyURL != "" && !easyPayHTTPS(c.NotifyURL) {
		return nil, errors.New("easypay: invalid notify URL")
	}
	return &easyPayChannel{settings: s, merchant: c.ID, currency: c.Currency, notifyURL: c.NotifyURL, key: c.Secrets["key"], oldKey: c.Secrets["old_key"], client: c.Client}, nil
}

func (s easyPaySettings) check() error {
	if !easyPayToken(s.PID) || (s.Mode != "redirect" && s.Mode != "api") || (s.SignType != "" && s.SignType != "MD5") {
		return errors.New("easypay: require pid, mode redirect/api and MD5 signing")
	}
	if len(s.Methods) == 0 {
		return errors.New("easypay: require payment method mappings")
	}
	for method, m := range s.Methods {
		if !easyPayToken(string(method)) || !easyPayToken(m.Type) || (m.CID != "" && !easyPayToken(m.CID)) {
			return errors.New("easypay: invalid method mapping")
		}
	}
	return nil
}

func easyPayToken(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "&=\x00\r\n")
}

func easyPayBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") {
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
	methods := make([]Method, 0, len(c.settings.Methods))
	for method := range c.settings.Methods {
		methods = append(methods, method)
	}
	slices.Sort(methods)
	return methods
}

func (c *easyPayChannel) Create(ctx context.Context, r CreateRequest) (Checkout, error) {
	m, ok := c.settings.Methods[r.Method]
	if !ok || r.Total.Currency != c.currency || r.Total.Minor <= 0 || !easyPayHTTPS(r.ReturnURL) {
		return Checkout{}, &Rejected{Code: "invalid_order", Message: "invalid method, amount or return URL"}
	}
	v, err := c.createParams(r, m)
	if err != nil {
		return Checkout{}, err
	}
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

func (c *easyPayChannel) createParams(r CreateRequest, m easyPayMethod) (url.Values, error) {
	notify := c.notifyURL
	if notify == "" {
		u, err := url.Parse(r.ReturnURL)
		if err != nil {
			return nil, errors.New("easypay: invalid return URL")
		}
		notify = (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/api/payments/v1/notify/" + c.merchant.TypeID()}).String()
	}
	v := url.Values{"pid": {c.settings.PID}, "type": {m.Type}, "out_trade_no": {r.Order.TypeID()}, "notify_url": {notify}, "return_url": {r.ReturnURL}, "name": {r.Subject}, "money": {easyPayFormat(r.Total)}}
	if m.CID != "" {
		v.Set("cid", m.CID)
	}
	if r.Mobile {
		v.Set("device", "mobile")
	}
	if c.settings.Mode == "api" && r.ClientIP.IsValid() {
		v.Set("clientip", r.ClientIP.String())
	}
	v.Set("sign", easyPaySign(v, c.key.Reveal()))
	v.Set("sign_type", "MD5")
	return v, nil
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
