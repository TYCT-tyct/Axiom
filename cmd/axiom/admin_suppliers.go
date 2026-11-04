package main

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// supplierIDOf reads a supplier's, group's or other ID from a path or a body. A malformed ID and an unknown one are the
// same 404 in a path.
func supplierIDOf(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil || id.String() != s {
		return uuid.UUID{}, suppliers.ErrNoSupplier
	}
	return id, nil
}

// idIn reads an ID that is in a body: one that is none cannot be a resource that is not there, so it is a 400.
func idIn(pointer, s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil || id.String() != s {
		return uuid.UUID{}, invalidField(pointer, "not an ID of this API")
	}
	return id, nil
}

// decimal is a number the supplier module holds as text, or absent when it has none.
func decimal(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func text(s string) *string { return decimal(s) }

func keyState(s string) *adminapi.SupplierGroupKeyState {
	if s == "" {
		return nil
	}
	v := adminapi.SupplierGroupKeyState(s)
	return &v
}

func keyReason(s string) *adminapi.SupplierGroupKeyReason {
	if s == "" {
		return nil
	}
	v := adminapi.SupplierGroupKeyReason(s)
	return &v
}

// supplierBody is a supplier as the contract gives it.
func supplierBody(s suppliers.Supplier) adminapi.Supplier {
	return adminapi.Supplier{
		Id: s.ID.String(), Name: s.Name, Kind: s.Kind, Origin: s.Origin, UpstreamAccount: s.UpstreamAccount,
		Generation: text(s.Generation), DetectedVersion: text(s.DetectedVersion),
		NativeUnit: adminapi.SupplierNativeUnit(s.NativeUnit), SettlementCurrency: s.SettlementCurrency,
		Status: adminapi.SupplierStatus(s.Status), StatusReason: text(s.StatusReason),
		BalanceNative: decimal(s.BalanceNative), BalanceObservedAt: s.BalanceObservedAt,
		LowBalanceThreshold: decimal(s.LowBalanceThreshold), UnitValue: decimal(s.UnitValue),
	}
}

func groupBody(g suppliers.Group) adminapi.SupplierGroup {
	body := adminapi.SupplierGroup{
		Id: g.ID.String(), UpstreamRef: g.UpstreamRef, Name: g.Name, Ratio: g.Ratio, Selected: g.Selected, Present: g.Present,
		Codename: text(g.Codename), KeyState: keyState(g.KeyState), KeyReason: keyReason(g.KeyReason),
	}
	if len(g.Facts) > 0 {
		body.Facts = &g.Facts
	}
	if g.Pool != nil {
		body.Pool = &adminapi.SupplierPool{
			Accounts: g.Pool.Accounts, Failed: g.Pool.Failed, Usable: g.Pool.Usable,
			Headroom: g.Pool.Headroom, Capacity: g.Pool.Capacity, Low: g.Pool.Low, ObservedAt: g.Pool.ObservedAt,
		}
	}
	return body
}

func credentialBody(c suppliers.Credential) adminapi.SupplierCredential {
	return adminapi.SupplierCredential{
		Id: c.ID.String(), GroupId: c.Group.String(), UpstreamKeyRef: c.UpstreamKeyRef, Status: adminapi.SupplierCredentialStatus(c.Status),
		CooldownUntil: c.CooldownUntil, ExpiresAt: c.ExpiresAt, MaxConcurrency: c.MaxConcurrency,
		RequestsPerMinute: c.RequestsPerMinute,
	}
}

// cursorOf reads the cursor of a list of suppliers: the last supplier's ID.
func supplierCursor(cursor *string) (uuid.UUID, error) {
	if cursor == nil {
		return uuid.UUID{}, nil
	}
	return idIn("cursor", *cursor)
}

// ListSuppliers lists suppliers, newest first, a page at a time.
func (a adminAPI) ListSuppliers(ctx context.Context, request adminapi.ListSuppliersRequestObject) (adminapi.ListSuppliersResponseObject, error) {
	q := suppliers.SupplierQuery{PageSize: pageSize(request.Params.Limit)}
	if request.Params.Status != nil {
		q.Status = suppliers.Status(*request.Params.Status)
	}
	var err error
	if q.After, err = supplierCursor(request.Params.Cursor); err != nil {
		return nil, err
	}
	var page []suppliers.Supplier
	var more bool
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		page, more, err = a.suppliers.Suppliers(ctx, tx, q)
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.SupplierList{Data: make([]adminapi.Supplier, len(page))}
	for i, s := range page {
		list.Data[i] = supplierBody(s)
	}
	if more {
		next := page[len(page)-1].ID.String()
		list.NextCursor = &next
	}
	return adminapi.ListSuppliers200JSONResponse(list), nil
}

// GetSupplier reads one supplier with its groups, the keys bound to them and its endpoints, as the sync last stored them.
// The keys the supplier lists that are bound to none are the sync's to say, and are not stored yet.
func (a adminAPI) GetSupplier(ctx context.Context, request adminapi.GetSupplierRequestObject) (adminapi.GetSupplierResponseObject, error) {
	id, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	var detail suppliers.Detail
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		detail, err = a.suppliers.Supplier(ctx, tx, id)
		return err
	}); err != nil {
		return nil, err
	}
	body := adminapi.SupplierDetail{
		Supplier: supplierBody(detail.Supplier), Groups: make([]adminapi.SupplierGroup, len(detail.Groups)),
		Credentials: make([]adminapi.SupplierCredential, len(detail.Credentials)), UnboundKeys: []adminapi.UpstreamKey{},
		Endpoints: make([]adminapi.SupplierEndpoint, len(detail.Endpoints)),
	}
	for i, g := range detail.Groups {
		body.Groups[i] = groupBody(g)
	}
	for i, c := range detail.Credentials {
		body.Credentials[i] = credentialBody(c)
	}
	for i, e := range detail.Endpoints {
		body.Endpoints[i] = endpointBody(e)
	}
	return adminapi.GetSupplier200JSONResponse(body), nil
}

