package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"uuid"

	"go.opentelemetry.io/otel/metric"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/fileparse"
	"axiom/internal/observability"
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
	// Outstanding is the node's drain counter, the one its Executor counts on: a request is counted from the gate until
	// ServeHTTP returns, and the settlement of a lost admission until it is written.
	Outstanding *Outstanding
	// Search is the deployment's search engines; nil means none is configured, and every request that asks for web
	// search keeps its documented refusal.
	Search SearchBackends
	// Parser runs the file-parser plugin's OCR engines through the deployment's backend; nil means no backend is
	// configured, and a request that asks for one keeps its documented refusal.
	Parser fileparse.Parser
	// Records is the call log the handler reserves a record in before admitting a call, so a call is never served when
	// its record cannot be kept (docs/domains/observability.md「写入路径」). nil is for tests that do not exercise the
	// call log; the production node always sets it.
	Records *observability.Recorder
}

// Handler serves the model API: the call paths in both prefixes, and the model list.
type Handler struct {
	Deps
	mux          *http.ServeMux
	admitTx      func(context.Context, accounts.Caller, accounts.AdmitRequest, func(pg.Tx, accounts.Admission) error) error
	admitReadTx  func(context.Context, accounts.Caller, accounts.AdmitRequest, func(pg.Tx, accounts.Admission) error) error
	admitBatchTx func(context.Context, []accounts.AdmitCall, func(pg.Tx, []accounts.Admission, []error) error) error

	charges  *billing.ChargeReader
	outage   *outage                 // the dependency outage being told, if any
	listings atomic.Pointer[listing] // the model list, encoded for the catalog snapshot it was last asked for
}

// complete reports whether every dependency is there.
func (d Deps) complete() bool {
	return d.Accounts != nil && d.Catalog != nil && d.Router != nil && d.Billing != nil && d.Limits != nil && d.Memory != nil &&
		d.Run != nil && d.Logger != nil && d.Models != nil && d.Outstanding != nil
}

