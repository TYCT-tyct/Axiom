package main

import (
	"context"

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
	if err := c.accounts.ConfirmEmailChange(ctx, callOf(ctx).principal, r.Body.Token); err != nil {
		return nil, err
	}
	return consoleapi.ConfirmEmailChange204Response{}, nil
}

func (c consoleAPI) GetDisplayName(ctx context.Context, _ consoleapi.GetDisplayNameRequestObject) (consoleapi.GetDisplayNameResponseObject, error) {
	name, err := c.accounts.DisplayName(ctx, callOf(ctx).principal)
	if err != nil {
		return nil, err
	}
	return consoleapi.GetDisplayName200JSONResponse{Name: &name}, nil
}
