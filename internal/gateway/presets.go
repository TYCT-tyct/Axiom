package gateway

import (
	"errors"

	"axiom/internal/accounts"
	"axiom/internal/protocol"
)

func (h *Handler) expandPreset(c *Call, body []byte) ([]byte, protocol.Request, error) {
	req, err := protocol.Inspect(c.Format, body)
	if err != nil {
		return nil, req, err
	}
	slug, model, err := protocol.PresetReference(&req)
	if err != nil || slug == "" {
		return body, req, err
	}
	config, err := h.Accounts.PresetConfig(c.site.ID, c.caller.AccountID(), slug)
	if errors.Is(err, accounts.ErrNotFound) {
		return nil, req, &Refusal{Status: 404, Code: "not_found", Message: "No such preset."}
	}
	if err != nil {
		return nil, req, err
	}
	// Maps and JSON merge buffers exist only for requests using a preset; reserve their bounded working set first.
	cost := protocol.PresetCost(body, []byte(config))
	if !h.Memory.Charge(cost) {
		return nil, req, ErrOverloaded
	}
	merged, err := protocol.MergePreset(c.Format, body, []byte(config), model)
	if err == nil && int64(len(merged)) > h.Limits.cfg.MaxBody {
		err = errTooLarge
	}
	if err == nil {
		req, err = protocol.Inspect(c.Format, merged)
	}
	held := int64(cap(merged))
	if err != nil {
		held = 0
	}
	h.Memory.Refund(cost - held)
	if err != nil {
		return nil, req, err
	}
	c.hold(func() { h.Memory.Refund(held) })
	return merged, req, nil
}
