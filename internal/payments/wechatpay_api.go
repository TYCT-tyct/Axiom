package payments

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
)

const wechatpayMaxResponse = 1 << 20

// errWechatOrderMissing is a signed ORDER_NOT_EXIST, not a failed query. Public Query turns it into PayPending; the
// admin probe uses it to prove the credentials.
var errWechatOrderMissing = errors.New("wechatpay: order does not exist")

// wechatFailure is an authenticated non-2xx answer or an unusable signed body. It deliberately carries only the status
// and the sanitized WeChat code: messages can echo request fields.
type wechatFailure struct {
	Status int
	Code   string
}

func (f *wechatFailure) Error() string {
	if f.Code == "" {
		return fmt.Sprintf("wechatpay: gateway HTTP %d", f.Status)
	}
	return fmt.Sprintf("wechatpay: gateway HTTP %d %s", f.Status, f.Code)
}

func wechatFailureOf(status int, body []byte) *wechatFailure {
	f := &wechatFailure{Status: status, Code: "invalid_error"}
	var e struct {
		Code string `json:"code"`
	}
	if jsonx.Unmarshal(body, &e) == nil {
		f.Code = wechatCode(e.Code)
	}
	return f
}

// wechatCode keeps an upstream code safe to log and bounded, whether or not the body was signed by WeChat.
func wechatCode(code string) string {
	var b strings.Builder
	for _, r := range code {
		if b.Len() == 64 {
			break
		}
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "invalid_error"
	}
	return b.String()
}

func (f *wechatFailure) rejected() *Rejected {
	return &Rejected{Code: "wechatpay." + f.Code, Message: "the gateway refused the request"}
}

// do signs one API v3 request, verifies the response's platform signature before returning its body, and bounds the
// call at ten seconds. A missing or unknown signature is an error even for an error status: the body is untrusted.
func (c *wechatChannel) do(ctx context.Context, method, path, query string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := c.request(ctx, method, path, query, body)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, errors.New("wechatpay: gateway request failed")
	}
	defer resp.Body.Close() //nolint:errcheck // the body is read below or discarded; closing cannot change the outcome
	response, err := egress.ReadBody(resp.Body, wechatpayMaxResponse)
	if err != nil {
		return 0, nil, errors.New("wechatpay: cannot read gateway response")
	}
	if len(response) == 0 {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp.StatusCode, nil, nil
		}
		return 0, nil, &wechatFailure{Status: resp.StatusCode, Code: "empty_error"}
	}
	if err := c.verify(resp.Header, response); err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, response, nil
}

func (c *wechatChannel) request(ctx context.Context, method, path, query string, body []byte) (*http.Request, error) {
	target := c.settings.Gateway + path
	if query != "" {
		target += "?" + query
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, errors.New("wechatpay: cannot build request")
	}
	timestamp := strconv.FormatInt(c.now().Unix(), 10)
	nonce := rand.Text()
	signature, err := c.sign(method, path, query, timestamp, nonce, body)
	if err != nil {
		return nil, errors.New("wechatpay: cannot sign request")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", fmt.Sprintf(
		`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",signature="%s",timestamp="%s",serial_no="%s"`,
		c.settings.MchID, nonce, signature, timestamp, c.settings.SerialNo,
	))
	return req, nil
}

func (c *wechatChannel) sign(method, path, query, timestamp, nonce string, body []byte) (string, error) {
	text := method + "\n" + path
	if query != "" {
		text += "?" + query
	}
	text += "\n" + timestamp + "\n" + nonce + "\n" + string(body) + "\n"
	sum := sha256.Sum256([]byte(text))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.private, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func (c *wechatChannel) verify(h http.Header, body []byte) error {
	serial, err := wechatHeader(h, "Wechatpay-Serial")
	if err != nil {
		return err
	}
	timestamp, err := wechatHeader(h, "Wechatpay-Timestamp")
	if err != nil {
		return err
	}
	nonce, err := wechatHeader(h, "Wechatpay-Nonce")
	if err != nil {
		return err
	}
	signature, err := wechatHeader(h, "Wechatpay-Signature")
	if err != nil {
		return err
	}
	key := c.keys[strings.ToUpper(serial)]
	if key == nil {
		return errors.New("wechatpay: gateway used an unknown verification key")
	}
	if err := wechatVerify(timestamp+"\n"+nonce+"\n"+string(body)+"\n", signature, key); err != nil {
		return err
	}
	return nil
}

func wechatHeader(h http.Header, name string) (string, error) {
	values := h.Values(name)
	if len(values) != 1 {
		return "", errors.New("wechatpay: gateway response is missing a signature header")
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", errors.New("wechatpay: gateway response has an empty signature header")
	}
	return value, nil
}

func wechatVerify(text, signature string, key *rsa.PublicKey) error {
	decoded, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return errors.New("wechatpay: invalid gateway signature encoding")
	}
	sum := sha256.Sum256([]byte(text))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], decoded); err != nil {
		return errors.New("wechatpay: invalid gateway signature")
	}
	return nil
}

