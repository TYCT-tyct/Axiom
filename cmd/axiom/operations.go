package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/getkin/kin-openapi/openapi3"

	"axiom/internal/accounts"
	"axiom/internal/operations"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// The operations API (api/operations.yaml, docs/domains/operations.md「运营接口」): what the operations agent calls through
// the MCP bridge, on the core's operations listener. Its reads go through the admin's handlers, so the two APIs read
// alike and the bodies, which the contracts define identically (just api), cannot drift; the guard has authenticated
// the credential and put the scope it may read in the call before a handler runs.

// operationsContract is the operations contract as the generated code embeds it, with the rules read out of it.
func operationsContract() (*contract, error) { return operationsContractOnce() }

var operationsContractOnce = sync.OnceValues(func() (*contract, error) {
	doc, err := operationsapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("the operations contract: %w", err)
	}
	c, err := loadContract(doc)
	if err != nil {
		return nil, fmt.Errorf("the operations contract: %w", err)
	}
	return c, nil
})

// The values of x-axiom-scope: the platform's own resources, the credential's sites, or the credential's own record.
const (
	agentPlatform = "platform"
	agentSites    = "sites"
	agentSelf     = "self"
)

// agentRules is what an operation of the operations API needs, read from the contract: the permission, where it acts,
// whether it moves money (x-axiom-money, which needs operations.money besides) and the MCP tool it is.
type agentRules struct {
	permission accounts.Permission
	scope      string
	money      bool
	tool       string
}

// agentRulesOf reads them, refusing a contract this program cannot enforce.
func agentRulesOf(op *openapi3.Operation) (*agentRules, error) {
	name, _ := op.Extensions["x-axiom-permission"].(string)
	permission, ok := accounts.PermissionNamed(name)
	if !ok {
		return nil, fmt.Errorf("x-axiom-permission %q is not declared by this program", name)
	}
	scope, _ := op.Extensions["x-axiom-scope"].(string)
	if scope != agentPlatform && scope != agentSites && scope != agentSelf {
		return nil, fmt.Errorf("x-axiom-scope %q is not platform, sites or self", scope)
	}
	tool, _ := op.Extensions["x-mcp-name"].(string)
	if tool == "" {
		return nil, errors.New("no x-mcp-name")
	}
	money, _ := op.Extensions["x-axiom-money"].(bool)
	return &agentRules{permission: permission, scope: scope, money: money, tool: tool}, nil
}

// readDeadline bounds a read of the operations API and writeDeadline a write (docs/domains/operations.md「运营接口」):
// a write runs the change it asks for, a read only reads the database.
const (
	readDeadline  = 15 * time.Second
	writeDeadline = 30 * time.Second
)

// recordDeadline bounds writing a request's record, which runs after the request's own deadline may have passed.
const recordDeadline = 5 * time.Second

// agentGuard stands in front of every operation of the operations API: the credential is live and holds the permission
// the operation names in the scope it acts in, and the request keeps to the contract. Every request of a known
// credential is recorded in the operation log before its answer leaves, refusals included; a request whose record
// cannot be written is answered 503 instead, so no answer goes out unrecorded. A token that names no credential, or one
// over its rate, has nobody to record it for.
type agentGuard struct {
	contract *contract
	accounts *accounts.Service
	db       *pg.DB
	logger   *slog.Logger
}

func (g *agentGuard) serve(w http.ResponseWriter, r *http.Request, pattern string, routes http.Handler) {
	deadline := readDeadline
	if r.Method != http.MethodGet {
		deadline = writeDeadline
	}
	ctx, cancel := context.WithTimeout(r.Context(), deadline)
	defer cancel()
	r = r.WithContext(ctx)
	rl, ok := g.contract.byPattern[pattern]
	if !ok || rl.agent == nil { // every route is registered from the contract, which loadContract read, so this is a bug
		respond(w, r, g.logger, errors.New("a route the contract does not declare for a credential: "+pattern))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	agent, err := g.accounts.AuthenticateAgent(ctx, bearerToken(r))
	if err != nil {
		respond(w, r, g.logger, err)
		return
	}
	started := time.Now()
	answer := &bufferedResponse{header: http.Header{}}
	body, err := heldBody(r)
	if err != nil {
		respond(answer, r, g.logger, invalidField("", "the request body is unreadable or larger than 1 MiB"))
	} else {
		g.answer(answer, r, rl, agent, routes)
	}
	if err := g.record(ctx, r, pattern, rl.agent.tool, agent, body, answer, started); err != nil {
		g.logger.Error("recording an operations call", "err", err, "request_id", httpserver.RequestID(ctx))
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "the call could not be recorded; nothing was answered")
		return
	}
	answer.sendTo(w)
}

