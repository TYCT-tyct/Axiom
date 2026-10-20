package gateway

import (
	"context"
	"net/http"
	"strings"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// Every model says it was created at the Unix epoch: the catalog keeps no listing time, and the clients that need the
// field only need it to be there.
const (
	createdUnix = 0
	createdAt   = "1970-01-01T00:00:00Z"
)

// visibility is what the model endpoints show: the catalog snapshot, the models this key may call (nil is every model)
// and the router that says which models have a route. A model is shown when both agree. Capacity is not part of it: an
// endpoint that is full, broken or out of budget must not make its model vanish from the list, because a call is what
// waits for capacity and the list is not (routing.Router.Serves). The list in OpenRouter's shape asks the router nothing:
// the build that made it recorded which models it served, so one response is one build's (openRouterListing.shows).
type visibility struct {
	snap    *catalog.Snapshot
	allowed map[catalog.ModelID]struct{} // nil: every model
	router  *routing.Router
}

// modelRoutes adds the model list and the single model. The list under /api/v1 is OpenRouter's shape and the one under /v1
// is OpenAI's and Anthropic's (openrouter.go), so a client is not handed the other protocol's members; the single model
// follows its prefix's list.
func (h *Handler) modelRoutes(prefix string) {
	list, one := h.listModels, h.retrieveModel
	if prefix == "/api/v1" {
		list, one = h.listOpenRouterModels, h.retrieveOpenRouterModel
	}
	h.mux.HandleFunc("GET "+prefix+"/models", list)
	// A model name holds a '/', so the wildcard takes the rest of the path, not one member.
	h.mux.HandleFunc("GET "+prefix+"/models/{model...}", one)
	// OpenRouter's other paths under /models are not served; without these the wildcard would take them for names.
	for _, path := range []string{"/models/count", "/models/user", "/models/{author}/{slug}/endpoints"} {
		h.mux.HandleFunc("GET "+prefix+path, notServed)
	}
}

// listModels answers GET /v1/models in the shape of OpenAI's and of Anthropic's at once, so every client reads its own
// members. A key sees the models it may call and that have an endpoint, by the permission a call is judged with, and a
// model is listed by its catalog name and, when that name resolves to the same model, by its maker's own: Claude Code
// takes only the entries whose ID holds "claude" or "anthropic".
func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	format := formatFor(r)
	release, ok := h.ipSlot(w, r, format)
	if !ok {
		return
	}
	defer release()
	v, err := h.visible(r)
	var l *listing
	if err == nil {
		l, err = h.listingFor(v.snap)
	}
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	writeBody(w, l.encode(v)) // no-store: the list is this key's, not a shared cache's
}

// retrieveModel answers GET /v1/models/{model}: the entry the list shows for the name, resolved as a call resolves it,
// so an alias gives the model behind it. A name the catalog does not know, a model nothing serves and one this key may
// not use are the same 404 the list leaves them out with.
func (h *Handler) retrieveModel(w http.ResponseWriter, r *http.Request) {
	format := formatFor(r)
	release, ok := h.ipSlot(w, r, format)
	if !ok {
		return
	}
	defer release()
	var entry modelEntry
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
	return visibility{snap: snap, allowed: allowed, router: h.Router}, nil
}

// shows says whether the model is for this key to see: it may call it, and a call has a route to take.
func (v visibility) shows(m catalog.ModelID) bool { return v.allows(m) && v.router.Serves(m) }

// allows says whether this key may call the model, whatever routing holds now: the half of shows that the list in
// OpenRouter's shape freezes at build (openRouterListing.shows), so that one response's total, page and entries are one
// build's and a route that appears or goes while the response is written cannot make them disagree.
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
	return h.admitDB(ctx, caller, accounts.AdmitRequest{Domain: r.Host, ClientIP: h.clientIP(r)}, func(tx pg.Tx, a accounts.Admission) error { return fn(ctx, tx, a) })
}

// model is the entry for one name, resolved as a call resolves it, or the error a call gets: a name the catalog does
// not know or has retired is model_not_found, and so is a model this key may not use, which is never 403, and one
// nothing serves.
func (v visibility) model(name string) (modelEntry, error) {
	resolved, err := v.snap.ResolvePublic(name)
	if err != nil {
		return modelEntry{}, err
	}
	m, ok := v.snap.Model(resolved.Model)
	if !ok || !v.shows(m.ID) {
		return modelEntry{}, errNoModel
	}
	author, display, _ := strings.Cut(m.Slug, "/")
	return newModelEntry(name, author, display), nil
}

