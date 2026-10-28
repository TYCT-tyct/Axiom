package payments

import (
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // EasyPay's wire protocol requires MD5
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Signature and whitelist adapted from sub2api backend/internal/payment/provider/easypay.go (a3298b46e).
func easyPaySign(v url.Values, key string) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		if k != "sign" && k != "sign_type" && v.Get(k) != "" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+v.Get(k))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key)) //nolint:gosec // required by the upstream protocol
	return hex.EncodeToString(sum[:])
}

var easyPayNotifyParams = map[string]bool{
	"pid": true, "trade_no": true, "out_trade_no": true, "type": true, "name": true,
	"money": true, "trade_status": true, "param": true, "sign": true, "sign_type": true,
}

func (c *easyPayChannel) Verify(_ context.Context, cb Callback) (Notice, error) {
	raw, err := easyPayCallbackBody(cb)
	if err != nil {
		return Notice{}, err
	}
	v, err := easyPayNotifyValues(raw)
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
	amount := easyPayMinor(v.Get("money"), digits(c.currency))
	return Notice{Relevant: true, Success: v.Get("trade_status") == "TRADE_SUCCESS", Order: v.Get("out_trade_no"), TradeNo: v.Get("trade_no"), Paid: Money{Currency: c.currency, Minor: amount}, Raw: "easypay notification"}, nil
}

func easyPayNotifyValues(raw string) (url.Values, error) {
	v, err := url.ParseQuery(raw)
	if err != nil {
		return nil, errors.New("easypay: invalid callback encoding")
	}
	for k, values := range v {
		if !easyPayNotifyParams[k] || len(values) != 1 || (k != "name" && strings.ContainsAny(values[0], "&=")) {
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
		media, _, err := mime.ParseMediaType(cb.Header.Get("Content-Type"))
		if err != nil || media != "application/x-www-form-urlencoded" || cb.RawQuery != "" {
			return "", errors.New("easypay: invalid callback form")
		}
		return string(cb.Body), nil
	default:
		return "", errors.New("easypay: unsupported callback method")
	}
}

// -1 preserves a verified, unrepresentable amount for review; zero means settlement data is still missing.
func easyPayMinor(text string, places int) int64 {
	if text == "" {
		return 0
	}
	whole, fraction, err := easyPayDecimal(text)
	if err != nil {
		return -1
	}
	fraction = strings.TrimRight(fraction, "0")
	if len(fraction) > places {
		return -1
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	// Bound before concatenating or parsing a gateway-controlled integer.
	if len(whole) > 19 {
		return -1
	}
	minorText := strings.TrimLeft(whole+fraction+strings.Repeat("0", places-len(fraction)), "0")
	if minorText == "" {
		return -1
	}
	if len(minorText) > 19 || (len(minorText) == 19 && minorText > "9223372036854775807") {
		return -1
	}
	minor, err := strconv.ParseInt(minorText, 10, 64)
	if err != nil {
		return -1
	}
	return minor
}

func easyPayDecimal(text string) (string, string, error) {
	whole, fraction, hasDot := strings.Cut(text, ".")
	if whole == "" || strings.Trim(whole, "0123456789") != "" || (hasDot && (fraction == "" || strings.Trim(fraction, "0123456789") != "")) {
		return "", "", errors.New("easypay: invalid decimal amount")
	}
	return whole, fraction, nil
}

func easyPayFormat(m Money) string {
	text := strconv.FormatInt(m.Minor, 10)
	places := digits(m.Currency)
	if places == 0 {
		return text
	}
	if len(text) <= places {
		text = strings.Repeat("0", places+1-len(text)) + text
	}
	return text[:len(text)-places] + "." + text[len(text)-places:]
}
