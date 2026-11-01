package payments

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"axiom/internal/platform/jsonx"
)

type wechatResource struct {
	Algorithm      string `json:"algorithm"`
	Ciphertext     string `json:"ciphertext"`
	Nonce          string `json:"nonce"`
	AssociatedData string `json:"associated_data"`
	OriginalType   string `json:"original_type"`
}

type wechatCallbackEnvelope struct {
	EventType    string         `json:"event_type"`
	ResourceType string         `json:"resource_type"`
	Resource     wechatResource `json:"resource"`
}

// Verify authenticates and decrypts a callback, then binds it to this merchant account. The body is verified over the
// exact bytes received: decoding and re-encoding could change whitespace or member order and invalidate the signature.
func (c *wechatChannel) Verify(_ context.Context, cb Callback) (Notice, error) {
	if err := wechatCallbackRequest(cb); err != nil {
		return Notice{}, err
	}
	if err := c.callbackTimestamp(cb.Header); err != nil {
		return Notice{}, err
	}
	if err := c.verify(cb.Header, cb.Body); err != nil {
		return Notice{}, err
	}
	var envelope wechatCallbackEnvelope
	if err := jsonx.Unmarshal(cb.Body, &envelope); err != nil {
		return Notice{}, errors.New("wechatpay: invalid callback envelope")
	}
	if envelope.EventType != "TRANSACTION.SUCCESS" {
		return Notice{Relevant: false}, nil
	}
	if err := envelope.Resource.check(envelope.ResourceType); err != nil {
		return Notice{}, err
	}
	plaintext, err := c.decrypt(envelope.Resource)
	if err != nil {
		return Notice{}, err
	}
	return c.callbackNotice(plaintext)
}

func wechatCallbackRequest(cb Callback) error {
	if cb.Method != http.MethodPost || cb.RawQuery != "" {
		return errors.New("wechatpay: callback must be a POST without a query")
	}
	if len(cb.Body) > callbackMax {
		return errors.New("wechatpay: callback body is too large")
	}
	media, _, err := mime.ParseMediaType(cb.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return errors.New("wechatpay: callback must be JSON")
	}
	return nil
}

func (c *wechatChannel) callbackTimestamp(h http.Header) error {
	timestamp, err := wechatHeader(h, "Wechatpay-Timestamp")
	if err != nil {
		return err
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || seconds <= 0 {
		return errors.New("wechatpay: callback has an invalid timestamp")
	}
	now := c.now()
	at := time.Unix(seconds, 0)
	if now.Sub(at) > wechatpayCallbackWindow || at.Sub(now) > wechatpayCallbackWindow {
		return errors.New("wechatpay: callback timestamp is outside the window")
	}
	return nil
}

func (r wechatResource) check(resourceType string) error {
	if resourceType != "encrypt-resource" || r.Algorithm != "AEAD_AES_256_GCM" || r.OriginalType != "transaction" {
		return errors.New("wechatpay: unsupported callback resource")
	}
	return nil
}

func (c *wechatChannel) callbackNotice(plaintext []byte) (Notice, error) {
	var transaction struct {
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
	if err := jsonx.Unmarshal(plaintext, &transaction); err != nil {
		return Notice{}, errors.New("wechatpay: invalid callback transaction")
	}
	if transaction.MchID != c.settings.MchID || transaction.AppID != c.settings.AppID {
		return Notice{}, errors.New("wechatpay: callback belongs to another merchant")
	}
	if transaction.OutTradeNo == "" {
		return Notice{}, errors.New("wechatpay: callback has no order number")
	}
	if transaction.TradeState == "SUCCESS" && transaction.TransactionID == "" {
		return Notice{}, errors.New("wechatpay: successful callback has no transaction number")
	}
	amount := wechatMinor(transaction.Amount.Total)
	if transaction.Amount.Currency != "" && transaction.Amount.Currency != "CNY" {
		amount = -1
	}
	return Notice{
		Relevant: true,
		Success:  transaction.TradeState == "SUCCESS",
		Order:    transaction.OutTradeNo,
		TradeNo:  transaction.TransactionID,
		Paid:     Money{Currency: c.currency, Minor: amount},
		Raw:      "wechatpay notification",
	}, nil
}

func (c *wechatChannel) decrypt(resource wechatResource) ([]byte, error) {
	if len(c.apiV3) != 32 {
		return nil, errors.New("wechatpay: APIv3 key is not 32 bytes")
	}
	if len(resource.Nonce) != 12 {
		return nil, errors.New("wechatpay: callback nonce is not 12 bytes")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(resource.Ciphertext)
	if err != nil || len(ciphertext) < 16 {
		return nil, errors.New("wechatpay: callback ciphertext is invalid")
	}
	block, err := aes.NewCipher(c.apiV3)
	if err != nil {
		return nil, errors.New("wechatpay: cannot initialize callback cipher")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("wechatpay: cannot initialize callback GCM")
	}
	plaintext, err := gcm.Open(nil, []byte(resource.Nonce), ciphertext, []byte(resource.AssociatedData))
	if err != nil {
		return nil, errors.New("wechatpay: callback decryption failed")
	}
	return plaintext, nil
}