// credentialsOf is the login a request gives.
func credentialsOf(l adminapi.SupplierLogin) suppliers.Credentials {
	var c suppliers.Credentials
	if l.Email != nil {
		c.Email = *l.Email
	}
	if l.Password != nil {
		c.Password = *l.Password
	}
	if l.AccessToken != nil {
		c.AccessToken = *l.AccessToken
	}
	if l.RefreshToken != nil {
		c.RefreshToken = *l.RefreshToken
	}
	if l.AccessTokenExpiresAt != nil {
		c.AccessTokenExpiresAt = l.AccessTokenExpiresAt.UTC()
	}
	return c
}

// loginMembers are the names of the members of a login that are given, without their values: what a request is
// compared by when it carries a secret, so that the fingerprint kept with an Idempotency-Key cannot be used to guess
// one.
func loginMembers(c suppliers.Credentials) []string {
	names := make([]string, 0, 5)
	for _, field := range c.Present() {
		names = append(names, string(field))
	}
	return names
}

// DetectSupplier finds out what system a site runs; 10 a minute per session, because each probe is a request to a
// third party.
func (a adminAPI) DetectSupplier(ctx context.Context, request adminapi.DetectSupplierRequestObject) (adminapi.DetectSupplierResponseObject, error) {
	if err := a.detections.allow(uuid.UUID(callOf(ctx).principal.SessionID())); err != nil {
		return nil, err
	}
	found, err := a.suppliers.Detect(ctx, request.Body.Origin)
	if err != nil {
		return nil, err
	}
	body := adminapi.Detection{Origin: found.Origin, LoginFields: make([]adminapi.LoginField, len(found.LoginFields))}
	for i, f := range found.LoginFields {
		body.LoginFields[i] = adminapi.LoginField(f)
	}
	if found.Kind != "" {
		body.Kind, body.Generation, body.DetectedVersion = &found.Kind, text(found.Generation), text(found.Version)
	}
	return adminapi.DetectSupplier200JSONResponse(body), nil
}

// windowLimiter lets n requests through per key in each window, in this node's memory: the limit on detectSupplier
// is a guard against a runaway page, and a node's own count is enough for it.
type windowLimiter struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	now    func() time.Time
	hits   map[uuid.UUID][]time.Time
}

func newWindowLimiter(n int, window time.Duration, now func() time.Time) *windowLimiter {
	return &windowLimiter{n: n, window: window, now: now, hits: map[uuid.UUID][]time.Time{}}
}

// allow counts a request of key, or says how long until one would be let through.
func (l *windowLimiter) allow(key uuid.UUID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	recent := l.hits[key][:0:0]
	for _, at := range l.hits[key] {
		if now.Sub(at) < l.window {
			recent = append(recent, at)
		}
	}
	if len(recent) >= l.n {
		l.hits[key] = recent
		return &accounts.TooManyAttempts{RetryAfter: recent[0].Add(l.window).Sub(now)}
	}
	l.hits[key] = append(recent, now)
	if len(l.hits) > 1024 { // sessions that came and went leave their keys
		l.sweep(now)
	}
	return nil
}