func (c *wechatChannel) Query(ctx context.Context, outTradeNo, _ string) (Payment, error) {
	payment, err := c.query(ctx, outTradeNo)
	if errors.Is(err, errWechatOrderMissing) {
		return Payment{State: PayPending, Paid: Money{Currency: c.currency}}, nil
	}
	return payment, err
}

func (c *wechatChannel) query(ctx context.Context, outTradeNo string) (Payment, error) {
	if outTradeNo == "" || len(outTradeNo) > 32 {
		return Payment{}, errors.New("wechatpay: order number must be 1 to 32 bytes")
	}
	path := "/v3/pay/transactions/out-trade-no/" + url.PathEscape(outTradeNo)
	query := "mchid=" + url.QueryEscape(c.settings.MchID)
	status, body, err := c.do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return Payment{}, err
	}
	if status == http.StatusNotFound {
		failure := wechatFailureOf(status, body)
		if failure.Code == "ORDER_NOT_EXIST" {
			return Payment{}, errWechatOrderMissing
		}
		return Payment{}, failure
	}
	if status != http.StatusOK {
		return Payment{}, wechatFailureOf(status, body)
	}
	return c.parseQuery(outTradeNo, body)
}

func (c *wechatChannel) parseQuery(outTradeNo string, body []byte) (Payment, error) {
	var response struct {
		AppID         string `json:"appid"`
		MchID         string `json:"mchid"`
		OutTradeNo    string `json:"out_trade_no"`
		TransactionID string `json:"transaction_id"`
		TradeState    string `json:"trade_state"`
		Amount        struct {
			Total    jsonx.Raw `json:"total"`
			Currency string    `json:"currency"`
		} `json:"amount"`
	}
	if err := jsonx.Unmarshal(body, &response); err != nil {
		return Payment{}, errors.New("wechatpay: invalid query response")
	}
	if response.MchID != c.settings.MchID || response.AppID != c.settings.AppID {
		return Payment{}, errors.New("wechatpay: query belongs to another merchant")
	}
	if response.OutTradeNo != outTradeNo {
		return Payment{}, errors.New("wechatpay: query echoed another order")
	}
	state, err := wechatPayState(response.TradeState)
	if err != nil {
		return Payment{}, err
	}
	amount := wechatMinor(response.Amount.Total)
	if response.Amount.Currency != "" && response.Amount.Currency != "CNY" {
		amount = -1
	}
	return Payment{State: state, TradeNo: response.TransactionID, Paid: Money{Currency: c.currency, Minor: amount}}, nil
}

func wechatPayState(state string) (PayState, error) {
	switch state {
	case "SUCCESS", "REFUND":
		return PayPaid, nil
	case "NOTPAY", "USERPAYING":
		return PayPending, nil
	case "CLOSED", "REVOKED", "PAYERROR":
		return PayClosed, nil
	default:
		return PayPending, errors.New("wechatpay: unknown trade state")
	}
}

func (c *wechatChannel) Close(ctx context.Context, order OrderID, _ string) error {
	body, err := jsonx.MarshalCanonical(map[string]string{"mchid": c.settings.MchID})
	if err != nil {
		return errors.New("wechatpay: encode close request")
	}
	path := "/v3/pay/transactions/out-trade-no/" + url.PathEscape(order.TypeID()) + "/close"
	status, response, err := c.do(ctx, http.MethodPost, path, "", body)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	failure := wechatFailureOf(status, response)
	if failure.Code == "ORDER_NOT_EXIST" || failure.Code == "ORDER_CLOSED" {
		return nil
	}
	return failure
}

func (c *wechatChannel) Refund(ctx context.Context, r RefundCall) (RefundState, error) {
	if r.Amount.Currency != c.currency || r.Amount.Minor <= 0 {
		return RefundState{}, &Rejected{Code: "invalid_amount", Message: "the refund amount must be positive CNY"}
	}
	total, err := c.refundTotal(ctx, r)
	if err != nil {
		return RefundState{}, err
	}
	amount := map[string]any{"refund": r.Amount.Minor, "total": total, "currency": "CNY"}
	request := map[string]any{
		"out_trade_no":  r.Order.TypeID(),
		"out_refund_no": r.Refund.TypeID(),
		"amount":        amount,
	}
	if reason := wechatReason(r.Reason); reason != "" {
		request["reason"] = reason
	}
	body, err := jsonx.MarshalCanonical(request)
	if err != nil {
		return RefundState{}, errors.New("wechatpay: encode refund request")
	}
	status, response, err := c.do(ctx, http.MethodPost, "/v3/refund/domestic/refunds", "", body)
	if err != nil {
		return RefundState{}, err
	}
	switch {
	case status == http.StatusTooManyRequests:
		// The refund may already be accepted: the docs say to query it and retry with the same number.
		return RefundState{}, wechatFailureOf(status, response)
	case status >= 400 && status < 500:
		return RefundState{}, wechatFailureOf(status, response).rejected()
	case status != http.StatusOK:
		return RefundState{}, wechatFailureOf(status, response)
	}
	return c.refundState(response, r, total)
}

