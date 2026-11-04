package payments

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"axiom/internal/platform"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
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
