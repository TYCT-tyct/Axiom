package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"uuid"

	"go.opentelemetry.io/otel/metric"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/clientip"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// Run takes a call that has been admitted: it tries the route, relays the answer, meters and settles (the steps after
// 6 of internal/gateway/README.md). It owns the call from here: it ends it with Done after the settlement, which
// releases the reservation's slots, and the handler does not touch it again.
type Run func(w http.ResponseWriter, r *http.Request, c *Call)

// Deps are the modules a Handler orchestrates, every one required, and the reverse proxies this deployment trusts.
type Deps struct {
	Accounts *accounts.Service
	Catalog  *catalog.Catalog
	Router   *routing.Router
	Billing  *billing.Billing
	Limits   *Backpressure
	Memory   *protocol.Memory // the node's budget for request bodies and streams
	Run      Run
	Logger   *slog.Logger
	Meter    metric.Meter // where the handler's counters go; nil for none
	// Models is the model list in OpenRouter's shape, which the route builder fills with each snapshot it publishes: the
	// handler serves it and reads no price of its own, so a client's estimate and a call's charge are one reading.
	Models *ModelList
	// Proxies are the reverse proxies whose X-Forwarded-For names the client (AXIOM_TRUSTED_PROXIES). None trusts no
	// header, so a request is the address it came from.
	Proxies []netip.Prefix
	// KV is the shared cache the precheck counts in, and nil is single-core mode: the counts stay in this process, as
	// they were before the cache (internal/platform/kv/README.md「配置与启动」).
	KV *kv.Client
}

// Handler serves the model API: the call paths in both prefixes, and the model list.
type Handler struct {
	Deps
	mux     *http.ServeMux
	admitTx func(context.Context, accounts.Caller, accounts.AdmitRequest, func(pg.Tx, accounts.Admission) error) error

	background *background             // settlements of lost admissions
	outage     *outage                 // the dependency outage being told, if any
	listings   atomic.Pointer[listing] // the model list, encoded for the catalog snapshot it was last asked for
}

// complete reports whether every dependency is there.
func (d Deps) complete() bool {
	return d.Accounts != nil && d.Catalog != nil && d.Router != nil && d.Billing != nil && d.Limits != nil && d.Memory != nil &&
		d.Run != nil && d.Logger != nil && d.Models != nil
}

// NewHandler checks the dependencies and builds the routes.
func NewHandler(d Deps) (*Handler, error) {
	if !d.complete() {
		return nil, errors.New("gateway: the handler needs every dependency")
	}
	if !d.Accounts.GatesLookups() {
		return nil, errors.New("gateway: accounts must take its key lookups' slots from Backpressure.KeyLookupSlot")
	}
	h := &Handler{Deps: d, mux: http.NewServeMux(), admitTx: d.Accounts.Admit, background: newBackground(), outage: newOutage(d.Logger, d.Meter)}
	for _, prefix := range []string{"/v1", "/api/v1"} {
		for path, format := range map[string]protocol.Format{
			"/chat/completions": protocol.Chat, "/responses": protocol.Responses, "/messages": protocol.Messages,
		} {
			h.mux.HandleFunc("POST "+prefix+path, h.call(format, false))
			h.mux.HandleFunc("GET "+prefix+path, refuseUpgrade(format))
		}
		h.mux.HandleFunc("POST "+prefix+"/responses/compact", h.call(protocol.Responses, true))
		h.mux.HandleFunc("GET "+prefix+"/responses/compact", refuseUpgrade(protocol.Responses))
		h.modelRoutes(prefix)
		h.auxRoutes(prefix)
	}
	return h, nil
}

// ServeHTTP routes the request. It expects httpserver.Public around it, which gives every request its ID; the call
// number is that ID, and every response carries it as X-Generation-Id beside Public's X-Request-ID, errors and the
// routes that are not calls included.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Generation-Id", httpserver.RequestID(r.Context()))
	if _, pattern := h.mux.Handler(r); pattern == "" { // no route took it: the mux's own answer is plain text
		h.refuseUnrouted(w, r)
		return
	}
	h.mux.ServeHTTP(w, r)
}

// refuseUnrouted answers a request no route took: 404, or 405 with the Allow header the mux would have sent, in the
// error shell of the path's protocol. It asks the mux for the status and the methods it would allow instead of keeping
// a second routing table, which would drift from the first, and drops only its plain-text body.
func (h *Handler) refuseUnrouted(w http.ResponseWriter, r *http.Request) {
	answer := &statusOnly{header: make(http.Header)}
	h.mux.ServeHTTP(answer, r)
	f := protocol.Chat
	if isMessagesPath(r.URL.Path) {
		f = protocol.Messages
	}
	switch answer.status {
	case http.StatusNotFound:
		writeRefusal(w, f, errNotFound)
	case http.StatusMethodNotAllowed:
		if allow := answer.header.Values("Allow"); len(allow) > 0 {
			w.Header()["Allow"] = allow
		}
		writeRefusal(w, f, errMethodNotAllowed)
	default: // the mux had something else to say, such as a redirect to the canonical path
		h.mux.ServeHTTP(w, r)
	}
}

