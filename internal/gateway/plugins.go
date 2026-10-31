package gateway

import (
	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

func (h *Handler) compress(c *Call, body []byte, req protocol.Request, snap *catalog.Snapshot, models []catalog.ModelID) ([]byte, protocol.Request, error) {
	if len(models) == 0 {
		// openrouter/auto has no model yet: the route picks one, so there is no context window to compress against.
		return body, req, nil
	}
	model, _ := snap.Model(models[0])
	serving, ok := model.Serving()
	if !ok || !req.Compresses(serving.Context) {
		return body, req, nil
	}
	output := serving.MaxOutput
	if req.MaxOutput > 0 && req.MaxOutput < output {
		output = req.MaxOutput
	}
	inputLimit := serving.Context - output
	// Automatic compression must not treat a reserved output limit as consumed input space.
	if req.Compression == protocol.CompressionDefault {
		inputLimit = serving.Context
	}
	if int64(len(body))+req.SearchBytes <= inputLimit && req.MessageCount <= protocol.CompressionMessageLimit {
		return body, req, nil
	}
	compressed, inspected, err := h.compressBody(c, body, req, serving.Context, output)
	if err != nil && req.Compression == protocol.CompressionDefault {
		return body, req, nil
	}
	return compressed, inspected, err
}

func (h *Handler) compressBody(c *Call, body []byte, req protocol.Request, context, output int64) ([]byte, protocol.Request, error) {
	cost := protocol.CompressionCost(body)
	if !h.Memory.Charge(cost) {
		return nil, req, ErrOverloaded
	}
	compressed, err := protocol.CompressContext(body, context, output)
	held := int64(cap(compressed))
	if err != nil {
		held = 0
	}
	h.Memory.Refund(cost - held)
	if err != nil {
		return nil, req, err
	}
	c.hold(func() { h.Memory.Refund(held) })
	req, err = protocol.Inspect(req.Format, compressed)
	return compressed, req, err
}