// modelEntry is one model as OpenAI's and as Anthropic's clients read it: the members both need, under the name that
// was asked for.
type modelEntry struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	OwnedBy     string `json:"owned_by"`
	Created     int64  `json:"created"`
	CreatedAt   string `json:"created_at"`
}

func newModelEntry(id, author, display string) modelEntry {
	return modelEntry{
		ID: id, Object: "model", Type: "model", DisplayName: display, OwnedBy: author,
		Created: createdUnix, CreatedAt: createdAt,
	}
}

// listing is the catalog's models as the list shows them, encoded once per catalog snapshot: what a request pays is
// choosing which of the encoded entries its key sees and copying them out, not building and encoding each one again.
type listing struct {
	snap   *catalog.Snapshot
	models []modelEntries // in the snapshot's order
}

// modelEntries is the entries of one model, one for each public name the list shows it under.
type modelEntries struct {
	id      catalog.ModelID
	entries []listedEntry
	size    int // the bytes of its entries and the commas between them
}

// listedEntry is an entry as JSON, and its ID as a JSON string, which first_id and last_id repeat.
type listedEntry struct {
	id, body []byte
}

// listingFor is the encoded list of the snapshot, built when the catalog's snapshot is not the one it was built for. A
// snapshot is never changed, so the encoding never goes stale; two requests that build it at once make the same list.
func (h *Handler) listingFor(snap *catalog.Snapshot) (*listing, error) {
	if l := h.listings.Load(); l != nil && l.snap == snap {
		return l, nil
	}
	l, err := newListing(snap)
	if err != nil {
		return nil, err
	}
	h.listings.Store(l)
	return l, nil
}

// newListing lists each model of the snapshot by the names that resolve to it: its catalog name, and its maker's own
// when that is a public name of the same model.
func newListing(snap *catalog.Snapshot) (*listing, error) {
	l := &listing{snap: snap}
	for m := range snap.Models() {
		author, name, _ := strings.Cut(m.Slug, "/")
		lm := modelEntries{id: m.ID}
		for _, id := range []string{m.Slug, name} {
			if r, err := snap.ResolvePublic(id); err != nil || r.Model != m.ID {
				continue
			}
			e, err := encodeEntry(newModelEntry(id, author, name))
			if err != nil {
				return nil, err
			}
			lm.entries = append(lm.entries, e)
			lm.size += len(e.body) + 1
		}
		if len(lm.entries) > 0 {
			l.models = append(l.models, lm)
		}
	}
	return l, nil
}

func encodeEntry(m modelEntry) (listedEntry, error) {
	body, err := jsonx.MarshalCanonical(m)
	if err != nil {
		return listedEntry{}, err
	}
	id, err := jsonx.MarshalCanonical(m.ID)
	return listedEntry{id: id, body: body}, err
}

// idsRoom is what the buffer is given beyond the entries: the list's own members and the two IDs they repeat.
const idsRoom = 512

// encode is the list as the key sees it, in the canonical form writeJSON would give it (members in key order), made in
// one buffer sized for the entries it holds. It counts first so a key that sees few models does not pay for the
// catalog; a model whose route appears or goes between the two passes only makes the count a little off.
func (l *listing) encode(v visibility) []byte {
	size := idsRoom
	for i := range l.models {
		if v.shows(l.models[i].id) {
			size += l.models[i].size
		}
	}
	buf := append(make([]byte, 0, size), `{"data":[`...)
	var first, last []byte
	for i := range l.models {
		if !v.shows(l.models[i].id) {
			continue
		}
		for _, e := range l.models[i].entries {
			if first != nil {
				buf = append(buf, ',')
			} else {
				first = e.id
			}
			last = e.id
			buf = append(buf, e.body...)
		}
	}
	buf = append(buf, ']')
	if first != nil {
		buf = append(append(buf, `,"first_id":`...), first...)
	}
	buf = append(buf, `,"has_more":false`...)
	if last != nil {
		buf = append(append(buf, `,"last_id":`...), last...)
	}
	return append(buf, `,"object":"list"}`...)
}