// statusOnly keeps the status and the headers of an answer, not its body: the mux's answer to a request no route took,
// whose body the protocol's error shell replaces.
type statusOnly struct {
	header http.Header
	status int
}

func (s *statusOnly) Header() http.Header { return s.header }

// Write keeps what the mux wrote out of the response: the status and the headers are the answer, and the body is the
// one the caller writes. It reports len(p) so the writer that called it sees success.
func (*statusOnly) Write(p []byte) (int, error) { return len(p), nil }

func (s *statusOnly) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
}

// isMessagesPath says whether a path belongs to the Messages format, whose errors have their own shell: everything
// under /messages in either prefix (docs/domains/gateway.md「错误」).
func isMessagesPath(path string) bool {
	path = strings.TrimPrefix(path, "/api")
	return path == "/v1/messages" || strings.HasPrefix(path, "/v1/messages/")
}

// Call is a request that has been admitted: its reservation is committed and its route is cut to the entries that
// reservation covers. Run gets it; nothing in it changes after that but what Run does through its methods.
type Call struct {
	ID        uuid.UUID // the request ID, which is also the call number
	Format    protocol.Format
	Compact   bool // the Responses compact endpoint: the upstream answers whole and its body is passed through
	Request   protocol.Request
	Body      []byte
	Header    http.Header // the client's values of the headers that may go upstream, which headers.go derives per attempt
	Session   routing.SessionKey
	Admission accounts.Admission
	Admitted  billing.Admitted
	Hold      *billing.Hold
	Plan      routing.Plan

	site        accounts.Site
	caller      accounts.Caller
	host        string
	ip          netip.Addr
	snap        *catalog.Snapshot
	models      []catalog.ModelID
	sticky      routing.EndpointID
	stickyCache *kv.Client
	stickyKey   string
	over        routing.Override // what the request's provider object narrows the route by
	rate        int              // requests of the key in the rate window, counted before admission

	mu       sync.Mutex
	releases []func()
}

// hold registers something to give back when the call is done, in reverse order.
func (c *Call) hold(release func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releases = append(c.releases, release)
}

// Done gives back the call's in-flight slots and the memory of its body. It is safe to call more than once.
func (c *Call) Done() {
	c.mu.Lock()
	releases := c.releases
	c.releases = nil
	c.mu.Unlock()
	for _, release := range slices.Backward(releases) {
		release()
	}
}

// call serves one of the call paths, or the Responses compact endpoint, which is passed through instead of relayed.
func (h *Handler) call(format protocol.Format, compact bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		release, ok := h.ipSlot(w, r, format)
		if !ok {
			return
		}
		defer release()
		c, err := h.accept(w, r, format, compact)
		if err != nil {
			h.fail(w, r, format, err)
			return
		}
		h.Run(w, r, c)
	}
}

// ipSlot takes the in-flight slot of the client's address, the first limit every endpoint that needs a key is held to.
// The address is a group (clientip.Group), as the login attempts count it: an IPv6 customer holds a whole /64, so one
// host must not take as many slots as it has addresses. When there is none it has answered the request in format and
// ok is false; otherwise the caller gives release back.
func (h *Handler) ipSlot(w http.ResponseWriter, r *http.Request, format protocol.Format) (release func(), ok bool) {
	release, err := h.Limits.IPCalls.Acquire(clientip.Group(h.clientIP(r)).String(), h.Limits.cfg.IPInFlight)
	if err != nil {
		h.fail(w, r, format, err)
		return nil, false
	}
	return release, true
}

var errUpgrade = invalid("Protocol upgrades such as WebSocket are not supported.")

// errNotFound and errMethodNotAllowed answer a path or a method no route takes, in the protocol's error shell like
// every other refusal; the mux's own plain-text answer would be the only response not shaped by it
// (docs/domains/gateway.md「错误」).
var (
	errNotFound         = &Refusal{Status: http.StatusNotFound, Code: "not_found"}
	errMethodNotAllowed = &Refusal{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed"}
)

// notServed answers a path that is registered only to keep a wildcard from taking it, as the mux answers a path nothing
// serves.
func notServed(w http.ResponseWriter, r *http.Request) { writeRefusal(w, formatFor(r), errNotFound) }

// refuseUpgrade answers the WebSocket handshake a client may try on a call path before it falls back to POST.
func refuseUpgrade(format protocol.Format) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			w.Header().Set("Allow", http.MethodPost)
			writeRefusal(w, format, errMethodNotAllowed)
			return
		}
		writeRefusal(w, format, errUpgrade)
	}
}

// accept takes a request as far as admission: recognise the site and the key from the headers alone, take the
// slots, read the body, resolve the model, admit. On any failure the call's holds are given back; the one exception
// is a lost admission, whose reservation may exist, so its settlement keeps them until it is done.
func (h *Handler) accept(w http.ResponseWriter, r *http.Request, format protocol.Format, compact bool) (*Call, error) {
	if r.Header.Get("Upgrade") != "" {
		return nil, errUpgrade
	}
	site, caller, err := h.authenticate(r)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(httpserver.RequestID(r.Context()))
	if err != nil {
		return nil, err
	}
	c := &Call{ID: id, Format: format, Compact: compact, Header: forwardable(r.Header), site: site, caller: caller, host: r.Host, ip: h.clientIP(r)}
	if err := h.take(w, r, c); err != nil {
		if !errors.Is(err, errCommitUnknown) {
			c.Done()
		}
		return nil, err
	}
	return c, nil
}