// answer authorizes the credential, checks the request and runs the handler, writing whatever the answer is to w.
func (g *agentGuard) answer(w *bufferedResponse, r *http.Request, rl *rules, agent accounts.Agent, routes http.Handler) {
	scope, err := g.accounts.AuthorizeAgent(agent, rl.agent.permission, rl.agent.scope == agentPlatform)
	if err == nil && rl.agent.money {
		if _, err = g.accounts.AuthorizeAgent(agent, accounts.OperationsMoney, rl.agent.scope == agentPlatform); err == nil {
			err = g.accounts.SpendAgentMoney(agent) // a preview and a retry are writes asked for too
		}
	}
	if err != nil {
		respond(w, r, g.logger, err)
		return
	}
	if rl.agent.scope == agentSelf { // the credential's own rows, which the handler selects by its ID, are platform data
		scope = pg.PlatformScope()
	}
	if fields := rl.validate(r.Context(), r); len(fields) > 0 {
		respond(w, r, g.logger, &apiError{
			status: http.StatusBadRequest, code: codeInvalidRequest,
			message: "the request breaks the contract", fields: fields,
		})
		return
	}
	routes.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callKey{}, call{r: r, scope: scope, agent: agent})))
}

// bearerToken is the request's Authorization bearer token, or "" when it has none.
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return token
}

// runIDPattern is the contract's X-Axiom-Run-ID, which the operation log's check repeats: a run ID that breaks it is a
// 400, and is not recorded.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// maxReason is the longest reason the contract and the log accept, in characters.
const maxReason = 1000

// heldBody reads a write's body for its record and leaves it for the handler to read again; a read has none.
func heldBody(r *http.Request) ([]byte, error) {
	if r.Method == http.MethodGet {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, err
}

// record writes the request's entry in the operation log, on its own deadline: a request that ran out of time still
// leaves its record. A read's reason is in its query, a write's in its body, whose other members are its input.
func (g *agentGuard) record(ctx context.Context, r *http.Request, pattern, tool string, agent accounts.Agent, body []byte, answer *bufferedResponse, started time.Time) error {
	entry := operations.LoggedCall{
		Credential: uuid.UUID(agent.ID()), Tool: tool, Reason: r.URL.Query().Get("reason"), Input: inputOf(pattern, r),
		Status: answer.statusCode(), Error: answer.errorCode(), RequestID: httpserver.RequestID(ctx), Started: started,
		Ended: time.Now(),
	}
	if r.Method != http.MethodGet {
		entry.Reason, entry.Operation = bodyInput(body, entry.Input), answer.operation()
	}
	entry.Reason = truncate(entry.Reason, maxReason)
	if run := r.Header.Get("X-Axiom-Run-Id"); runIDPattern.MatchString(run) {
		entry.Run = run
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordDeadline)
	defer cancel()
	return g.db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := operations.RecordCall(ctx, tx, entry)
		return err
	})
}

// truncate is s cut to at most n characters, for a reason the contract refused as too long but the log still keeps.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// inputOf is what the request asked with: its path parameters by name, as the route pattern names them, and its query
// parameters but the reason, which the record keeps apart.
func inputOf(pattern string, r *http.Request) map[string]any {
	input := map[string]any{}
	if path := pathInput(pattern, r.URL.Path); len(path) > 0 {
		input["path"] = path
	}
	if query := queryInput(r); len(query) > 0 {
		input["query"] = query
	}
	return input
}

// bodyInput puts a write's JSON body but its reason into input, and returns the reason; a body that is not a JSON
// object, which the contract refused, is left out.
func bodyInput(body []byte, input map[string]any) string {
	var members map[string]any
	if jsonx.Unmarshal(body, &members) != nil || members == nil {
		return ""
	}
	reason, _ := members["reason"].(string)
	delete(members, "reason")
	if len(members) > 0 {
		input["body"] = members
	}
	return reason
}

