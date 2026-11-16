package payments

import (
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // EasyPay's wire protocol requires MD5
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Signature and whitelist adapted from sub2api backend/internal/payment/provider/easypay.go (a3298b46e).
func easyPaySign(v url.Values, key string) string {
	return md5Sign(v, key, nil)
}

func md5Sign(v url.Values, key string, encode func(string) string) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		if k != "sign" && k != "sign_type" && v.Get(k) != "" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var parts []string
	for _, k := range keys {
		value := v.Get(k)
		if encode != nil {
			value = encode(value)
		}
		parts = append(parts, k+"="+value)
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key)) //nolint:gosec // required by the upstream protocol
	return hex.EncodeToString(sum[:])
}

var easyPayNotifyParams = map[string]bool{
	"pid": true, "trade_no": true, "out_trade_no": true, "type": true, "name": true,
	"money": true, "trade_status": true, "param": true, "sign": true, "sign_type": true,
}

// EzPay's callback adds the fiat and paid currency amounts to the EasyPay fields; all are signed.
var ezPayNotifyParams = map[string]bool{"currency": true, "pay_currency": true, "pay_amount": true}

func (c *easyPayChannel) Verify(_ context.Context, cb Callback) (Notice, error) {
	raw, err := easyPayCallbackBody(cb)
	if err != nil {
		return Notice{}, err
	}
	v, err := easyPayNotifyValues(raw, c.settings.Preset == "ezpay")
	if err != nil {
		return Notice{}, err
	}
	valid := hmac.Equal([]byte(v.Get("sign")), []byte(easyPaySign(v, c.key.Reveal())))
	if c.oldKey.Reveal() != "" {
		oldValid := hmac.Equal([]byte(v.Get("sign")), []byte(easyPaySign(v, c.oldKey.Reveal())))
		valid = valid || oldValid
	}
	if !valid {
		return Notice{}, errors.New("easypay: invalid signature")
	}
	if v.Get("pid") != c.settings.PID {
		return Notice{}, errors.New("easypay: merchant mismatch")
	}
	if c.settings.Preset == "ezpay" {
		return c.ezPayNotice(v), nil
	}
	amount := parseMinorText(v.Get("money"), c.currency.MinorDigits())
	return Notice{Relevant: true, Success: v.Get("trade_status") == "TRADE_SUCCESS", Order: v.Get("out_trade_no"), TradeNo: v.Get("trade_no"), Paid: Money{Currency: c.currency, Minor: amount}, Raw: "easypay notification"}, nil
}

func easyPayNotifyValues(raw string, ezpay bool) (url.Values, error) {
	v, err := url.ParseQuery(raw)
	if err != nil {
		return nil, errors.New("easypay: invalid callback encoding")
	}
	for k, values := range v {
		allowed := easyPayNotifyParams[k] || (ezpay && ezPayNotifyParams[k])
		if !allowed || len(values) != 1 || (k != "name" && strings.ContainsAny(values[0], "&=")) {
			return nil, errors.New("easypay: ambiguous callback parameters")
		}
	}
	if v.Get("sign_type") != "" && v.Get("sign_type") != "MD5" {
		return nil, errors.New("easypay: unsupported signature type")
	}
	return v, nil
}

func easyPayCallbackBody(cb Callback) (string, error) {
	if len(cb.RawQuery) > callbackMax || len(cb.Body) > callbackMax {
		return "", errors.New("easypay: callback too large")
	}
	switch cb.Method {
	case http.MethodGet:
		if len(cb.Body) != 0 {
			return "", errors.New("easypay: GET callback has a body")
		}
		return cb.RawQuery, nil
	case http.MethodPost:
		return callbackForm(cb, "easypay")
	default:
		return "", errors.New("easypay: unsupported callback method")
	}
}