// take takes the call's slots, reads its body and admits it.
func (h *Handler) take(w http.ResponseWriter, r *http.Request, c *Call) error {
	release, err := h.Limits.InFlight.Acquire(accountKey(c.caller), 0)
	if err != nil {
		return err
	}
	c.hold(release)
	if err := h.read(w, r, c); err != nil {
		return err
	}
	return h.admit(r.Context(), c)
}

// currentCatalog is the catalog's snapshot; getting one ends a catalog outage.
func (h *Handler) currentCatalog() (*catalog.Snapshot, error) {
	snap, err := h.Catalog.Current()
	if err == nil {
		h.outage.recovered(depCatalog)
	}
	return snap, err
}

// admitDB runs an admission transaction; one that commits ends a database outage.
func (h *Handler) admitDB(ctx context.Context, caller accounts.Caller, req accounts.AdmitRequest, fn func(pg.Tx, accounts.Admission) error) error {
	err := h.admitTx(ctx, caller, req, fn)
	if err == nil {
		h.outage.recovered(depDatabase)
	}
	return err
}

// authenticate finds the site by the Host and the key by the headers; the body is not read. An unregistered host, a
// host that is not an API domain and a key that is not known are all the same 401.
func (h *Handler) authenticate(r *http.Request) (accounts.Site, accounts.Caller, error) {
	site, surface, err := h.Accounts.SiteForHost(r.Host)
	if errors.Is(err, accounts.ErrStale) {
		return accounts.Site{}, accounts.Caller{}, err
	}
	h.outage.recovered(depAccounts)
	if err != nil || surface != accounts.API {
		return accounts.Site{}, accounts.Caller{}, errUnauthorized
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.Limits.cfg.KeyLookup)
	defer cancel()
	caller, err := h.Accounts.AuthenticateKey(ctx, site.ID, apiKey(r))
	return site, caller, err
}

// apiKey is the key in Authorization: Bearer, or else in x-api-key.
func apiKey(r *http.Request) string {
	if scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
		return strings.TrimSpace(token)
	}
	return r.Header.Get("X-Api-Key")
}

// accountKey and keyKey are what the limits and the queue count an account and a key by.
func accountKey(c accounts.Caller) string {
	id := uuid.UUID(c.AccountID())
	return string(id[:])
}

func keyKey(c accounts.Caller) string {
	id := uuid.UUID(c.KeyID())
	return string(id[:])
}

// read reads the body and everything the request says: its summary, the models, its session and its count in the
// rate window. Nothing here touches the database.
func (h *Handler) read(w http.ResponseWriter, r *http.Request, c *Call) error {
	body, err := h.readBody(w, r, c)
	if err != nil {
		return err
	}
	req, err := protocol.Inspect(c.Format, body)
	if err != nil {
		return err
	}
	if req.Previous != "" { // continuing a stored response is M2 (docs/domains/gateway.md「Responses 的会话状态」)
		return invalid("previous_response_id is not supported yet.")
	}
	if c.Compact && req.Stream { // the compact endpoint answers a whole reply (docs/domains/gateway.md「转发规则」)
		return invalid("The compact endpoint answers one reply, not a stream; leave stream out or set it to false.")
	}
	over, names, err := readRouting(&req)
	if err != nil {
		return err
	}
	snap, err := h.currentCatalog()
	if err != nil {
		return err
	}
	models, err := resolve(snap, names)
	if err != nil {
		return err
	}
	c.Body, c.Request, c.snap, c.models, c.over = body, req, snap, models, over
	c.Session = h.Router.SessionKey(routing.AccountID(c.caller.AccountID()), &c.Request, body)
	c.rate = h.precheck(r.Context(), c.site.ID, callDimension, keyKey(c.caller), keyHex(c.caller),
		c, r.Header.Get("X-Claude-Code-Context-Compacted") != "")
	return nil
}

// fail answers a request that did not get through, unless its client has gone. A body that was too slow is answered
// though the context has ended: net/http cancels it when the read deadline passes, and the connection can still be
// written to.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, format protocol.Format, err error) {
	if r.Context().Err() != nil && !errors.Is(err, errBodyTimeout) {
		return
	}
	ref, expected := refusal(format, err)
	h.Limits.refused(r.Context(), ref)
	if dep, reason, out := outageOf(err); out {
		h.outage.refuse(r.Context(), dep, reason)
		writeRefusal(w, format, ref)
		return
	}
	switch {
	case !expected:
		h.Logger.Error("a request failed unexpectedly", "request_id", httpserver.RequestID(r.Context()), "err", err)
	case ref.Status >= http.StatusInternalServerError:
		h.Logger.Warn("a request was turned away", "request_id", httpserver.RequestID(r.Context()), "status", ref.Status, "err", err)
	}
	writeRefusal(w, format, ref)
}