// refundTotal reads the original paid total, which WeChat requires in every refund request. Every rejection here
// happens before the refund request is sent, so the core must treat it as a definite failure rather than an unknown.
func (c *wechatChannel) refundTotal(ctx context.Context, r RefundCall) (int64, error) {
	payment, err := c.query(ctx, r.Order.TypeID())
	if err != nil {
		if errors.Is(err, errWechatOrderMissing) {
			return 0, &Rejected{Code: "wechatpay.ORDER_NOT_EXIST", Message: "the order does not exist"}
		}
		// The refund request is sent only after this query, so none of its failures can have moved money.
		return 0, &Rejected{Code: "wechatpay.REFUND_NOT_SENT", Message: "the order query failed before the refund was sent"}
	}
	switch {
	case payment.State == PayClosed:
		return 0, &Rejected{Code: "wechatpay.ORDER_CLOSED", Message: "the order is closed"}
	case payment.State != PayPaid:
		return 0, &Rejected{Code: "wechatpay.ORDER_NOT_PAID", Message: "the order is not confirmed paid"}
	case payment.Paid.Minor <= 0:
		return 0, &Rejected{Code: "wechatpay.ORDER_AMOUNT_UNAVAILABLE", Message: "the paid amount is unavailable"}
	case r.Amount.Minor > payment.Paid.Minor:
		return 0, &Rejected{Code: "wechatpay.REFUND_EXCEEDS_ORDER", Message: "the refund exceeds the paid amount"}
	}
	return payment.Paid.Minor, nil
}

func (c *wechatChannel) QueryRefund(ctx context.Context, r RefundCall) (RefundState, error) {
	path := "/v3/refund/domestic/refunds/" + url.PathEscape(r.Refund.TypeID())
	status, body, err := c.do(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return RefundState{}, err
	}
	if status == http.StatusNotFound {
		failure := wechatFailureOf(status, body)
		if failure.Code == "RESOURCE_NOT_EXISTS" {
			return RefundState{State: RefundPending}, nil
		}
		return RefundState{}, failure
	}
	if status != http.StatusOK {
		return RefundState{}, wechatFailureOf(status, body)
	}
	return c.refundState(body, r, 0)
}

type wechatRefundResult struct {
	RefundID    string `json:"refund_id"`
	OutRefundNo string `json:"out_refund_no"`
	OutTradeNo  string `json:"out_trade_no"`
	Status      string `json:"status"`
	Amount      struct {
		Total    jsonx.Raw `json:"total"`
		Refund   jsonx.Raw `json:"refund"`
		Currency string    `json:"currency"`
	} `json:"amount"`
}

// refundState checks every echo that could move the wrong amount, then maps WeChat's status. ABNORMAL stays pending:
// the cash has not reached the user and may still be handled by hand, so unfreezing would risk paying twice.
func (*wechatChannel) refundState(body []byte, r RefundCall, total int64) (RefundState, error) {
	var response wechatRefundResult
	if err := jsonx.Unmarshal(body, &response); err != nil {
		return RefundState{}, errors.New("wechatpay: invalid refund response")
	}
	if err := wechatRefundEcho(response, r, total); err != nil {
		return RefundState{}, err
	}
	provider := response.RefundID
	if provider == "" {
		provider = r.Refund.TypeID()
	}
	switch response.Status {
	case "SUCCESS":
		return RefundState{State: RefundSucceeded, ProviderRef: provider}, nil
	case "PROCESSING", "ABNORMAL":
		return RefundState{State: RefundPending, ProviderRef: provider}, nil
	case "CLOSED":
		return RefundState{State: RefundFailed, ProviderRef: provider}, nil
	default:
		return RefundState{}, errors.New("wechatpay: unknown refund status")
	}
}

func wechatRefundEcho(response wechatRefundResult, r RefundCall, total int64) error {
	if response.OutRefundNo != r.Refund.TypeID() || response.OutTradeNo != r.Order.TypeID() {
		return errors.New("wechatpay: refund response echoed another refund")
	}
	if wechatMinor(response.Amount.Refund) != r.Amount.Minor {
		return errors.New("wechatpay: refund response echoed another amount")
	}
	if total > 0 && wechatMinor(response.Amount.Total) != total {
		return errors.New("wechatpay: refund response echoed another order total")
	}
	if response.Amount.Currency != "" && response.Amount.Currency != "CNY" {
		return errors.New("wechatpay: refund currency is not CNY")
	}
	return nil
}

// wechatMinor reads a JSON integer amount. Missing or null is the interface's zero (query again); anything that is
// not a non-negative int64, including a string or a decimal, is -1 (review).
func wechatMinor(raw jsonx.Raw) int64 {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0
	}
	if text[0] == '"' || text[0] == '-' {
		return -1
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value < 0 {
		return -1
	}
	return value
}
