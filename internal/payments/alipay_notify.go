package payments

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"net/url"
)

// Verify checks the official asynchronous notification: a form POST signed with RSA2 over every non-empty parameter
// except sign and sign_type, then bound to this merchant by app_id and, when configured, seller_id.
func (c *alipayChannel) Verify(_ context.Context, cb Callback) (Notice, error) {
	raw, err := alipayCallbackForm(cb)
	if err != nil {
		return Notice{}, err
	}
	v, err := url.ParseQuery(raw)
	if err != nil {
		return Notice{}, errors.New("alipay: invalid callback encoding")
	}
	for _, values := range v {
		if len(values) != 1 {
			return Notice{}, errors.New("alipay: ambiguous callback parameters")
		}
	}
	if v.Get("sign_type") != alipaySignType {
		return Notice{}, errors.New("alipay: unsupported signature type")
	}
	if !c.verify([]byte(alipaySignText(v, false)), v.Get("sign")) {
		return Notice{}, errors.New("alipay: invalid signature")
	}
	return c.notice(v)
}

// notice binds an already verified form to this merchant and turns the trade status into a Notice.
func (c *alipayChannel) notice(v url.Values) (Notice, error) {
	if v.Get("app_id") != c.settings.AppID {
		return Notice{}, errors.New("alipay: merchant mismatch")
	}
	if c.settings.SellerID != "" && v.Get("seller_id") != c.settings.SellerID {
		return Notice{}, errors.New("alipay: merchant mismatch")
	}
	// A signed event of another kind is not a payment; acknowledge it without touching an order.
	if t := v.Get("notify_type"); t != "" && t != "trade_status_sync" {
		return Notice{Relevant: false}, nil
	}
	status := v.Get("trade_status")
	success := status == "TRADE_SUCCESS" || status == "TRADE_FINISHED"
	amount := parseMinorText(v.Get("total_amount"), digits(c.currency))
	return Notice{
		Relevant: true, Success: success,
		Order: v.Get("out_trade_no"), TradeNo: v.Get("trade_no"),
		Paid: Money{Currency: c.currency, Minor: amount},
		Raw:  "alipay notification",
	}, nil
}

func alipayCallbackForm(cb Callback) (string, error) {
	if len(cb.RawQuery) > callbackMax || len(cb.Body) > callbackMax {
		return "", errors.New("alipay: callback too large")
	}
	if cb.Method != http.MethodPost {
		return "", errors.New("alipay: callback must be a POST form")
	}
	media, _, err := mime.ParseMediaType(cb.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" || cb.RawQuery != "" {
		return "", errors.New("alipay: invalid callback form")
	}
	return string(cb.Body), nil
}
