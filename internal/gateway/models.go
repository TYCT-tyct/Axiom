package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
)

// created is what every model says of its creation: the catalog keeps no listing time, and the clients that need the
// field only need it to be there.
var created = time.Unix(0, 0).UTC()

// visibility is what the model endpoints show: the catalog snapshot and the models this key may call (nil is every
// model). Capacity is not part of it: an endpoint that is full, broken or out of budget must not make its model
// vanish from the list, because a call is what waits for capacity and the list is not.
type visibility struct {
	snap    *catalog.Snapshot
	allowed map[catalog.ModelID]struct{} // nil: every model
}

// listModels answers GET /v1/models in the shape of OpenAI's and of Anthropic's at once, so every client reads its own
// members. A key sees the models it may call, by the permission a call is judged with, and a model is listed by its
// catalog name and, when that name resolves to the same model, by its maker's own: Claude Code takes only the entries
// whose ID holds "claude" or "anthropic".
func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	format := formatFor(r)
	release, err := h.Limits.IPCalls.Acquire(ipKey(clientIP(r)), h.Limits.cfg.IPInFlight)
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	defer release()
	v, err := h.visible(r)
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	data := v.models()
	list := map[string]any{"object": "list", "data": data, "has_more": false}
	if len(data) > 0 {
		list["first_id"], list["last_id"] = data[0]["id"], data[len(data)-1]["id"]
	}
	writeJSON(w, list) // no-store: the list is this key's, not a shared cache's
}

// retrieveModel answers GET /v1/models/{model}: the entry the list shows for the name, resolved as a call resolves it,
// so an alias gives the model behind it. A name the catalog does not know, or one this key may not use, is the same
// 404 a call gets for it.
func (h *Handler) retrieveModel(w http.ResponseWriter, r *http.Request) {
	format := formatFor(r)
	release, err := h.Limits.IPCalls.Acquire(ipKey(clientIP(r)), h.Limits.cfg.IPInFlight)
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	defer release()
	var entry map[string]any
	v, err := h.visible(r)
	if err == nil {
		entry, err = v.model(r.PathValue("model"))
	}
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	writeJSON(w, entry)
}

// formatFor is the shell a request that is not a call reads its errors in: Messages' when the client says it speaks
// Anthropic, Chat's otherwise. The bodies of the model endpoints are the same in both.
func formatFor(r *http.Request) protocol.Format {
	if r.Header.Get("Anthropic-Version") != "" {
		return protocol.Messages
	}
	return protocol.Chat
}

// visible reads the catalog and the key's models, judging the key as an admission does: the database says whether it
// may be used now, and allowedModels, the function the call path filters its route with (routingAdmission), says which
// models its names stand for. The two answers cannot drift apart.
func (h *Handler) visible(r *http.Request) (visibility, error) {
	_, caller, err := h.authenticate(r)
	if err != nil {
		return visibility{}, err
	}
	snap, err := h.currentCatalog()
	if err != nil {
		return visibility{}, err
	}
	var allowed map[catalog.ModelID]struct{}
	err = h.authorise(r, caller, func(_ context.Context, _ pg.Tx, a accounts.Admission) error {
		if names := a.Models(); names != nil { // nil allows every model, an empty list none
			allowed = allowedModels(snap, names)
		}
		return nil
	})
	if err != nil {
		return visibility{}, err
	}
	return visibility{snap: snap, allowed: allowed}, nil
}

// allows says whether the key may call the model.
func (v visibility) allows(m catalog.ModelID) bool {
	_, ok := v.allowed[m]
	return v.allowed == nil || ok
}

// authorise asks the database whether the key may be used now, as an admission does but with nothing to reserve: the
// key may have been disabled, expired or limited to other addresses since it was recognised, and recognising it says
// none of that. It takes a connection of the admission class and no account gate, as it moves no money. fn reads in
// the same transaction, whose scope is the key's account, under the admission's budget.
func (h *Handler) authorise(r *http.Request, caller accounts.Caller, fn func(context.Context, pg.Tx, accounts.Admission) error) error {
	ctx, cancel := context.WithTimeout(r.Context(), h.Limits.cfg.AdmissionBudget)
	defer cancel()
	release, err := h.Limits.Pool.Acquire(ctx, Admission)
	if err != nil {
		return err
	}
	defer release()
	return h.admitDB(ctx, caller, accounts.AdmitRequest{Domain: r.Host, ClientIP: clientIP(r)}, func(tx pg.Tx, a accounts.Admission) error { return fn(ctx, tx, a) })
}

// models is the list: each visible model by the names that resolve to it.
func (v visibility) models() []map[string]any {
	data := []map[string]any{}
	for m := range v.snap.Models() {
		if !v.allows(m.ID) {
			continue
		}
		author, name, _ := strings.Cut(m.Slug, "/")
		for _, id := range []string{m.Slug, name} {
			if r, err := v.snap.ResolvePublic(id); err != nil || r.Model != m.ID {
				continue
			}
			data = append(data, modelEntry(id, author, name))
		}
	}
	return data
}

// model is the entry for one name, resolved as a call resolves it, or the error a call gets: a name the catalog does
// not know or has retired is model_not_found, and so is a model this key may not use, which is never 403.
func (v visibility) model(name string) (map[string]any, error) {
	resolved, err := v.snap.ResolvePublic(name)
	if err != nil {
		return nil, err
	}
	if !v.allows(resolved.Model) {
		return nil, errNoModel
	}
	m, ok := v.snap.Model(resolved.Model)
	if !ok {
		return nil, errNoModel
	}
	author, display, _ := strings.Cut(m.Slug, "/")
	return modelEntry(name, author, display), nil
}

// modelEntry is one model as OpenAI's and as Anthropic's clients read it: the members both need, under the name that
// was asked for.
func modelEntry(id, author, display string) map[string]any {
	return map[string]any{
		"id": id, "object": "model", "type": "model", "display_name": display, "owned_by": author,
		"created": created.Unix(), "created_at": created.Format(time.RFC3339),
	}
}
