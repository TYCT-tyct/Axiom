package payments

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

// EzPay is a self-hosted, EasyPay-compatible USDT gateway. Its create request follows the RFC 3986 example in its
// documentation, while its callback signs the decoded values as they arrive; the two protocols differ and are kept
// apart here.
func ezPayCreateSign(v url.Values, key string) string {
	return md5Sign(v, key, func(value string) string { return ezPayRFC3986(ezPayDecodeIfEncoded(value)) })
}

// ezPayDecodeIfEncoded mirrors EzPay's DecodeIfEncoded so that a value containing a literal percent escapes the same
// way on both sides of the signature.
func ezPayDecodeIfEncoded(s string) string {
	if !strings.ContainsAny(s, "%+") {
		return s
	}
	if decoded, err := url.QueryUnescape(s); err == nil {
		return decoded
	}
	return s
}

func ezPayRFC3986(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "+", "%20"), "%7E", "~")
}

func ezPayPID(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func (s easyPaySettings) checkEzPayPreset(currency platform.Currency) error {
	switch {
	case s.Mode != "redirect", s.Close, s.Refund, len(s.CIDs) != 0:
		return errors.New("easypay: EzPay requires redirect checkout with query and offline refunds without cids or close")
	case currency.String() != "USD":
		return errors.New("easypay: EzPay requires USD fiat amounts")
	case !ezPayPID(s.PID):
		return errors.New("easypay: EzPay requires a 1-32 character pid of letters, digits, underscores or hyphens")
	default:
		return nil
	}
}

type ezPayOrderResponse struct {
	Code        jsonx.Raw `json:"code"`
	Msg         string    `json:"msg"`
	TradeStatus jsonx.Raw `json:"trade_status"`
	Money       jsonx.Raw `json:"money"`
	TradeNo     jsonx.Raw `json:"trade_no"`
	OutTradeNo  jsonx.Raw `json:"out_trade_no"`
	Type        jsonx.Raw `json:"type"`
	Currency    jsonx.Raw `json:"currency"`
}

func (c *easyPayChannel) ezPayQuery(ctx context.Context, order string) (Payment, error) {
	v := c.credentials("order")
	v.Set("out_trade_no", order)
	body, err := c.request(ctx, http.MethodGet, "/api.php", v, true)
	if err != nil {
		return Payment{}, err
	}
	var response ezPayOrderResponse
	if err = jsonx.Unmarshal(body, &response); err != nil {
		return Payment{}, errors.New("easypay: invalid query JSON")
	}
	code, err := easyPayScalar(response.Code)
	if err != nil {
		return Payment{}, err
	}
	if code == "-1" && easyPayOrderMissing(response.Msg) {
		return Payment{State: PayPending}, nil
	}
	if err = easyPayCode(response.Code, true); err != nil {
		return Payment{}, err
	}
	return c.ezPayPayment(response, order)
}

func (c *easyPayChannel) ezPayPayment(r ezPayOrderResponse, order string) (Payment, error) {
	echo, err := easyPayScalar(r.OutTradeNo)
	if err != nil || echo == "" || echo != order {
		return Payment{}, errors.New("easypay: echoed order mismatch")
	}
	state, err := ezPayState(r.TradeStatus)
	if err != nil {
		return Payment{}, err
	}
	trade, err := easyPayScalar(r.TradeNo)
	if err != nil {
		return Payment{}, err
	}
	typ, err := easyPayScalar(r.Type)
	if err != nil {
		return Payment{}, err
	}
	if typ != "" && typ != "usdt_trc20" {
		return Payment{State: state, TradeNo: trade, Paid: Money{Currency: c.currency, Minor: -1}}, nil
	}
	amount, err := easyPayScalar(r.Money)
	if err != nil {
		return Payment{}, err
	}
	currency, err := easyPayScalar(r.Currency)
	if err != nil {
		return Payment{}, err
	}
	return Payment{State: state, TradeNo: trade, Paid: c.ezPayMoney(amount, currency)}, nil
}

func ezPayState(raw jsonx.Raw) (PayState, error) {
	status, err := easyPayScalar(raw)
	if err != nil {
		return 0, err
	}
	switch status {
	case "TRADE_SUCCESS":
		return PayPaid, nil
	case "WAIT_BUYER_PAY":
		return PayPending, nil
	case "TRADE_CLOSED":
		return PayClosed, nil
	default:
		return 0, errors.New("easypay: unknown EzPay trade status")
	}
}

// ezPayMoney reads the fiat amount; a callback for another fiat currency is kept as that currency so the core can hold
// it for review instead of crediting a converted value.
func (c *easyPayChannel) ezPayMoney(text, code string) Money {
	if code == "" {
		return Money{Currency: c.currency, Minor: parseMinorText(text, c.currency.MinorDigits())}
	}
	currency, err := platform.ParseCurrency(strings.ToUpper(code))
	if err != nil {
		return Money{Currency: c.currency, Minor: -1}
	}
	return Money{Currency: currency, Minor: parseMinorText(text, currency.MinorDigits())}
}

func (c *easyPayChannel) ezPayNotice(v url.Values) Notice {
	paid := c.ezPayMoney(v.Get("money"), v.Get("currency"))
	if typ := strings.ToLower(v.Get("type")); v.Get("currency") == "" || (typ != "" && typ != "usdt_trc20") {
		// A signed event without its fiat currency, or for another upstream, cannot settle this order; keep it for review.
		paid.Minor = -1
	}
	return Notice{
		Relevant: true, Success: v.Get("trade_status") == "TRADE_SUCCESS",
		Order: v.Get("out_trade_no"), TradeNo: v.Get("trade_no"), Paid: paid, Raw: "ezpay notification",
	}
}
