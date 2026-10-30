package main

import (
	"context"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// signInView is a site's sign-in configuration as the contract writes it.
func signInView(v accounts.SiteSignIn) adminapi.SiteSignIn {
	out := adminapi.SiteSignIn{Passkeys: v.Passkeys}
	if v.LoginDomain != "" {
		out.LoginDomain = &v.LoginDomain
	}
	for _, provider := range v.Providers {
		entry := adminapi.SiteOAuthProvider{Provider: adminapi.OAuthProvider(provider.Provider), Configured: provider.Configured}
		if provider.ClientID != "" {
			entry.ClientId = &provider.ClientID
		}
		out.Providers = append(out.Providers, entry)
	}
	return out
}

// GetSiteSignIn tells a site's login domain, passkey switch and provider applications.
func (a adminAPI) GetSiteSignIn(ctx context.Context, request adminapi.GetSiteSignInRequestObject) (adminapi.GetSiteSignInResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	var view accounts.SiteSignIn
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		view, err = a.accounts.SiteSignIn(ctx, tx, id)
		return err
	}); err != nil {
		return nil, err
	}
	return adminapi.GetSiteSignIn200JSONResponse(signInView(view)), nil
}

// UpdateSiteSignIn sets the login domain and passkey switch. A domain change after sign-in assets exist is refused.
func (a adminAPI) UpdateSiteSignIn(ctx context.Context, request adminapi.UpdateSiteSignInRequestObject) (adminapi.UpdateSiteSignInResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	update := accounts.SiteSignInUpdate{LoginDomain: request.Body.LoginDomain, Passkeys: request.Body.Passkeys}
	view, replayed, err := idempotently(ctx, a.db, "updateSiteSignIn", request.Params.IdempotencyKey, []any{request.SiteId, request.Body},
		func(tx pg.Tx) (adminapi.SiteSignIn, error) {
			if err := a.accounts.SetSiteSignIn(ctx, tx, actor(ctx), id, update, request.Body.Reason); err != nil {
				return adminapi.SiteSignIn{}, err
			}
			stored, err := a.accounts.SiteSignIn(ctx, tx, id)
			return signInView(stored), err
		})
	if err != nil {
		return nil, err
	}
	if !replayed {
		a.reloadSignIn(ctx, request.SiteId)
	}
	return adminapi.UpdateSiteSignIn200JSONResponse{
		Body: view, Headers: adminapi.UpdateSiteSignIn200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

// SetSiteOAuthProvider stores a provider application, sealed, and returns the site's settings without it.
func (a adminAPI) SetSiteOAuthProvider(ctx context.Context, request adminapi.SetSiteOAuthProviderRequestObject) (adminapi.SetSiteOAuthProviderResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	provider := string(request.Provider)
	// The secret stays out of the persisted, unkeyed fingerprint: whether one was sent is enough to tell a replay
	// from a request that clears it.
	identity := []any{request.SiteId, request.Provider, request.Body.ClientId, request.Body.Reason, request.Body.ClientSecret != ""}
	view, replayed, err := idempotently(ctx, a.db, "setSiteOAuthProvider", request.Params.IdempotencyKey, identity,
		func(tx pg.Tx) (adminapi.SiteSignIn, error) {
			if err := a.accounts.SetSiteOAuthProvider(ctx, tx, actor(ctx), id, provider, request.Body.ClientId, request.Body.ClientSecret, request.Body.Reason); err != nil {
				return adminapi.SiteSignIn{}, err
			}
			stored, err := a.accounts.SiteSignIn(ctx, tx, id)
			return signInView(stored), err
		})
	if err != nil {
		return nil, err
	}
	if !replayed {
		a.reloadSignIn(ctx, request.SiteId)
	}
	return adminapi.SetSiteOAuthProvider200JSONResponse{
		Body: view, Headers: adminapi.SetSiteOAuthProvider200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

// DeleteSiteOAuthProvider turns a provider off and returns the site's settings.
func (a adminAPI) DeleteSiteOAuthProvider(ctx context.Context, request adminapi.DeleteSiteOAuthProviderRequestObject) (adminapi.DeleteSiteOAuthProviderResponseObject, error) {
	id, _, err := a.siteOf(request.SiteId)
	if err != nil {
		return nil, err
	}
	provider := string(request.Provider)
	view, replayed, err := idempotently(ctx, a.db, "deleteSiteOAuthProvider", request.Params.IdempotencyKey, []any{request.SiteId, request.Provider, request.Body},
		func(tx pg.Tx) (adminapi.SiteSignIn, error) {
			if err := a.accounts.DeleteSiteOAuthProvider(ctx, tx, actor(ctx), id, provider, request.Body.Reason); err != nil {
				return adminapi.SiteSignIn{}, err
			}
			stored, err := a.accounts.SiteSignIn(ctx, tx, id)
			return signInView(stored), err
		})
	if err != nil {
		return nil, err
	}
	if !replayed {
		a.reloadSignIn(ctx, request.SiteId)
	}
	return adminapi.DeleteSiteOAuthProvider201JSONResponse{
		Body: view, Headers: adminapi.DeleteSiteOAuthProvider201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

// reloadSignIn reloads this node's sites after a sign-in setting changed. A failure is not the change's: the write is
// done, and the next reload tries again.
func (a adminAPI) reloadSignIn(ctx context.Context, site string) {
	if err := a.accounts.Refresh(context.WithoutCancel(ctx)); err != nil {
		a.logger.WarnContext(ctx, "sign-in settings saved but this node did not reload them", "site", site, "err", err)
	}
}