// pathInput is the path's parameters by the names the route pattern gives them.
func pathInput(pattern, path string) map[string]any {
	_, route, _ := strings.Cut(pattern, " ")
	names, values := strings.Split(route, "/"), strings.Split(path, "/")
	params := map[string]any{}
	if len(names) != len(values) {
		return params
	}
	for i, name := range names {
		if strings.HasPrefix(name, "{") && strings.HasSuffix(name, "}") {
			params[name[1:len(name)-1]] = values[i]
		}
	}
	return params
}

// queryInput is the query's parameters but the reason: one value as itself, several as a list.
func queryInput(r *http.Request) map[string]any {
	query := map[string]any{}
	for name, vals := range r.URL.Query() {
		if name == "reason" {
			continue
		}
		if len(vals) == 1 {
			query[name] = vals[0]
		} else {
			query[name] = vals
		}
	}
	return query
}

// bufferedResponse holds an answer until its record is written: the guard sends it only then.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(p)
}

func (b *bufferedResponse) statusCode() int {
	if b.status == 0 {
		return http.StatusOK
	}
	return b.status
}

// errorCode is the code of a failure's body, the contract's Error; "" for a success.
func (b *bufferedResponse) errorCode() string {
	if b.statusCode() < http.StatusBadRequest {
		return ""
	}
	var failed errorBody
	if jsonx.Unmarshal(b.body.Bytes(), &failed) != nil {
		return ""
	}
	return string(failed.Error.Code)
}

// operation is the ID of the operation a successful answer names; the zero ID for any other answer.
func (b *bufferedResponse) operation() uuid.UUID {
	var answered struct {
		ID uuid.UUID `json:"operation_id"`
	}
	if b.statusCode() >= http.StatusBadRequest || jsonx.Unmarshal(b.body.Bytes(), &answered) != nil {
		return uuid.UUID{}
	}
	return answered.ID
}

// sendTo writes the held answer to w, under the headers w already carries.
func (b *bufferedResponse) sendTo(w http.ResponseWriter) {
	maps.Copy(w.Header(), b.header)
	w.WriteHeader(b.statusCode())
	_, _ = w.Write(b.body.Bytes()) //nolint:errcheck // the client left; there is no one to tell
}

// operationsRoutes are the operations listener's routes: the contract's operations behind the guard, and a 404 for any
// other path. There are no pages and nothing is compressed: the only client is the MCP bridge.
func operationsRoutes(api operationsapi.StrictServerInterface, g *agentGuard, logger *slog.Logger) http.Handler {
	return apiRoutes(g, logger, func(m mounting) http.Handler {
		ops := operationsapi.NewStrictHandlerWithOptions(api, nil, operationsapi.StrictHTTPServerOptions{
			RequestErrorHandlerFunc: invalidRequest, ResponseErrorHandlerFunc: m.handlerFailed,
		})
		return operationsapi.HandlerWithOptions(ops, operationsapi.StdHTTPServerOptions{BaseRouter: m.mux, ErrorHandlerFunc: invalidRequest})
	})
}

// operationsAPI implements the operations contract.
type operationsAPI struct {
	admin adminAPI
	db    *pg.DB
}

// Whoami is the credential's own description.
func (operationsAPI) Whoami(ctx context.Context, _ operationsapi.WhoamiRequestObject) (operationsapi.WhoamiResponseObject, error) {
	info := callOf(ctx).agent.Info()
	body := operationsapi.AgentSelf{
		Id: info.ID.TypeID(), Name: info.Name, Permissions: info.Permissions, Platform: info.Platform,
		ProposerVendors: info.Vendors, ExpiresAt: info.ExpiresAt,
	}
	if info.Sites != nil {
		body.Sites = new(typeIDs(info.Sites))
	}
	return operationsapi.Whoami200JSONResponse(body), nil
}

