package gateway

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
)

// modelList is the answer to GET /v1/models, kept for the catalog snapshot it was made from: Claude Code asks at every
// start, and the answer changes only when the catalog does.
type modelList struct{ cached atomic.Pointer[listed] }

type listed struct {
	snap *catalog.Snapshot
	body []byte
}

// created is what every model says of its creation: the catalog keeps no listing time, and the clients that need the
// field only need it to be there.
var created = time.Unix(0, 0).UTC()

// listModels answers the list in the shape of OpenAI's and of Anthropic's at once, so every client reads its own
// members. Each model is listed by its catalog name and, when that name resolves to the same model, by its maker's
// own: Claude Code takes only the entries whose ID holds "claude" or "anthropic".
func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	format := protocol.Chat
	if r.Header.Get("Anthropic-Version") != "" {
		format = protocol.Messages
	}
	release, err := h.Limits.IPCalls.Acquire(ipKey(h.clientIP(r)), h.Limits.cfg.IPInFlight)
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	defer release()
	_, caller, err := h.authenticate(r)
	if err == nil {
		err = h.authorise(r, caller, func(context.Context, pg.Tx, accounts.Admission) error { return nil })
	}
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	snap, err := h.currentCatalog()
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	body, err := h.models.of(snap)
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body) //nolint:errcheck,gosec // the client may be gone
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
	return h.admitDB(ctx, caller, accounts.AdmitRequest{Domain: r.Host, ClientIP: h.clientIP(r)}, func(tx pg.Tx, a accounts.Admission) error { return fn(ctx, tx, a) })
}

func (l *modelList) of(snap *catalog.Snapshot) ([]byte, error) {
	if c := l.cached.Load(); c != nil && c.snap == snap {
		return c.body, nil
	}
	data := []map[string]any{}
	for m := range snap.Models() {
		author, name, _ := strings.Cut(m.Slug, "/")
		for _, id := range []string{m.Slug, name} {
			if r, err := snap.ResolvePublic(id); err != nil || r.Model != m.ID {
				continue
			}
			data = append(data, map[string]any{
				"id": id, "object": "model", "type": "model", "display_name": name, "owned_by": author,
				"created": created.Unix(), "created_at": created.Format(time.RFC3339),
			})
		}
	}
	list := map[string]any{"object": "list", "data": data, "has_more": false}
	if len(data) > 0 {
		list["first_id"], list["last_id"] = data[0]["id"], data[len(data)-1]["id"]
	}
	body, err := jsonx.MarshalCanonical(list)
	if err != nil {
		return nil, err
	}
	l.cached.Store(&listed{snap, body})
	return body, nil
}
