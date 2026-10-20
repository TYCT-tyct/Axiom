package gateway

import (
	"context"
	"net/http"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
)

// The count of POST /messages/count_tokens, which never reaches an upstream (internal/gateway/README.md「辅助接口」).
// The text is the request's bytes less the image and file data, a third of a token each: an empirical ratio that usually
// overcounts (English runs about four bytes a token) but bounds nothing. An image counts as the most one costs, and a
// file, whose pages are not known without decoding it, as a flat few pages, so a file of more pages is undercounted.
const (
	bytesPerToken  = 3
	imageTokens    = 1600
	documentTokens = 3000
)

// countInput is the estimate of a request's input tokens. It rounds up and errs on the high side for English text and
// for images; a file is a flat guess.
func countInput(r protocol.Request) int64 {
	return (r.TextBytes+bytesPerToken-1)/bytesPerToken + r.Images*imageTokens + r.Documents*documentTokens
}

// countRateKey is the key's count under the same counter as its calls but its own name, so the two spend separate
// allowances (docs/domains/gateway.md「给其他中转站对接的接口」).
func countRateKey(c accounts.Caller) string { return "count:" + keyKey(c) }

// countTokens answers Anthropic's count_tokens: {"input_tokens": n}. It takes what a call takes before admission (the
// address's limit, the key, the account's slot, the body's memory, the key's rate), then asks the database only whether
// the key may be used and may use the model. Nothing is reserved, charged or recorded.
func (h *Handler) countTokens(w http.ResponseWriter, r *http.Request) {
	release, ok := h.ipSlot(w, r, protocol.Messages)
	if !ok {
		return
	}
	defer release()
	n, err := h.count(w, r)
	if err != nil {
		h.fail(w, r, protocol.Messages, err)
		return
	}
	writeJSON(w, map[string]any{"input_tokens": n})
}

func (h *Handler) count(w http.ResponseWriter, r *http.Request) (int64, error) {
	if r.Header.Get("Upgrade") != "" {
		return 0, errUpgrade
	}
	_, caller, err := h.authenticate(r)
	if err != nil {
		return 0, err
	}
	c := &Call{caller: caller}
	defer c.Done()
	slot, err := h.Limits.InFlight.Acquire(accountKey(caller), 0)
	if err != nil {
		return 0, err
	}
	c.hold(slot)
	body, err := h.readBody(w, r, c)
	if err != nil {
		return 0, err
	}
	req, err := protocol.Inspect(protocol.Messages, body)
	if err != nil {
		return 0, err
	}
	if h.Limits.Rate.Count(countRateKey(caller)) > h.Limits.cfg.CountRate {
		return 0, errCountRate
	}
	snap, err := h.currentCatalog()
	if err != nil {
		return 0, err
	}
	_, names, err := readRouting(&req) // a count refuses what the call would
	if err != nil {
		return 0, err
	}
	model, err := snap.ResolvePublic(names.primary)
	if err != nil {
		return 0, err
	}
	err = h.authorise(r, caller, func(_ context.Context, _ pg.Tx, a accounts.Admission) error {
		if names := a.Models(); names != nil { // a key that may not use the model is told it does not exist
			if _, ok := allowedModels(snap, names)[model.Model]; !ok {
				return errNoModel
			}
		}
		return nil
	})
	return countInput(req), err
}
