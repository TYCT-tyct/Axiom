package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/observability"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
)

// errNoGeneration is a call number the key's account has no record of: it never existed, belongs to another account,
// or its record is not written yet. The three look alike on purpose.
var errNoGeneration = &Refusal{Status: http.StatusNotFound, Code: "generation_not_found"}

// errNoContent is a call whose content is not retained. Axiom stores no prompt or reply text (observability's call log
// has none), so the endpoint exists for clients that ask and always answers that there is nothing to show.
var errNoContent = &Refusal{Status: http.StatusNotFound, Code: "content_not_available"}

// credits turns billing's smallest unit (a billionth of a credit) into the credits an endpoint shows. A float64 holds
// every balance a person has to the digits a client prints.
func credits(nano int64) float64 { return float64(nano) / 1e9 }

// totalCredits is the credits ever granted: the balance and the spend each shown, then added. Added in billionths the
// two could pass MaxInt64 (a top-up after a spend) and wrap negative.
func totalCredits(f billing.Funds) float64 { return credits(f.Balance) + credits(f.Spent) }

// auxRoutes adds the endpoints that are not calls: the token count and the read-only account endpoints
// (docs/domains/gateway.md「给其他中转站对接的接口」). Every one needs a usable key, and the reads happen in the same
// transaction that checks it, so the key's account is the only data they can see.
func (h *Handler) auxRoutes(prefix string) {
	h.mux.HandleFunc("POST "+prefix+"/messages/count_tokens", h.countTokens)
	h.mux.HandleFunc("GET "+prefix+"/key", h.reading(h.keyInfo))
	h.mux.HandleFunc("GET "+prefix+"/credits", h.reading(h.accountCredits))
	h.mux.HandleFunc("GET "+prefix+"/generation", h.reading(h.generation))
	h.mux.HandleFunc("GET "+prefix+"/generation/content", h.reading(h.generationContent))
	h.mux.HandleFunc("GET "+prefix+"/activity", h.reading(h.activity))
	h.mux.HandleFunc("GET "+prefix+"/dashboard/billing/subscription", h.reading(h.subscription))
	h.mux.HandleFunc("GET "+prefix+"/dashboard/billing/usage", h.reading(h.legacyUsage))
}

// reader produces the body of a read-only endpoint from the key's admission, inside its transaction.
type reader func(ctx context.Context, tx pg.Tx, a accounts.Admission, q url.Values) (any, error)

// reading serves a GET that shows the key's own account. Errors take Chat's shell, which OpenRouter's clients read.
func (h *Handler) reading(serve reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		release, ok := h.ipSlot(w, r, protocol.Chat)
		if !ok {
			return
		}
		defer release()
		_, caller, err := h.authenticate(r)
		var out any
		if err == nil {
			err = h.authorise(r, caller, func(ctx context.Context, tx pg.Tx, a accounts.Admission) (err error) {
				out, err = serve(ctx, tx, a, r.URL.Query())
				return err
			})
		}
		if err != nil {
			h.fail(w, r, protocol.Chat, err)
			return
		}
		writeJSON(w, out)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	body, err := jsonx.MarshalCanonical(v)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	writeBody(w, body)
}

// writeBody answers 200 with a JSON body that is already encoded.
func writeBody(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // money and usage: never from a shared cache
	w.Write(body)                               //nolint:errcheck,gosec // the client may be gone
}

