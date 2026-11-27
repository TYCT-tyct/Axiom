package main

import (
	"context"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers"
)

// The platform's egress proxies and which one a credential goes through (docs/domains/suppliers.md「出口代理」). A
// change that can move a credential's exit rebuilds this node's routing before it answers (leaveRouting); the other
// nodes take it at their next reload.

// ListEgressProxies lists the proxies, without their passwords.
func (a adminAPI) ListEgressProxies(ctx context.Context, _ adminapi.ListEgressProxiesRequestObject) (adminapi.ListEgressProxiesResponseObject, error) {
	var proxies []suppliers.EgressProxy
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		proxies, err = a.suppliers.EgressProxies(ctx, tx)
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.EgressProxyList{Data: make([]adminapi.EgressProxy, len(proxies))}
	for i, p := range proxies {
		list.Data[i] = egressProxyBody(p)
	}
	return adminapi.ListEgressProxies200JSONResponse(list), nil
}

// CreateEgressProxy adds a proxy.
func (a adminAPI) CreateEgressProxy(ctx context.Context, request adminapi.CreateEgressProxyRequestObject) (adminapi.CreateEgressProxyResponseObject, error) {
	b := request.Body
	status := string(adminapi.EgressProxyCreateStatusActive)
	if b.Status != nil {
		status = string(*b.Status)
	}
	spec := suppliers.EgressProxySpec{
		ID: b.Id, Scheme: string(b.Scheme), Host: b.Host, Port: b.Port,
		Set: suppliers.EgressProxyChange{Name: b.Name, FallbackID: textValue(b.FallbackId), Status: status, Auth: proxyAuth(b.Auth)},
	}
	body, replayed, err := idempotently(ctx, a.db, "createEgressProxy", request.Params.IdempotencyKey, []any{b.Id, b.Name, b.Scheme, b.Host, b.Port, proxyAuthGiven(b.Auth), b.FallbackId, status, b.Reason},
		func(tx pg.Tx) (adminapi.EgressProxy, error) {
			p, err := a.suppliers.CreateEgressProxy(ctx, tx, actor(ctx), spec, b.Reason)
			return egressProxyBody(p), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.CreateEgressProxy201JSONResponse{Body: body, Headers: adminapi.CreateEgressProxy201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// UpdateEgressProxy changes a proxy; a status or a fallback moves its credentials' exit, so this node reloads.
func (a adminAPI) UpdateEgressProxy(ctx context.Context, request adminapi.UpdateEgressProxyRequestObject) (adminapi.UpdateEgressProxyResponseObject, error) {
	b := request.Body
	change := suppliers.EgressProxyChange{Name: b.Name, FallbackID: textValue(b.FallbackId), Status: string(b.Status), Auth: proxyAuth(b.Auth)}
	body, replayed, err := idempotently(ctx, a.db, "updateEgressProxy", request.Params.IdempotencyKey, []any{request.ProxyId, b.Name, b.FallbackId, b.Status, proxyAuthGiven(b.Auth), b.Reason},
		func(tx pg.Tx) (adminapi.EgressProxy, error) {
			p, err := a.suppliers.UpdateEgressProxy(ctx, tx, actor(ctx), request.ProxyId, change, b.Reason)
			return egressProxyBody(p), err
		})
	if err != nil {
		return nil, err
	}
	a.leaveRouting(ctx, replayed)
	return adminapi.UpdateEgressProxy200JSONResponse{Body: body, Headers: adminapi.UpdateEgressProxy200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// RemoveEgressProxy removes a proxy nothing uses.
func (a adminAPI) RemoveEgressProxy(ctx context.Context, request adminapi.RemoveEgressProxyRequestObject) (adminapi.RemoveEgressProxyResponseObject, error) {
	body, replayed, err := idempotently(ctx, a.db, "removeEgressProxy", request.Params.IdempotencyKey, []any{request.ProxyId, request.Body},
		func(tx pg.Tx) (adminapi.EgressProxy, error) {
			p, err := a.suppliers.RemoveEgressProxy(ctx, tx, actor(ctx), request.ProxyId, request.Body.Reason)
			return egressProxyBody(p), err
		})
	if err != nil {
		return nil, err
	}
	return adminapi.RemoveEgressProxy200JSONResponse{Body: body, Headers: adminapi.RemoveEgressProxy200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// SetSupplierCredentialEgress sets one credential's exit; routing on this node follows it at once.
func (a adminAPI) SetSupplierCredentialEgress(ctx context.Context, request adminapi.SetSupplierCredentialEgressRequestObject) (adminapi.SetSupplierCredentialEgressResponseObject, error) {
	supplier, credential, err := credentialIDsOf(request.SupplierId, request.CredentialId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	changed, replayed, err := idempotently(ctx, a.db, "setSupplierCredentialEgress", request.Params.IdempotencyKey, []any{request.SupplierId, request.CredentialId, b},
		func(tx pg.Tx) (adminapi.SupplierCredential, error) {
			c, err := a.suppliers.SetCredentialEgress(ctx, tx, actor(ctx), supplier, credential, textValue(b.EgressId), b.Reason)
			return a.credentialBody(c), err
		})
	if err != nil {
		return nil, err
	}
	a.leaveRouting(ctx, replayed)
	return adminapi.SetSupplierCredentialEgress200JSONResponse{Body: changed, Headers: adminapi.SetSupplierCredentialEgress200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// proxyAuth is a request's credentials for a proxy; nil for none.
func proxyAuth(a *adminapi.EgressProxyAuth) *suppliers.ProxyAuth {
	if a == nil {
		return nil
	}
	out := &suppliers.ProxyAuth{Username: a.Username}
	if a.Password != nil {
		out.Password = new(secret.New(*a.Password))
	}
	return out
}

// proxyAuthGiven is what an idempotency fingerprint covers of a proxy's credentials: the username and whether a
// password was given, never a hash of the password (merchantRequest).
func proxyAuthGiven(a *adminapi.EgressProxyAuth) any {
	if a == nil {
		return nil
	}
	return struct {
		Username string
		Password bool
	}{a.Username, a.Password != nil}
}

// egressProxyBody is a proxy as the contract gives it.
func egressProxyBody(p suppliers.EgressProxy) adminapi.EgressProxy {
	return adminapi.EgressProxy{
		Id: p.ID, Name: p.Name, Scheme: adminapi.EgressProxyScheme(p.Scheme), Host: p.Host, Port: p.Port,
		Username: text(p.Username), HasPassword: p.HasPassword, FallbackId: text(p.FallbackID), Status: adminapi.EgressProxyStatus(p.Status),
	}
}
