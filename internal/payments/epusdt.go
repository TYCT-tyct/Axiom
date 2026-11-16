package payments

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

type easyPayPreset struct {
	name, upstream, queryMethod string
	zeroMissing                 bool
}

var easyPayPresets = map[string]easyPayPreset{
	"epusdt": {"Epusdt", "usdt.tron", http.MethodPost, true},
	"ezpay":  {"EzPay", "usdt_trc20", http.MethodGet, false},
}

func (c *easyPayChannel) validateMerchant(in MerchantInput) error {
	preset, ok := easyPayPresets[c.settings.Preset]
	if !ok {
		return nil
	}
	for i, method := range in.Methods {
		if method.Upstream != preset.upstream {
			return &FieldError{Field: "/methods/" + strconv.Itoa(i) + "/upstream", Reason: preset.name + " requires " + preset.upstream}
		}
	}
	return nil
}

// Only save probes the gateway: opening a stored channel must not add network I/O to callbacks.
func (c *easyPayChannel) probe(ctx context.Context) error {
	preset, ok := easyPayPresets[c.settings.Preset]
	if !ok {
		return nil
	}
	v := c.credentials("order")
	// Never create a payable order merely to validate configuration.
	v.Set("out_trade_no", OrderID(uuid.NewV7()).TypeID())
	body, err := c.request(ctx, preset.queryMethod, "/api.php", v, true)
	if err != nil {
		return &FieldError{"/settings", preset.name + " requires an available authenticated order query endpoint"}
	}
	var response struct {
		Code jsonx.Raw `json:"code"`
		Msg  string    `json:"msg"`
	}
	if err = jsonx.Unmarshal(body, &response); err != nil {
		return &FieldError{"/settings", preset.name + " query did not return protocol JSON"}
	}
	code, err := easyPayScalar(response.Code)
	if err != nil || (code != "-1" && (!preset.zeroMissing || code != "0")) || !easyPayOrderMissing(response.Msg) {
		return &FieldError{"/settings", preset.name + " query must explicitly report the probe order missing"}
	}
	return nil
}

func (s easyPaySettings) checkPreset(currency platform.Currency) error {
	switch s.Preset {
	case "":
		return nil
	case "epusdt":
	case "ezpay":
		return s.checkEzPayPreset(currency)
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
