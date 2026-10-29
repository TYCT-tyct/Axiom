package payments

import (
	"context"
	"errors"
	"strconv"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

// Only save probes the gateway: opening a stored channel must not add network I/O to callbacks.
func (c *easyPayChannel) validateMerchant(ctx context.Context, in MerchantInput) error {
	if c.settings.Preset != "epusdt" {
		return nil
	}
	for i, method := range in.Methods {
		if method.Upstream != "usdt.tron" {
			return &FieldError{Field: "/methods/" + strconv.Itoa(i) + "/upstream", Reason: "Epusdt requires usdt.tron"}
		}
	}
	v := c.credentials("order")
	// Never create a payable order merely to validate configuration.
	v.Set("out_trade_no", OrderID(uuid.NewV7()).TypeID())
	body, err := c.post(ctx, "/api.php", v, true)
	if err != nil {
		return &FieldError{"/settings", "Epusdt requires an available authenticated order query endpoint"}
	}
	var response struct {
		Code jsonx.Raw `json:"code"`
		Msg  string    `json:"msg"`
	}
	if err = jsonx.Unmarshal(body, &response); err != nil {
		return &FieldError{"/settings", "Epusdt query did not return protocol JSON"}
	}
	code, err := easyPayScalar(response.Code)
	if err != nil || (code != "0" && code != "-1") || !easyPayOrderMissing(response.Msg) {
		return &FieldError{"/settings", "Epusdt query must explicitly report the probe order missing"}
	}
	return nil
}

func (s easyPaySettings) checkPreset(currency platform.Currency) error {
	switch s.Preset {
	case "":
		return nil
	case "epusdt":
	default:
		return errors.New("easypay: unknown preset")
	}
	pid, err := strconv.ParseInt(s.PID, 10, 64)
	if err != nil || pid <= 0 || strconv.FormatInt(pid, 10) != s.PID {
		return errors.New("easypay: Epusdt requires a canonical positive numeric pid")
	}
	switch {
	case s.Mode != "redirect", s.Close, s.Refund, len(s.CIDs) != 0:
		return errors.New("easypay: Epusdt requires redirect checkout and offline refunds without cids or close")
	case currency.String() != "CNY" && currency.String() != "USD":
		return errors.New("easypay: Epusdt requires CNY or USD fiat amounts")
	default:
		return nil
	}
}
