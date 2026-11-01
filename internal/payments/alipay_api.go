package payments

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
)

// The Alipay business error codes the channel can act on. Everything else is an outcome that is still unknown.
const (
	alipayTradeNotExist     = "ACQ.TRADE_NOT_EXIST"
	alipayTradeStatusError  = "ACQ.TRADE_STATUS_ERROR"
	alipayRefundAmountError = "ACQ.REFUND_AMT_NOT_EQUAL_TOTAL"
	alipayTradeFinished     = "ACQ.TRADE_HAS_FINISHED"
)

type alipayFailure struct {
	Code, SubCode string
	Signed        bool // a failure inside the signed method node; an unsigned error_response is not trusted
}

func (f alipayFailure) found() bool { return f.Code != "" }

func (f alipayFailure) Error() string {
	return "alipay: the gateway refused: " + f.Code + " " + f.SubCode
}

func (f alipayFailure) rejected() *Rejected {
	code := f.SubCode
	if code == "" {
		code = f.Code
	}
	return &Rejected{Code: "alipay." + code, Message: "the gateway refused"}
}

// post never wraps network or decoder errors: both may echo the signed response body.
func (c *alipayChannel) post(ctx context.Context, method string, v url.Values) (jsonx.Raw, alipayFailure, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.settings.Gateway, strings.NewReader(v.Encode()))
	if err != nil {
		return nil, alipayFailure{}, errors.New("alipay: cannot construct request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, alipayFailure{}, errors.New("alipay: gateway request failed")
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // reading the bounded response below determines the result
	body, err := egress.ReadBody(resp.Body, 1<<20)
	if err != nil {
		return nil, alipayFailure{}, errors.New("alipay: cannot read bounded gateway response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, alipayFailure{}, fmt.Errorf("alipay: gateway HTTP %d", resp.StatusCode)
	}
	return c.response(body, method)
}

// response verifies the signature over the method node's exact bytes before trusting any field, as the official
// protocol signs the response text rather than a re-encoding of it.
func (c *alipayChannel) response(body []byte, method string) (jsonx.Raw, alipayFailure, error) {
	var top map[string]jsonx.Raw
	if err := jsonx.Unmarshal(body, &top); err != nil {
		return nil, alipayFailure{}, errors.New("alipay: invalid gateway JSON")
	}
	node, ok := top[strings.ReplaceAll(method, ".", "_")+"_response"]
	if !ok {
		return c.errorResponse(top)
	}
	signature, signed := alipayResponseSign(top)
	if !signed {
		return nil, alipayFailure{}, errors.New("alipay: gateway response is unsigned")
	}
	if !c.verify(node, signature) {
		return nil, alipayFailure{}, errors.New("alipay: invalid gateway signature")
	}
	failure, err := alipayFailureOf(node)
	if err != nil {
		return nil, alipayFailure{}, err
	}
	failure.Signed = failure.found()
	return node, failure, nil
}

// errorResponse reads the unsigned error node, accepting its signature only when one actually verifies.
func (c *alipayChannel) errorResponse(top map[string]jsonx.Raw) (jsonx.Raw, alipayFailure, error) {
	node, ok := top["error_response"]
	if !ok {
		return nil, alipayFailure{}, errors.New("alipay: gateway response has no result")
	}
	failure, err := alipayFailureOf(node)
	if err != nil || !failure.found() {
		return nil, alipayFailure{}, errors.New("alipay: invalid gateway response")
	}
	if signature, signed := alipayResponseSign(top); signed && c.verify(node, signature) {
		failure.Signed = true
	}
	return nil, failure, nil
}

func alipayResponseSign(top map[string]jsonx.Raw) (string, bool) {
	raw, ok := top["sign"]
	if !ok {
		return "", false
	}
	var signature string
	if jsonx.Unmarshal(raw, &signature) != nil || signature == "" {
		return "", false
	}
	return signature, true
}

func alipayFailureOf(node jsonx.Raw) (alipayFailure, error) {
	var r struct {
		Code    string `json:"code"`
		SubCode string `json:"sub_code"`
	}
	if err := jsonx.Unmarshal(node, &r); err != nil {
		return alipayFailure{}, errors.New("alipay: invalid gateway result")
	}
	if r.Code == "" || r.Code == "10000" {
		return alipayFailure{}, nil
	}
	return alipayFailure{Code: r.Code, SubCode: r.SubCode}, nil
}

// call sends one API request and leaves the signature check to post.
func (c *alipayChannel) call(ctx context.Context, method string, biz any) (jsonx.Raw, alipayFailure, error) {
	content, err := jsonx.MarshalCanonical(biz)
	if err != nil {
		return nil, alipayFailure{}, errors.New("alipay: cannot encode the request")
	}
	v, err := c.signed(url.Values{
		"app_id": {c.settings.AppID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"},
		"sign_type": {alipaySignType}, "timestamp": {c.now().In(alipayZone).Format("2006-01-02 15:04:05")},
		"version": {"1.0"}, "biz_content": {string(content)},
	})
	if err != nil {
		return nil, alipayFailure{}, err
	}
	return c.post(ctx, method, v)
}

// Query asks the gateway by our order number; the trade may not exist yet, which is still a pending order.
func (c *alipayChannel) Query(ctx context.Context, outTradeNo, _ string) (Payment, error) {
	node, failure, err := c.call(ctx, "alipay.trade.query", map[string]any{"out_trade_no": outTradeNo})
	if err != nil {
		return Payment{}, err
	}
	if failure.found() {
		if failure.Signed && failure.SubCode == alipayTradeNotExist {
			return Payment{State: PayPending}, nil
		}
		return Payment{}, failure
	}
	var r struct {
		OutTradeNo  string `json:"out_trade_no"`
		TradeNo     string `json:"trade_no"`
		TradeStatus string `json:"trade_status"`
		TotalAmount string `json:"total_amount"`
	}
	if err = jsonx.Unmarshal(node, &r); err != nil {
		return Payment{}, errors.New("alipay: invalid query response")
	}
	if r.OutTradeNo != outTradeNo {
		return Payment{}, errors.New("alipay: query echoed another order")
	}
	var state PayState
	switch r.TradeStatus {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		state = PayPaid
	case "TRADE_CLOSED":
		state = PayClosed
	case "WAIT_BUYER_PAY":
		state = PayPending
	default:
		return Payment{}, errors.New("alipay: unknown trade status")
	}
	paid := Money{Currency: c.currency, Minor: parseMinorText(r.TotalAmount, digits(c.currency))}
	return Payment{State: state, TradeNo: r.TradeNo, Paid: paid}, nil
}

// Close closes a trade; a trade that does not exist is already closed for our purposes.
func (c *alipayChannel) Close(ctx context.Context, order OrderID, _ string) error {
	_, failure, err := c.call(ctx, "alipay.trade.close", map[string]any{"out_trade_no": order.TypeID()})
	if err != nil {
		return err
	}
	if !failure.found() || failure.Signed && failure.SubCode == alipayTradeNotExist {
		return nil
	}
	return failure
}

// Refund uses the refund ID as out_request_no, so a resend of the same refund cannot refund twice.
func (c *alipayChannel) Refund(ctx context.Context, r RefundCall) (RefundState, error) {
	if r.Amount.Currency != c.currency || r.Amount.Minor <= 0 {
		return RefundState{}, &Rejected{Code: "invalid_amount", Message: "unsupported amount or currency"}
	}
	outRequestNo := r.Refund.TypeID()
	biz := map[string]any{
		"out_trade_no":   r.Order.TypeID(),
		"refund_amount":  formatMinorText(r.Amount),
		"out_request_no": outRequestNo,
	}
	if reason := alipayReason(r.Reason); reason != "" {
		biz["refund_reason"] = reason
	}
	node, failure, err := c.call(ctx, "alipay.trade.refund", biz)
	if err != nil {
		return RefundState{}, err
	}
	if failure.found() {
		if failure.Signed && alipayRefundRefused(failure.SubCode) {
			return RefundState{}, failure.rejected()
		}
		return RefundState{}, failure
	}
	return c.refundResponse(node, r, outRequestNo)
}

func (c *alipayChannel) refundResponse(node jsonx.Raw, r RefundCall, outRequestNo string) (RefundState, error) {
	var result struct {
		OutTradeNo string `json:"out_trade_no"`
		FundChange string `json:"fund_change"`
		RefundFee  string `json:"refund_fee"`
	}
	if err := jsonx.Unmarshal(node, &result); err != nil {
		return RefundState{}, errors.New("alipay: invalid refund response")
	}
	if result.OutTradeNo != r.Order.TypeID() {
		return RefundState{}, errors.New("alipay: refund echoed another order")
	}
	if result.FundChange == "Y" && result.RefundFee != "" && parseMinorText(result.RefundFee, digits(c.currency)) != r.Amount.Minor {
		return RefundState{}, errors.New("alipay: refund echoed another amount")
	}
	state := RefundPending
	if result.FundChange == "Y" {
		state = RefundSucceeded
	}
	return RefundState{State: state, ProviderRef: outRequestNo}, nil
}

// QueryRefund resolves a refund by its out_request_no; absence is final only after the documented grace period.
func (c *alipayChannel) QueryRefund(ctx context.Context, r RefundCall) (RefundState, error) {
	outRequestNo := r.ProviderRef
	if outRequestNo == "" {
		outRequestNo = r.Refund.TypeID()
	}
	node, failure, err := c.call(ctx, "alipay.trade.fastpay.refund.query", map[string]any{
		"out_trade_no": r.Order.TypeID(), "out_request_no": outRequestNo,
	})
	if err != nil {
		return RefundState{}, err
	}
	if failure.found() {
		if failure.Signed && failure.SubCode == alipayTradeNotExist {
			return RefundState{State: RefundFailed, ProviderRef: outRequestNo}, nil
		}
		return RefundState{}, failure
	}
	return c.refundQueryResult(node, outRequestNo, r.Amount, r.SentAt)
}

func (c *alipayChannel) refundQueryResult(node jsonx.Raw, outRequestNo string, amount Money, sentAt time.Time) (RefundState, error) {
	var result struct {
		OutRequestNo string `json:"out_request_no"`
		RefundStatus string `json:"refund_status"`
		RefundAmount string `json:"refund_amount"`
	}
	if err := jsonx.Unmarshal(node, &result); err != nil {
		return RefundState{}, errors.New("alipay: invalid refund query response")
	}
	if result.OutRequestNo != "" && result.OutRequestNo != outRequestNo {
		return RefundState{}, errors.New("alipay: refund query echoed another request")
	}
	if result.RefundStatus == "REFUND_SUCCESS" && result.RefundAmount != "" && parseMinorText(result.RefundAmount, digits(c.currency)) != amount.Minor {
		return RefundState{}, errors.New("alipay: refund query echoed another amount")
	}
	if result.RefundStatus == "REFUND_SUCCESS" {
		return RefundState{State: RefundSucceeded, ProviderRef: outRequestNo}, nil
	}
	if alipayEmptyRefundQuery(result.OutRequestNo, result.RefundStatus, result.RefundAmount) &&
		!sentAt.IsZero() && c.now().Sub(sentAt) >= alipayRefundGrace {
		return RefundState{State: RefundFailed, ProviderRef: outRequestNo}, nil
	}
	return RefundState{State: RefundPending, ProviderRef: outRequestNo}, nil
}

// emptyRefundQuery recognises the no-record answer; some gateways still echo a zero refund amount.
func alipayEmptyRefundQuery(outRequestNo, refundStatus, refundAmount string) bool {
	if outRequestNo != "" || refundStatus != "" {
		return false
	}
	return refundAmount == "" || alipayZeroAmount(refundAmount)
}

// alipayZeroAmount accepts a zero such as 0.00 without treating a malformed amount as a no-record answer.
func alipayZeroAmount(text string) bool {
	whole, fraction, err := decimalParts(text)
	if err != nil {
		return false
	}
	return strings.Trim(whole, "0") == "" && strings.Trim(fraction, "0") == ""
}

// testConfig proves the application credentials with a read-only query for an order number that cannot exist.
func (c *alipayChannel) testConfig(ctx context.Context) error {
	_, failure, err := c.call(ctx, "alipay.trade.query", map[string]any{"out_trade_no": "probe_" + rand.Text()})
	if err != nil {
		return err
	}
	if !failure.found() {
		return errors.New("alipay: merchant probe found a trade")
	}
	if failure.SubCode == alipayTradeNotExist {
		return nil
	}
	return failure
}

// alipayRefundRefused is a signed business error that means the gateway did not refund; other errors stay unknown.
func alipayRefundRefused(subCode string) bool {
	switch subCode {
	case alipayTradeNotExist, alipayTradeStatusError, alipayRefundAmountError, alipayTradeFinished:
		return true
	}
	return false
}

// alipayReason keeps the short reason Alipay accepts and never lets a raw NUL or invalid UTF-8 into the request.
func alipayReason(reason string) string {
	reason = strings.ReplaceAll(strings.ToValidUTF8(reason, ""), "\x00", "")
	runes := []rune(strings.TrimSpace(reason))
	if len(runes) > 256 {
		runes = runes[:256]
	}
	return string(runes)
}