// NewHandler checks the dependencies and builds the routes.
func NewHandler(d Deps) (*Handler, error) {
	if !d.complete() {
		return nil, errors.New("gateway: the handler needs every dependency")
	}
	if !d.Accounts.GatesLookups() {
		return nil, errors.New("gateway: accounts must take its key lookups' slots from Backpressure.KeyLookupSlot")
	}
	h := &Handler{
		Deps: d, mux: http.NewServeMux(), admitTx: d.Accounts.Admit, admitReadTx: d.Accounts.AdmitRead,
		admitBatchTx: d.Accounts.AdmitBatch, outage: newOutage(d.Logger, d.Meter), charges: billing.NewChargeReader(d.Logger, d.Meter),
	}
	for _, prefix := range []string{"/v1", "/api/v1"} {
		for path, format := range map[string]protocol.Format{
			"/chat/completions": protocol.Chat, "/responses": protocol.Responses, "/messages": protocol.Messages,
			"/completions": protocol.Completions, "/embeddings": protocol.Embeddings, "/rerank": protocol.Rerank,
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
	// The drain counts the request from the moment it passes the gate, not from Run: a request can still be reading
	// its body when the drain begins, and starting a stream after it would otherwise escape the drain. What it hands
	// to the background is counted before this is released (Outstanding).
	defer h.Outstanding.add()()
	w.Header().Set("X-Generation-Id", httpserver.RequestID(r.Context()))
	if _, pattern := h.mux.Handler(r); pattern == "" { // no route took it: the mux's own answer is plain text
		h.refuseUnrouted(w, r)
		return
	}
	h.mux.ServeHTTP(w, r)
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
	c := &Call{ID: id, At: time.Now(), Format: format, Compact: compact, Header: forwardable(r.Header), site: site, caller: caller, host: r.Host, ip: h.clientIP(r)}
	if err := h.take(w, r, c); err != nil {
		if !errors.Is(err, errCommitUnknown) {
			c.Done()
		}
		return nil, err
	}
	return c, nil
}

// take takes the call's slots, reads its body and admits it. The record's room is taken with the in-flight slots: an
// admitted call that cannot be recorded would be a call served without its log, so the node refuses it instead.
func (h *Handler) take(w http.ResponseWriter, r *http.Request, c *Call) error {
	release, err := h.Limits.InFlight.Acquire(accountKey(c.caller), 0)
	if err != nil {
		return err
	}
	c.hold(release)
	if h.Records != nil {
		slot, err := h.Records.Admit()
		if err != nil {
			if errors.Is(err, observability.ErrBacklog) || errors.Is(err, observability.ErrBroken) {
				return errRecordBacklog
			}
			return err
		}
		c.record = slot
		c.hold(slot.Release)
	}
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

// admitReadDB is admitDB for the reads a management key may also make; one that commits ends a database outage.
func (h *Handler) admitReadDB(ctx context.Context, caller accounts.Caller, req accounts.AdmitRequest, fn func(pg.Tx, accounts.Admission) error) error {
	err := h.admitReadTx(ctx, caller, req, fn)
	if err == nil {
		h.outage.recovered(depDatabase)
	}
	return err
}

// admitDBBatch runs an admission batch; one that commits ends a database outage.
func (h *Handler) admitDBBatch(ctx context.Context, calls []accounts.AdmitCall, fn func(pg.Tx, []accounts.Admission, []error) error) error {
	err := h.admitBatchTx(ctx, calls, fn)
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

// prepareServer refuses an openrouter:web_search tool this node cannot serve and removes its declaration, so routing,
// the quote and the converters see the body the gateway will send.
func (h *Handler) prepareServer(c *Call, body []byte, req protocol.Request) ([]byte, protocol.Request, error) {
	if ref := h.checkServerTool(&req, body); ref != nil {
		return nil, req, ref
	}
	if req.ServerSearch == nil {
		return body, req, nil
	}
	return h.stripServerTool(c, body, req)
}

// read reads the body and everything the request says: its summary, the models, its session and its count in the
// rate window. Nothing here touches the database.
//
//nolint:cyclop // one branch per request plugin and refusal
func (h *Handler) read(w http.ResponseWriter, r *http.Request, c *Call) error {
	body, err := h.readBody(w, r, c)
	if err != nil {
		return err
	}
	sessionBody := body // default expansion must not move a client cache key beyond the session prefix
	body, req, err := h.expandPreset(c, body)
	if err != nil {
		return err
	}
	if err := unsupportedRequest(c, &req); err != nil {
		return err
	}
	over, names, err := readRouting(&req, body)
	if err != nil {
		return err
	}
	onlineWebPlugin(&req, names.online)
	if ref := h.checkWebSearch(&req); ref != nil {
		return ref
	}
	body, req, err = h.prepareServer(c, body, req)
	if err != nil {
		return err
	}
	if err := h.gateParsing(r, c, sessionBody, req); err != nil {
		return err
	}
	body, req, err = h.parseFiles(r.Context(), c, body, req, h.fileBilling())
	if err != nil {
		return err
	}
	if err := pinnedProvider(&req, over); err != nil {
		return err
	}
	snap, err := h.currentCatalog()
	if err != nil {
		return err
	}
	models, err := resolveModels(snap, names, &req)
	if err != nil {
		return err
	}
	web, file, parses, pages, billing := req.Web, req.File, req.FileParses, req.FilePages, req.FileBilling
	server := req
	body, req, err = h.compress(c, body, req, snap, models)
	if err != nil {
		return err
	}
	restoreWeb(&req, web)
	restoreFile(&req, file, parses, pages, billing)
	restoreServer(&req, server)
	return h.fillCall(c, r, sessionBody, body, req, snap, models, names, over)
}

// fillCall fills the call with what routing and admission read, and counts the key's rate window: nothing here touches the
// database.
func (h *Handler) fillCall(c *Call, r *http.Request, sessionBody, body []byte, req protocol.Request, snap *catalog.Snapshot,
	models []catalog.ModelID, names variantNames, over routing.Override,
) error {
	c.Body, c.Request, c.snap, c.models, c.auto, c.free, c.over = body, req, snap, models, names.auto, names.free, over
	c.cache = cachePlanFor(c, r.Header)
	headerSession := strings.TrimSpace(r.Header.Get("X-Session-Id"))
	if !utf8.ValidString(headerSession) || utf8.RuneCountInString(headerSession) > 256 {
		return invalid("x-session-id must be valid UTF-8 with at most 256 characters.")
	}
	if c.gated {
		return nil // the file parser's gate counted this call before parsing; the session and rate are already set
	}
	c.Session = h.Router.SessionKey(routing.AccountID(c.caller.AccountID()), &c.Request, sessionBody, headerSession)
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

// pinnedProvider refuses a file reference the client did not pin to one provider: a file id is the supplier account's
// resource, not content, and the plan is cut to a single attempt, so the id can only reach that provider (protocol
// README.md「跨格式转换」).
func pinnedProvider(req *protocol.Request, over routing.Override) error {
	if req.FileRefs > 0 && len(over.OnlyNames) != 1 {
		return invalid("A file reference (file_id) is only sent back to the provider that issued it: name that one provider in provider.only.")
	}
	return nil
}

// unsupportedRequest refuses what this gateway does not serve yet: continuing a stored response is M2, and the compact
// endpoint answers one reply (docs/domains/gateway.md「Responses 的会话状态」「转发规则」).
func unsupportedRequest(c *Call, req *protocol.Request) error {
	if req.Previous != "" {
		return invalid("previous_response_id is not supported yet.")
	}
	if c.Compact && req.Stream {
		return invalid("The compact endpoint answers one reply, not a stream; leave stream out or set it to false.")
	}
	return nil
}