// keyInfo is OpenRouter's GET /api/v1/key for the key that asks. Usage counts what was charged (spent); the limit's
// remaining also takes off what is reserved, because that is what admission refuses on. A key without a limit shows
// null for the limit, what is left of it and its reset. The fields OpenRouter has for BYOK are left out: Axiom has no
// BYOK.
func (h *Handler) keyInfo(ctx context.Context, tx pg.Tx, a accounts.Admission, _ url.Values) (any, error) {
	key := a.Caller().KeyID()
	info, err := h.Accounts.GetKeyInfo(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	limit, reset, limited := a.SpendLimit()
	use, err := billing.ReadKeyUsage(ctx, tx, uuid.UUID(key), billing.Reset(reset))
	if err != nil {
		return nil, err
	}
	data := map[string]any{
		"label": info.Label, "name": info.Name, "usage": credits(use.Total), "usage_daily": credits(use.Day),
		"usage_weekly": credits(use.Week), "usage_monthly": credits(use.Month), "limit": nil, "limit_remaining": nil,
		"limit_reset": nil, "expires_at": nil, "is_free_tier": false, "is_management_key": false,
		"budgets": []budgetView{}, "effective_budget": nil, "rate_limit": h.keyRateLimit(a),
	}
	if limited {
		data["limit"], data["limit_remaining"] = credits(limit), credits(max(0, limit-use.Counted))
		if reset != "" {
			data["limit_reset"] = reset
		}
	}
	if info.ExpiresAt != nil {
		data["expires_at"] = info.ExpiresAt.UTC().Format(time.RFC3339)
	}
	budgets, effective, err := keyBudgets(ctx, tx, a, use)
	if err != nil {
		return nil, err
	}
	data["budgets"], data["effective_budget"] = budgets, effective
	return map[string]any{"data": data}, nil
}

// funds is the key's account's balance and lifetime spend. The credits ever granted are the balance plus what was
// charged, as long as charges are all that takes from the balance; the ledger itself is not summed, since that would
// read every call of a busy account on each poll (internal/gateway/README.md「辅助接口」).
func (h *Handler) funds(ctx context.Context, tx pg.Tx, a accounts.Admission) (billing.Funds, error) {
	account := a.Caller().AccountID()
	workspaces, err := h.Accounts.AccountWorkspaces(ctx, tx, account)
	if err != nil {
		return billing.Funds{}, err
	}
	return billing.ReadFunds(ctx, tx, uuid.UUID(account), workspaces)
}

// accountCredits is OpenRouter's GET /api/v1/credits.
func (h *Handler) accountCredits(ctx context.Context, tx pg.Tx, a accounts.Admission, _ url.Values) (any, error) {
	f, err := h.funds(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	return map[string]any{"data": map[string]any{"total_credits": totalCredits(f), "total_usage": credits(f.Spent)}}, nil
}

// subscription is OpenAI's legacy GET /dashboard/billing/subscription, which new-api reads a channel's balance from:
// the hard limit less the usage is the balance.
func (h *Handler) subscription(ctx context.Context, tx pg.Tx, a accounts.Admission, _ url.Values) (any, error) {
	f, err := h.funds(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	limit := totalCredits(f)
	return map[string]any{
		"object": "billing_subscription", "has_payment_method": false, "soft_limit_usd": limit,
		"hard_limit_usd": limit, "system_hard_limit_usd": limit, "access_until": 0,
	}, nil
}

// legacyUsage is OpenAI's legacy GET /dashboard/billing/usage: total_usage in hundredths of a unit. It is the account's
// lifetime usage whatever dates are asked, which is what the caller subtracts from the hard limit to get the balance.
func (h *Handler) legacyUsage(ctx context.Context, tx pg.Tx, a accounts.Admission, _ url.Values) (any, error) {
	f, err := h.funds(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	return map[string]any{"object": "list", "total_usage": 100 * credits(f.Spent)}, nil
}

// generation is OpenRouter's GET /api/v1/generation?id=: what one call of the key's account used and was charged. It
// shows the price the user paid and nothing of our cost, endpoint or route (observability.OwnCall has none).
func (h *Handler) generation(ctx context.Context, tx pg.Tx, _ accounts.Admission, q url.Values) (any, error) {
	id, err := uuid.Parse(q.Get("id"))
	if err != nil {
		return nil, invalid("The id parameter must be the number of a call, as in X-Generation-Id.")
	}
	call, err := observability.GetOwn(ctx, tx, id)
	if errors.Is(err, pg.ErrNoRows) {
		return nil, errNoGeneration
	}
	if err != nil {
		return nil, err
	}
	charges, err := h.charges.ReadCharges(ctx, tx, []uuid.UUID{id})
	if err != nil {
		return nil, err
	}
	sum := func(items ...string) (n int64) {
		for _, item := range items {
			n += call.Usage[item]
		}
		return n
	}
	data := map[string]any{
		"id": call.ID.String(), "model": call.Model, "created_at": call.At.UTC().Format(time.RFC3339),
		"total_cost": credits(call.PriceNano), "currency": call.Currency, "is_free": call.Free,
		"service_tier": call.ServiceTier, "app": call.ClientApp,
		"latency": call.FirstToken.Milliseconds(), "generation_time": call.Latency.Milliseconds(),
		"finish_reason": call.FinishReason, "error": call.ErrorKind, "usage": call.Usage,
		"tokens_prompt":     sum("input", "cache_read", "cache_write", "cache_write_1h", "audio_in", "image_in"),
		"tokens_completion": sum("output", "reasoning", "audio_out", "image_out"),
		"tokens_reasoning":  sum("reasoning"), "tokens_cached": sum("cache_read"),
	}
	if call.PriceVersion != "" {
		data["price_version"] = call.PriceVersion
	}
	if call.PhysicalInput != nil {
		data["native_tokens_prompt"] = *call.PhysicalInput
	}
	data["native_tokens_completion"] = sum("output", "reasoning", "audio_out", "image_out")
	data["native_tokens_reasoning"] = sum("reasoning")
	data["native_tokens_cached"] = sum("cache_read")
	data["native_tokens_completion_images"] = sum("image_out")
	data["streamed"] = call.Streamed
	data["cancelled"] = call.FinishReason == "cancelled"
	if call.SessionID != "" {
		data["session_id"] = call.SessionID
	}
	if d, ok := charges[id]; ok {
		data["charge_details"] = d
		// A correction can change the charge after the call log was written; the reservation's itemization is the
		// authority, so total_cost follows it rather than the log's snapshot.
		if e, err := platform.ParseExact(d.Charged); err == nil {
			if f, _ := e.Rat().Float64(); f >= 0 {
				data["total_cost"] = f
			}
		}
		if d.CacheDiscount != "" {
			if e, err := platform.ParseExact(d.CacheDiscount); err == nil {
				if f, _ := e.Rat().Float64(); f > 0 {
					data["cache_discount"] = f
				}
			}
		}
	}
	return map[string]any{"data": data}, nil
}

// generationContent is OpenRouter's GET /api/v1/generation/content: the prompt and reply text of a call, while the
// retention rules keep it. Axiom keeps no content, so an existing call of the account is still 404 with a code that
// says why, and another account's call is the same 404 as generation's.
func (h *Handler) generationContent(ctx context.Context, tx pg.Tx, _ accounts.Admission, q url.Values) (any, error) {
	id, err := uuid.Parse(q.Get("id"))
	if err != nil {
		return nil, invalid("The id parameter must be the number of a call, as in X-Generation-Id.")
	}
	if _, err := observability.GetOwn(ctx, tx, id); errors.Is(err, pg.ErrNoRows) {
		return nil, errNoGeneration
	} else if err != nil {
		return nil, err
	}
	return nil, errNoContent
}

// activityItem is one row of OpenRouter's GET /api/v1/activity. endpoint_id is the zero UUID and provider_name is
// Axiom because the user must not learn which endpoint or supplier served a call; model_permaslug is the model's
// public name, as the call log keeps no dated slug.
type activityItem struct {
	Date               string  `json:"date"`
	Model              string  `json:"model"`
	ModelPermaslug     string  `json:"model_permaslug"`
	EndpointID         string  `json:"endpoint_id"`
	ProviderName       string  `json:"provider_name"`
	Usage              float64 `json:"usage"`
	BYOKUsageInference float64 `json:"byok_usage_inference"`
	Requests           int64   `json:"requests"`
	PromptTokens       int64   `json:"prompt_tokens"`
	CompletionTokens   int64   `json:"completion_tokens"`
	ReasoningTokens    int64   `json:"reasoning_tokens"`
	CachedTokens       int64   `json:"cached_tokens"`
	WorkspaceID        string  `json:"workspace_id,omitempty"`
}

// activity is OpenRouter's GET /api/v1/activity: the account's use and sale charge per UTC day and model over the
// last 30 completed days, or one day of them. api_key_hash names a key by its UUID, which is the identifier Axiom's
// keys API returns (there is no exposed secret hash); user_id needs organization accounts, which Axiom does not have.
func (h *Handler) activity(ctx context.Context, tx pg.Tx, a accounts.Admission, q url.Values) (any, error) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	since, until := today.AddDate(0, 0, -30), today
	if date := q.Get("date"); date != "" {
		d, err := time.Parse("2006-01-02", date)
		if err != nil || !d.Before(today) || d.Before(since) {
			return nil, invalid("The date parameter must be a UTC day within the last 30 completed days, as YYYY-MM-DD.")
		}
		since, until = d, d.AddDate(0, 0, 1)
	}
	if q.Get("user_id") != "" {
		return nil, invalid("The user_id parameter needs organization accounts, which Axiom does not have.")
	}
	if group := q.Get("group_by"); group != "" && group != "workspace" {
		return nil, invalid("The group_by parameter must be workspace.")
	}
	var key, workspace *uuid.UUID
	if v := q.Get("api_key_hash"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, invalid("The api_key_hash parameter must be the UUID of a key of this account.")
		}
		key = &id
	}
	if v := q.Get("workspace_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, invalid("The workspace_id parameter must be a UUID.")
		}
		workspace = &id
	}
	rows, err := observability.ActivityOwn(ctx, tx, observability.ActivityQuery{
		Account: uuid.UUID(a.Caller().AccountID()), Since: since, Until: until, Key: key, Workspace: workspace,
		GroupByWorkspace: q.Get("group_by") == "workspace",
	})
	if err != nil {
		return nil, err
	}
	// The call log's price is the snapshot written at settlement, and corrections move the reservation and ledger
	// afterwards. Read the current charge for these calls and let it replace the snapshot when present; a missing row
	// (old call, not settled, or out of scope) keeps the recorded amount.
	var ids []uuid.UUID
	for _, r := range rows {
		ids = append(ids, r.RequestIDs...)
	}
	current, err := billing.ChargedCalls(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	items := make([]activityItem, len(rows))
	for i, r := range rows {
		// A group may hold several calls with different corrections. Sum each call's current charge, falling back to
		// that call's own snapshot only when its reservation is absent (an old or non-settled row).
		var usage int64
		for j, id := range r.RequestIDs {
			if charged, ok := current[id]; ok {
				usage += charged
			} else if j < len(r.RequestPrices) {
				usage += r.RequestPrices[j]
			}
		}
		items[i] = activityItem{
			Date: r.Date, Model: r.Model, ModelPermaslug: r.Model, EndpointID: uuid.Nil().String(),
			ProviderName: "Axiom", Usage: credits(usage), Requests: r.Requests,
			PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens, ReasoningTokens: r.ReasoningTokens,
			CachedTokens: r.CachedTokens, WorkspaceID: r.Workspace,
		}
	}
	return map[string]any{"data": items}, nil
}