// OperationLogList is the credential's own record, newest first.
func (o operationsAPI) OperationLogList(ctx context.Context, r operationsapi.OperationLogListRequestObject) (operationsapi.OperationLogListResponseObject, error) {
	var after uuid.UUID
	if r.Params.Cursor != nil {
		id, err := uuid.Parse(*r.Params.Cursor)
		if err != nil {
			return nil, invalidField("cursor", "not a cursor this API gave out")
		}
		after = id
	}
	c := callOf(ctx)
	var page []operations.LoggedCall
	var more bool
	if err := o.db.ReadTx(ctx, c.scope, func(tx pg.Tx) (err error) {
		page, more, err = operations.LoggedCalls(ctx, tx, uuid.UUID(c.agent.ID()), after, pageSize(r.Params.Limit))
		return err
	}); err != nil {
		return nil, err
	}
	list := operationsapi.OperationLogList{Data: make([]operationsapi.OperationLogEntry, len(page))}
	for i, e := range page {
		input, err := jsonx.MarshalCanonical(e.Input)
		if err != nil {
			return nil, err
		}
		entry := operationsapi.OperationLogEntry{
			Id: e.ID.String(), Tool: e.Tool, Reason: e.Reason, Input: input, Status: e.Status, RequestId: e.RequestID,
			StartedAt: e.Started, EndedAt: e.Ended,
		}
		if e.Run != "" {
			entry.RunId = &e.Run
		}
		if e.Error != "" {
			entry.ErrorCode = &e.Error
		}
		if e.Operation != (uuid.UUID{}) {
			entry.OperationId = new(e.Operation.String())
		}
		list.Data[i] = entry
	}
	if more {
		list.NextCursor = new(page[len(page)-1].ID.String())
	}
	return operationsapi.OperationLogList200JSONResponse(list), nil
}

// relayed is the admin handler's answer of type A as the operations contract's T: the contracts define the bodies
// alike, so the JSON one writes is the JSON the other reads. Any other answer is a bug.
func relayed[T, A any](answer any, err error) (T, error) {
	var out T
	if err != nil {
		return out, err
	}
	body, ok := answer.(A)
	if !ok {
		return out, fmt.Errorf("the admin handler answered %T", answer)
	}
	return adminJSON[T](body)
}

// SuppliersList lists suppliers as the admin does.
func (o operationsAPI) SuppliersList(ctx context.Context, r operationsapi.SuppliersListRequestObject) (operationsapi.SuppliersListResponseObject, error) {
	answer, err := o.admin.ListSuppliers(ctx, adminapi.ListSuppliersRequestObject{Params: adminapi.ListSuppliersParams{
		Status: (*adminapi.SupplierStatus)(r.Params.Status), Cursor: r.Params.Cursor, Limit: r.Params.Limit,
	}})
	return relayed[operationsapi.SuppliersList200JSONResponse, adminapi.ListSuppliers200JSONResponse](answer, err)
}

// SuppliersGet reads one supplier as the admin does.
func (o operationsAPI) SuppliersGet(ctx context.Context, r operationsapi.SuppliersGetRequestObject) (operationsapi.SuppliersGetResponseObject, error) {
	answer, err := o.admin.GetSupplier(ctx, adminapi.GetSupplierRequestObject{SupplierId: r.SupplierId})
	return relayed[operationsapi.SuppliersGet200JSONResponse, adminapi.GetSupplier200JSONResponse](answer, err)
}

// PendingMappingsList lists the upstream names waiting to be mapped as the admin does.
func (o operationsAPI) PendingMappingsList(ctx context.Context, r operationsapi.PendingMappingsListRequestObject) (operationsapi.PendingMappingsListResponseObject, error) {
	answer, err := o.admin.ListPendingMappings(ctx, adminapi.ListPendingMappingsRequestObject{Params: adminapi.ListPendingMappingsParams{
		SupplierId: r.Params.SupplierId, Cursor: r.Params.Cursor, Limit: r.Params.Limit,
	}})
	return relayed[operationsapi.PendingMappingsList200JSONResponse, adminapi.ListPendingMappings200JSONResponse](answer, err)
}

// ModelsList lists the catalog's models as the admin does.
func (o operationsAPI) ModelsList(ctx context.Context, r operationsapi.ModelsListRequestObject) (operationsapi.ModelsListResponseObject, error) {
	answer, err := o.admin.ListModels(ctx, adminapi.ListModelsRequestObject{Params: adminapi.ListModelsParams{
		Status: (*adminapi.ModelStatus)(r.Params.Status), Author: r.Params.Author, SlugPrefix: r.Params.SlugPrefix,
		Cursor: r.Params.Cursor, Limit: r.Params.Limit,
	}})
	return relayed[operationsapi.ModelsList200JSONResponse, adminapi.ListModels200JSONResponse](answer, err)
}

