package gateway

import (
	"context"
	"io"
	"net/http"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// The estimate POST /messages/count_tokens answers when no upstream answers it (internal/gateway/README.md「辅助接口」).
// The text is the request's bytes less the image and file data, a third of a token each: an empirical ratio that usually
// overcounts (English runs about four bytes a token) but bounds nothing. An image counts as the most one costs, and a
// file, whose pages are not known without decoding it, as a flat few pages, so a file of more pages is undercounted.
const (
	bytesPerToken  = 3
	imageTokens    = 1600
	documentTokens = 3000
)

// countWait is how long a count waits for its upstream before it estimates instead: a coding tool counts before its
// turns, which a slow relay should not hold up. maxCountReply is the most of the answer read; {"input_tokens": n} is
// a few dozen bytes.
const (
	countWait     = 3 * time.Second
	maxCountReply = 4 << 10
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
// the key may be used and may use the model, and then an upstream (countUpstream). Nothing is reserved, charged or
// recorded.
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
	return h.countAs(w, r, protocol.Messages, nil)
}

// countAs is count for a request of format, converted from Gemini's when gemini is set.
func (h *Handler) countAs(w http.ResponseWriter, r *http.Request, format protocol.Format, gemini *geminiInbound) (int64, error) {
	if r.Header.Get("Upgrade") != "" {
		return 0, errUpgrade
	}
	site, caller, err := h.authenticate(r)
	if err != nil {
		return 0, err
	}
	c := &Call{caller: caller, site: site, Format: format, gemini: gemini}
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
	if gemini != nil {
		if body, err = h.geminiBody(c, body); err != nil {
			return 0, err
		}
	}
	body, req, err := h.expandPreset(c, body)
	if err != nil {
		return 0, err
	}
	c.rate = h.precheck(r.Context(), site.ID, countDimension, countRateKey(caller), keyHex(caller), c, false)
	if err := h.checkRate(c.rate, h.Limits.cfg.CountRate, errCountRate, c.rateReset); err != nil {
		return 0, err
	}
	snap, err := h.currentCatalog()
	if err != nil {
		return 0, err
	}
	over, names, err := readRouting(&req, body) // a count refuses what the call would
	if err != nil {
		return 0, err
	}
	if err := countable(names, &req); err != nil {
		return 0, err
	}
	model, err := snap.ResolvePublic(names.primary)
	if err != nil {
		return 0, err
	}
	var admission routing.Admission
	err = h.authorise(r, caller, func(_ context.Context, _ pg.Tx, a accounts.Admission) error {
		admission = routingAdmission(a, caller, site, snap)
		return h.countAdmission(a, c, snap, model.Model)
	})
	if err != nil {
		return 0, err
	}
	if format != protocol.Messages { // only Anthropic's API counts tokens, and the body is not its shape
		return countInput(req), nil
	}
	return h.countOf(r, routing.Input{
		Admission: admission, Req: &req, Models: []catalog.ModelID{model.Model}, Override: over, NativeOnly: true, NoSegments: true,
	}, body), nil
}

// countOf is the upstream's count of the request, or the estimate when none answers.
func (h *Handler) countOf(r *http.Request, in routing.Input, body []byte) int64 {
	if n, ok := h.countUpstream(r, in, body); ok {
		return n
	}
	return countInput(*in.Req)
}

// countUpstream asks the first endpoint of the request's route that speaks Messages itself — the route a call would
// take, so the body goes nowhere the call could not — once the admission's transaction and pool permit are given back.
// ok is false when none answered with a count, and the caller estimates. The credential's slot is held only while the
// count is asked, and a failure of the count rests nothing: a relay without the route answers 404, which says nothing
// of the credential or the endpoint (suppliers.Call.Count).
func (h *Handler) countUpstream(r *http.Request, in routing.Input, body []byte) (int64, bool) {
	plan, _, err := h.Models.plan(h.Router, in)
	if err != nil {
		return 0, false
	}
	for _, e := range plan.Entries {
		if h.Router.Blocked(e) {
			continue
		}
		conn, err := h.Upstreams.Acquire(suppliers.EndpointID(e.Endpoint))
		if err != nil {
			continue
		}
		n, ok := h.askCount(r, conn, e.Target.Model, body)
		conn.Done()
		h.Limits.Queue.Wake() // a freed slot is what a queued call waits for (session.done)
		return n, ok
	}
	return 0, false
}

// askCount sends the count to one credential and reads {"input_tokens": n} of a 2xx answer, strictly.
func (h *Handler) askCount(r *http.Request, conn Conn, model string, body []byte) (int64, bool) {
	out, held, err := protocol.CountBody(h.Memory, body, model)
	if err != nil {
		return 0, false
	}
	defer h.Memory.Refund(held)
	ctx, cancel := context.WithTimeout(r.Context(), countWait)
	defer cancel()
	call := suppliers.Call{Format: protocol.Messages, Count: true, Body: out, Header: protocolHeader(protocol.Messages, r.Header, nil)}
	resp, f := conn.Send(ctx, call)
	if f != nil {
		return 0, false
	}
	reply, err := io.ReadAll(io.LimitReader(resp.Body, maxCountReply+1))
	if closeErr := resp.Body.Close(); err == nil {
		err = closeErr
	}
	var count struct {
		InputTokens *int64 `json:"input_tokens"`
	}
	if err != nil || len(reply) > maxCountReply || jsonx.Unmarshal(reply, &count) != nil || count.InputTokens == nil || *count.InputTokens < 0 {
		return 0, false
	}
	return *count.InputTokens, true
}

// countable refuses what count_tokens cannot answer: a router that expands to a model, and a request that searches,
// whose results are injected after its body was counted.
func countable(names variantNames, req *protocol.Request) error {
	if name := names.routerName(); name != "" {
		return invalid(name + " has no model to count tokens for: count with the concrete model.")
	}
	if names.online || req.Web != nil {
		return invalid("count_tokens cannot count a request that runs a web search: the results are injected after counting.")
	}
	return nil
}

// countAdmission checks the current business policy and model permission in the key's admission transaction.
func (h *Handler) countAdmission(a accounts.Admission, c *Call, snap *catalog.Snapshot, model catalog.ModelID) error {
	if err := h.judgeRate(a, c, countDimension); err != nil {
		return err
	}
	if names := a.Models(); names != nil { // a key that may not use the model is told it does not exist
		if _, ok := allowedModels(snap, names)[model]; !ok {
			return errNoModel
		}
	}
	return nil
}
