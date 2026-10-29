package gateway

import (
	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

func (h *Handler) compress(c *Call, body []byte, req protocol.Request, snap *catalog.Snapshot, models []catalog.ModelID) ([]byte, protocol.Request, error) {
	model, _ := snap.Model(models[0])
	serving, ok := model.Serving()
	if !ok || !req.Compresses(serving.Context) {
		return body, req, nil
	}
	output := serving.MaxOutput
	if req.MaxOutput > 0 && (output == 0 || req.MaxOutput < output) {
		output = req.MaxOutput
	}
	if int64(len(body)) <= serving.Context-output && req.MessageCount <= protocol.CompressionMessageLimit {
		return body, req, nil
	}
	cost := protocol.CompressionCost(body)
	if !h.Memory.Charge(cost) {
		return nil, req, ErrOverloaded
	}
	compressed, err := protocol.CompressContext(body, serving.Context, output)
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
