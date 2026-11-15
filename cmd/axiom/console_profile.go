package main

import (
	"context"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
)

func (c consoleAPI) ChangeDisplayName(ctx context.Context, r consoleapi.ChangeDisplayNameRequestObject) (consoleapi.ChangeDisplayNameResponseObject, error) {
	if err := c.accounts.ChangeDisplayName(ctx, callOf(ctx).principal, r.Body.Name); err != nil {
		return nil, err
	}
	return consoleapi.ChangeDisplayName204Response{}, nil
}

func (c consoleAPI) RequestEmailChange(ctx context.Context, r consoleapi.RequestEmailChangeRequestObject) (consoleapi.RequestEmailChangeResponseObject, error) {
	if err := c.accounts.RequestEmailChange(ctx, callOf(ctx).principal, r.Body.Email, r.Body.CurrentPassword, languageOf(r.Body.Language)); err != nil {
		return nil, err
	}
	return consoleapi.RequestEmailChange202Response{}, nil
}

func (c consoleAPI) ConfirmEmailChange(ctx context.Context, r consoleapi.ConfirmEmailChangeRequestObject) (consoleapi.ConfirmEmailChangeResponseObject, error) {
	cl := callOf(ctx)
	changed, err := c.sessions().changed(cl, func() (accounts.Issued, []string, error) {
		return c.accounts.ConfirmEmailChange(ctx, cl.principal, accounts.EmailChangeConfirmation{Token: r.Body.Token, ClientIP: cl.ip, UserAgent: cl.r.UserAgent()})
	})
	if err != nil {
		return nil, err
	}
	response := consoleapi.ConfirmEmailChange200JSONResponse{Headers: consoleapi.ConfirmEmailChange200ResponseHeaders{SetCookie: changed.cookie}}
	response.Body.CsrfToken = &changed.csrf
	if changed.codes != nil {
		response.Body.RecoveryCodes = &changed.codes
	}
	return response, nil
}

func (c consoleAPI) GetDisplayName(ctx context.Context, _ consoleapi.GetDisplayNameRequestObject) (consoleapi.GetDisplayNameResponseObject, error) {
	name, err := c.accounts.DisplayName(ctx, callOf(ctx).principal)
	if err != nil {
		return nil, err
	}
	return consoleapi.GetDisplayName200JSONResponse{Name: &name}, nil
}
