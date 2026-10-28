package payments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
)

// post never wraps network or decoder errors: both may quote the secret-bearing URL or echoed response body.
func (c *easyPayChannel) post(ctx context.Context, path string, v url.Values, query bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	target := c.settings.Gateway + path
	if query {
		target += "?" + v.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(v.Encode()))
	if err != nil {
		return nil, errors.New("easypay: cannot construct request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("easypay: gateway request failed")
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // reading the bounded response below determines the result
	body, err := egress.ReadBody(resp.Body, 1<<20)
	if err != nil {
		return nil, errors.New("easypay: cannot read bounded gateway response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("easypay: gateway HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func (c *easyPayChannel) credentials(act string) url.Values {
	return url.Values{"act": {act}, "pid": {c.settings.PID}, "key": {c.key.Reveal()}}
}

func easyPayScalar(raw jsonx.Raw) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw.Kind() == '"' {
		var s string
		if err := jsonx.Unmarshal(raw, &s); err != nil {
			return "", errors.New("easypay: invalid scalar")
		}
		return strings.TrimSpace(s), nil
	}
	if raw.Kind() == '0' {
		return string(raw), nil
	}
	return "", errors.New("easypay: expected string or number")
}

func easyPayCode(raw jsonx.Raw, required bool) error {
	code, err := easyPayScalar(raw)
	if err != nil {
		return err
	}
	if code == "" && !required {
		return nil
	}
	n, err := strconv.ParseInt(code, 10, 64)
	if err != nil {
		return errors.New("easypay: invalid response code")
	}
	if n != 1 {
		return &Rejected{Code: "gateway_refused", Message: "gateway returned a failure code"}
	}
	return nil
}

type easyPayOrderData struct {
	TradeStatus jsonx.Raw `json:"trade_status"`
	Status      jsonx.Raw `json:"status"`
	Money       jsonx.Raw `json:"money"`
	TradeNo     jsonx.Raw `json:"trade_no"`
	OutTradeNo  jsonx.Raw `json:"out_trade_no"`
}

type easyPayOrderResponse struct {
	easyPayOrderData `json:",inline"`
	Code             jsonx.Raw        `json:"code"`
	Data             easyPayOrderData `json:"data"`
}

func easyPayPick(top, nested jsonx.Raw) jsonx.Raw {
	if len(top) != 0 && string(top) != "null" {
		return top
	}
	return nested
}

func (c *easyPayChannel) Query(ctx context.Context, order, _ string) (Payment, error) {
	if !c.Caps().Has(CapQuery) {
		panic("easypay: query is not declared")
	}
	v := c.credentials("order")
	v.Set("out_trade_no", order)
	body, err := c.post(ctx, "/api.php", v, true)
	if err != nil {
		return Payment{}, err
	}
	var response easyPayOrderResponse
	if err = jsonx.Unmarshal(body, &response); err != nil {
		return Payment{}, errors.New("easypay: invalid query JSON")
	}
	if err = easyPayCode(response.Code, false); err != nil {
		return Payment{}, err
	}
	return c.payment(response, order)
}

func (c *easyPayChannel) payment(r easyPayOrderResponse, order string) (Payment, error) {
	// Check both echoes, even when top-level data otherwise takes precedence.
	for _, raw := range []jsonx.Raw{r.OutTradeNo, r.Data.OutTradeNo} {
		echoed, err := easyPayScalar(raw)
		if err != nil || (echoed != "" && echoed != order) {
			return Payment{}, errors.New("easypay: echoed order mismatch")
		}
	}
	paid, err := easyPayPaid(r)
	if err != nil {
		return Payment{}, err
	}
	trade, err := easyPayScalar(easyPayPick(r.TradeNo, r.Data.TradeNo))
	if err != nil {
		return Payment{}, err
	}
	text, err := easyPayScalar(easyPayPick(r.Money, r.Data.Money))
	if err != nil {
		return Payment{}, err
	}
	amount := easyPayMinor(text, digits(c.currency))
	state := PayPending
	if paid {
		state = PayPaid
	}
	return Payment{State: state, TradeNo: trade, Paid: Money{Currency: c.currency, Minor: amount}}, nil
}

func easyPayPaid(r easyPayOrderResponse) (bool, error) {
	status, err := easyPayScalar(easyPayPick(r.TradeStatus, r.Data.TradeStatus))
	if err != nil {
		return false, err
	}
	if status != "" {
		return status == "TRADE_SUCCESS", nil
	}
	status, err = easyPayScalar(easyPayPick(r.Status, r.Data.Status))
	return status == "1", err
}

func (c *easyPayChannel) Close(ctx context.Context, order OrderID, _ string) error {
	if !c.Caps().Has(CapClose) {
		panic("easypay: close is not declared")
	}
	v := c.credentials("close")
	v.Set("out_trade_no", order.TypeID())
	body, err := c.post(ctx, "/api.php", v, true)
	if err != nil {
		return err
	}
	var response struct {
		Code jsonx.Raw `json:"code"`
	}
	if err = jsonx.Unmarshal(body, &response); err != nil {
		return errors.New("easypay: invalid close JSON")
	}
	return easyPayCode(response.Code, true)
}

func (c *easyPayChannel) Refund(ctx context.Context, r RefundCall) (RefundState, error) {
	if !c.Caps().Has(CapRefund) {
		panic("easypay: refund is not declared")
	}
	if r.Amount.Currency != c.currency || r.Amount.Minor <= 0 {
		return RefundState{}, &Rejected{Code: "invalid_amount", Message: "invalid refund amount"}
	}
	v := c.credentials("refund")
	v.Set("money", easyPayFormat(r.Amount))
	v.Set("out_trade_no", r.Order.TypeID())
	state, missing, err := c.refund(ctx, v)
	if missing && r.TradeNo != "" {
		v.Del("out_trade_no")
		v.Set("trade_no", r.TradeNo)
		state, _, err = c.refund(ctx, v)
	}
	return state, err
}

func (c *easyPayChannel) refund(ctx context.Context, v url.Values) (RefundState, bool, error) {
	body, err := c.post(ctx, "/api.php?act=refund", v, false)
	if err != nil {
		return RefundState{}, false, err
	}
	var response struct {
		Code jsonx.Raw `json:"code"`
		Msg  string    `json:"msg"`
	}
	if err = jsonx.Unmarshal(body, &response); err != nil {
		return RefundState{}, false, errors.New("easypay: invalid refund JSON")
	}
	err = easyPayCode(response.Code, true)
	if err != nil {
		var refused *Rejected
		missing := errors.As(err, &refused) && easyPayOrderMissing(response.Msg)
		return RefundState{}, missing, err
	}
	ref := v.Get("out_trade_no")
	if ref == "" {
		ref = v.Get("trade_no")
	}
	return RefundState{State: RefundSucceeded, ProviderRef: ref}, false, nil
}

func easyPayOrderMissing(msg string) bool {
	switch strings.ToLower(strings.TrimSpace(msg)) {
	case "订单不存在", "订单号不存在", "订单编号不存在", "order not found", "order does not exist", "trade not found":
		return true
	default:
		return false
	}
}

func (*easyPayChannel) QueryRefund(context.Context, RefundCall) (RefundState, error) {
	panic("easypay: query refund is not declared")
}