func (l *windowLimiter) sweep(now time.Time) {
	for key, hits := range l.hits {
		if len(hits) == 0 || now.Sub(hits[len(hits)-1]) >= l.window {
			delete(l.hits, key)
		}
	}
}

// supplierProblem is the part of a refusal that the supplier module can name, as the answer the contract gives it.
func supplierProblem(err error) error {
	switch loginErr, ok := errors.AsType[*suppliers.LoginError](err); {
	case ok:
		return invalidField("/login/"+loginErr.Field, loginErr.Reason)
	case errors.Is(err, suppliers.ErrBadOrigin):
		return invalidField("/origin", "must be https://host or https://host:port")
	}
	return err
}

// CreateSupplier connects a supplier. The sync logs in and moves it on from `connecting`.
func (a adminAPI) CreateSupplier(ctx context.Context, request adminapi.CreateSupplierRequestObject) (adminapi.CreateSupplierResponseObject, error) {
	b := request.Body
	origin, err := suppliers.NormalizeOrigin(b.Origin)
	if err != nil {
		return nil, supplierProblem(err)
	}
	currency, err := platform.ParseCurrency(b.SettlementCurrency)
	if err != nil {
		return nil, invalidField("/settlement_currency", "not a currency code")
	}
	if err := a.checkSettlementCurrency(currency); err != nil {
		return nil, err
	}
	login := credentialsOf(b.Login)
	// The login is not in the fingerprint: it would let the fingerprint be used to guess a password. Its members' names are.
	identity := []any{b.Name, b.Kind, origin, b.UpstreamAccount, b.SettlementCurrency, b.Reason, loginMembers(login)}
	supplier, replayed, err := idempotently(ctx, a.db, "createSupplier", request.Params.IdempotencyKey, identity,
		func(tx pg.Tx) (adminapi.Supplier, error) {
			// Asked only by the request that makes the change: the network may differ when a lost response is retried.
			if err := a.suppliers.CheckOrigin(ctx, origin); err != nil {
				return adminapi.Supplier{}, supplierProblem(err)
			}
			made, err := a.suppliers.Connect(ctx, tx, actor(ctx), suppliers.ConnectRequest{
				Name: b.Name, Kind: b.Kind, Origin: origin, UpstreamAccount: b.UpstreamAccount, SettlementCurrency: currency,
				Login: login, Reason: b.Reason,
			})
			return supplierBody(made), supplierProblem(err)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.CreateSupplier201JSONResponse{Body: supplier, Headers: adminapi.CreateSupplier201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// SetSupplierStatus pauses a supplier or resumes it.
func (a adminAPI) SetSupplierStatus(ctx context.Context, request adminapi.SetSupplierStatusRequestObject) (adminapi.SetSupplierStatusResponseObject, error) {
	id, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	supplier, replayed, err := idempotently(ctx, a.db, "setSupplierStatus", request.Params.IdempotencyKey, []any{request.SupplierId, b},
		func(tx pg.Tx) (adminapi.Supplier, error) {
			changed, err := a.suppliers.SetStatus(ctx, tx, actor(ctx), id, suppliers.Status(b.Status), b.Reason)
			return supplierBody(changed), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.SetSupplierStatus200JSONResponse{Body: supplier, Headers: adminapi.SetSupplierStatus200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// SetSupplierEndpointQuantization records the precision an endpoint serves at, or clears it.
func (a adminAPI) SetSupplierEndpointQuantization(ctx context.Context, request adminapi.SetSupplierEndpointQuantizationRequestObject) (adminapi.SetSupplierEndpointQuantizationResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	endpoint, err := supplierIDOf(request.EndpointId)
	if err != nil {
		return nil, suppliers.ErrNoSupplierEndpoint
	}
	var level *suppliers.Quantization
	if request.Body.Quantization != nil {
		q := suppliers.Quantization(*request.Body.Quantization)
		level = &q
	}
	changed, replayed, err := idempotently(ctx, a.db, "setSupplierEndpointQuantization", request.Params.IdempotencyKey,
		[]any{request.SupplierId, request.EndpointId, request.Body},
		func(tx pg.Tx) (adminapi.SupplierEndpoint, error) {
			e, err := a.suppliers.SetEndpointQuantization(ctx, tx, actor(ctx), supplier, endpoint, level, request.Body.Reason)
			return endpointBody(e), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.SetSupplierEndpointQuantization200JSONResponse{Body: changed, Headers: adminapi.SetSupplierEndpointQuantization200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// RemoveSupplier removes a supplier: out of routing, its login deleted and its keys erased, in one transaction.
func (a adminAPI) RemoveSupplier(ctx context.Context, request adminapi.RemoveSupplierRequestObject) (adminapi.RemoveSupplierResponseObject, error) {
	id, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	supplier, replayed, err := idempotently(ctx, a.db, "removeSupplier", request.Params.IdempotencyKey, []any{request.SupplierId, request.Body},
		func(tx pg.Tx) (adminapi.Supplier, error) {
			removed, err := a.suppliers.Remove(ctx, tx, actor(ctx), id, request.Body.Reason)
			return supplierBody(removed), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.RemoveSupplier200JSONResponse{Body: supplier, Headers: adminapi.RemoveSupplier200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// ReplaceSupplierLogin seals a new login over the old one; the supplier is `connecting` again.
func (a adminAPI) ReplaceSupplierLogin(ctx context.Context, request adminapi.ReplaceSupplierLoginRequestObject) (adminapi.ReplaceSupplierLoginResponseObject, error) {
	id, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	login := credentialsOf(request.Body.Login)
	identity := []any{request.SupplierId, request.Body.Reason, loginMembers(login)}
	supplier, replayed, err := idempotently(ctx, a.db, "replaceSupplierLogin", request.Params.IdempotencyKey, identity,
		func(tx pg.Tx) (adminapi.Supplier, error) {
			changed, err := a.suppliers.ReplaceLogin(ctx, tx, actor(ctx), id, login, request.Body.Reason)
			return supplierBody(changed), supplierProblem(err)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.ReplaceSupplierLogin200JSONResponse{Body: supplier, Headers: adminapi.ReplaceSupplierLogin200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// ConfirmSupplierUnitValue records what one unit of the supplier's balance is worth.
func (a adminAPI) ConfirmSupplierUnitValue(ctx context.Context, request adminapi.ConfirmSupplierUnitValueRequestObject) (adminapi.ConfirmSupplierUnitValueResponseObject, error) {
	id, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	supplier, replayed, err := idempotently(ctx, a.db, "confirmSupplierUnitValue", request.Params.IdempotencyKey, []any{request.SupplierId, b},
		func(tx pg.Tx) (adminapi.Supplier, error) {
			changed, err := a.suppliers.ConfirmUnitValue(ctx, tx, actor(ctx), id, b.Value, b.Reason)
			return supplierBody(changed), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.ConfirmSupplierUnitValue200JSONResponse{Body: supplier, Headers: adminapi.ConfirmSupplierUnitValue200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// SelectSupplierGroup chooses whether a supplier's group is used.
func (a adminAPI) SelectSupplierGroup(ctx context.Context, request adminapi.SelectSupplierGroupRequestObject) (adminapi.SelectSupplierGroupResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	group, err := supplierIDOf(request.GroupId)
	if err != nil {
		return nil, suppliers.ErrNoGroup
	}
	b := request.Body
	chosen, replayed, err := idempotently(ctx, a.db, "selectSupplierGroup", request.Params.IdempotencyKey, []any{request.SupplierId, request.GroupId, b},
		func(tx pg.Tx) (adminapi.SupplierGroup, error) {
			g, err := a.suppliers.SelectGroup(ctx, tx, actor(ctx), supplier, group, b.Selected, b.Reason)
			return groupBody(g), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.SelectSupplierGroup200JSONResponse{Body: chosen, Headers: adminapi.SelectSupplierGroup200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// bodyGroup is a group a body names, which the supplier lacks: a rule of the request, not a missing resource.
func bodyGroup(err error) error {
	if errors.Is(err, suppliers.ErrNoGroup) {
		return unprocessable("the group is not one of the supplier's")
	}
	return err
}

// BindSupplierKey binds one of the supplier's keys to a selected group.
func (a adminAPI) BindSupplierKey(ctx context.Context, request adminapi.BindSupplierKeyRequestObject) (adminapi.BindSupplierKeyResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	group, err := idIn("/group_id", b.GroupId)
	if err != nil {
		return nil, err
	}
	bound, replayed, err := idempotently(ctx, a.db, "bindSupplierKey", request.Params.IdempotencyKey, []any{request.SupplierId, b},
		func(tx pg.Tx) (adminapi.SupplierCredential, error) {
			c, err := a.suppliers.BindKey(ctx, tx, actor(ctx), supplier, group, b.UpstreamKeyRef, b.Reason)
			return credentialBody(c), bodyGroup(err)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.BindSupplierKey201JSONResponse{Body: bound, Headers: adminapi.BindSupplierKey201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// SetSupplierCredentialLimits sets the calls a credential serves at once and the requests its minute admits.
func (a adminAPI) SetSupplierCredentialLimits(ctx context.Context, request adminapi.SetSupplierCredentialLimitsRequestObject) (adminapi.SetSupplierCredentialLimitsResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	credential, err := supplierIDOf(request.CredentialId)
	if err != nil {
		return nil, suppliers.ErrNoCredential
	}
	b := request.Body
	limits := suppliers.Limits{MaxConcurrency: b.MaxConcurrency, RequestsPerMinute: b.RequestsPerMinute}
	changed, replayed, err := idempotently(ctx, a.db, "setSupplierCredentialLimits", request.Params.IdempotencyKey, []any{request.SupplierId, request.CredentialId, b},
		func(tx pg.Tx) (adminapi.SupplierCredential, error) {
			c, err := a.suppliers.SetLimits(ctx, tx, actor(ctx), supplier, credential, limits, b.Reason)
			return credentialBody(c), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.SetSupplierCredentialLimits200JSONResponse{Body: changed, Headers: adminapi.SetSupplierCredentialLimits200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// pendingCursor is where a page of the queue of names to map ended, in the cursor's own form.
type pendingCursor struct {
	Seen  time.Time `json:"s"`
	Group uuid.UUID `json:"g"`
	Name  string    `json:"n"`
}

func pendingAfter(cursor *string) (*suppliers.PendingMapping, error) {
	if cursor == nil {
		return nil, nil //nolint:nilnil // no cursor is the first page
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*cursor)
	var c pendingCursor
	if err != nil || jsonx.Unmarshal(decoded, &c) != nil || c.Name == "" {
		return nil, invalidField("cursor", "not a cursor this API gave out")
	}
	return &suppliers.PendingMapping{FirstSeen: c.Seen, Group: c.Group, UpstreamName: c.Name}, nil
}

// ListPendingMappings lists the upstream names no model is mapped to, oldest first, a page at a time.
func (a adminAPI) ListPendingMappings(ctx context.Context, request adminapi.ListPendingMappingsRequestObject) (adminapi.ListPendingMappingsResponseObject, error) {
	p := request.Params
	q := suppliers.PendingQuery{PageSize: pageSize(p.Limit)}
	var err error
	if p.SupplierId != nil {
		if q.Supplier, err = idIn("supplier_id", *p.SupplierId); err != nil {
			return nil, err
		}
	}
	if q.After, err = pendingAfter(p.Cursor); err != nil {
		return nil, err
	}
	var names []suppliers.PendingMapping
	var more bool
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		names, more, err = a.suppliers.PendingMappings(ctx, tx, q)
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.PendingMappingList{Data: make([]adminapi.PendingMapping, len(names))}
	for i, n := range names {
		list.Data[i] = adminapi.PendingMapping{
			SupplierId: n.Supplier.String(), GroupId: n.Group.String(), GroupCodename: text(n.GroupCodename), UpstreamName: n.UpstreamName,
			Formats: modelFormats(n.Formats),
		}
	}
	if more {
		last := names[len(names)-1]
		encoded, err := jsonx.MarshalCanonical(pendingCursor{last.FirstSeen, last.Group, last.UpstreamName})
		if err != nil {
			return nil, err
		}
		next := base64.RawURLEncoding.EncodeToString(encoded)
		list.NextCursor = &next
	}
	return adminapi.ListPendingMappings200JSONResponse(list), nil
}

// MapSupplierModel makes an alias: the supplier's name means this model, in one group or in the whole supplier.
// mappableModel refuses a model the catalog lacks or has retired: an alias to it would send traffic nowhere.
func mappableModel(ctx context.Context, tx pg.Tx, id uuid.UUID) error {
	view, err := catalog.ModelIn(ctx, tx, catalog.ModelID(id))
	switch {
	case errors.Is(err, catalog.ErrModelNotFound):
		return unprocessable("the model is not in the catalog")
	case err != nil:
		return err
	case view.Status == catalog.StatusRetired:
		return unprocessable("the model is retired")
	}
	return nil
}

// checkSettlementCurrency refuses a currency the supplier's cost could never be converted from: the site's own, or one
// the exchange rates cover with it (billing.ExchangePair). Any other would put every attempt on the supplier at a missing
// rate, with a zero cost and an unknown profit and no rate that could be set to fix it.
func (a adminAPI) checkSettlementCurrency(currency platform.Currency) error {
	site, ok := a.accounts.MainSite()
	if !ok {
		return accounts.ErrNotFound
	}
	if currency == site.Currency {
		return nil
	}
	if _, _, err := billing.ExchangePair(currency.String(), site.Currency.String()); err != nil {
		return unprocessableFields("/settlement_currency", "must be the site's currency or one that has exchange rates with it")
	}
	return nil
}