// ModelGet reads one model as the admin does.
func (o operationsAPI) ModelGet(ctx context.Context, r operationsapi.ModelGetRequestObject) (operationsapi.ModelGetResponseObject, error) {
	answer, err := o.admin.GetModel(ctx, adminapi.GetModelRequestObject{ModelId: r.ModelId})
	return relayed[operationsapi.ModelGet200JSONResponse, adminapi.GetModel200JSONResponse](answer, err)
}

// CallsList lists the calls of the credential's sites as the admin's search does; the scope keeps it to them.
func (o operationsAPI) CallsList(ctx context.Context, r operationsapi.CallsListRequestObject) (operationsapi.CallsListResponseObject, error) {
	p := r.Params
	answer, err := o.admin.ListAdminCalls(ctx, adminapi.ListAdminCallsRequestObject{Params: adminapi.ListAdminCallsParams{
		Since: p.Since, Until: p.Until, Cursor: p.Cursor, Limit: p.Limit, AccountId: p.AccountId, RequestId: p.RequestId,
		Model: p.Model, Status: (*adminapi.AdminCallStatus)(p.Status), SiteId: p.SiteId,
	}})
	return relayed[operationsapi.CallsList200JSONResponse, adminapi.ListAdminCalls200JSONResponse](answer, err)
}

// CallGet reads one call's trace as the admin does; a call outside the credential's sites is not found.
func (o operationsAPI) CallGet(ctx context.Context, r operationsapi.CallGetRequestObject) (operationsapi.CallGetResponseObject, error) {
	answer, err := o.admin.GetAdminCallTrace(ctx, adminapi.GetAdminCallTraceRequestObject{CallId: r.CallId})
	return relayed[operationsapi.CallGet200JSONResponse, adminapi.GetAdminCallTrace200JSONResponse](answer, err)
}

// AuditList lists the platform's audit entries as the admin's search does; the guard has required the platform grant.
func (o operationsAPI) AuditList(ctx context.Context, r operationsapi.AuditListRequestObject) (operationsapi.AuditListResponseObject, error) {
	p := r.Params
	answer, err := o.admin.ListAuditEntries(ctx, adminapi.ListAuditEntriesRequestObject{Params: adminapi.ListAuditEntriesParams{
		Since: p.Since, Until: p.Until, Cursor: p.Cursor, Limit: p.Limit, SiteId: p.SiteId, AccountId: p.AccountId,
		RequestId: p.RequestId, ActorKind: p.ActorKind, ActorId: p.ActorId, Action: p.Action, Module: p.Module,
		TargetKind: p.TargetKind, TargetId: p.TargetId,
	}})
	return relayed[operationsapi.AuditList200JSONResponse, adminapi.ListAuditEntries200JSONResponse](answer, err)
}

// AuditDigestsList lists the owner's daily digests newest first; the cursor is the last day of the page before.
func (o operationsAPI) AuditDigestsList(ctx context.Context, r operationsapi.AuditDigestsListRequestObject) (operationsapi.AuditDigestsListResponseObject, error) {
	var before *time.Time
	if r.Params.Cursor != nil {
		day, err := time.Parse(time.DateOnly, *r.Params.Cursor)
		if err != nil {
			return nil, invalidField("cursor", "not a cursor this API gave out")
		}
		before = &day
	}
	var digests []operations.AuditDigest
	var more bool
	if err := o.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		digests, more, err = operations.AuditDigests(ctx, tx, before, pageSize(r.Params.Limit))
		return err
	}); err != nil {
		return nil, err
	}
	list, err := adminJSON[operationsapi.AuditDigestList](map[string]any{"data": digests})
	if err != nil {
		return nil, err
	}
	if more {
		list.NextCursor = new(digests[len(digests)-1].Day)
	}
	return operationsapi.AuditDigestsList200JSONResponse(list), nil
}

// AuditGet reads one audit entry as the admin does.
func (o operationsAPI) AuditGet(ctx context.Context, r operationsapi.AuditGetRequestObject) (operationsapi.AuditGetResponseObject, error) {
	answer, err := o.admin.GetAuditEntry(ctx, adminapi.GetAuditEntryRequestObject{EntryId: r.EntryId})
	return relayed[operationsapi.AuditGet200JSONResponse, adminapi.GetAuditEntry200JSONResponse](answer, err)
}
