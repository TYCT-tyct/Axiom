package main

import (
	"context"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
)

// RequestPasswordReset accepts the same durable work for every email, without looking up an account.
func (c consoleAPI) RequestPasswordReset(ctx context.Context, r consoleapi.RequestPasswordResetRequestObject) (consoleapi.RequestPasswordResetResponseObject, error) {
	cl := callOf(ctx)
	if err := c.accounts.RequestPasswordReset(ctx, accounts.PasswordResetRequest{
		Host: cl.r.Host, Email: r.Body.Email, Language: languageOf(r.Body.Language), ClientIP: cl.ip,
	}); err != nil {
		return nil, err
	}
	return consoleapi.RequestPasswordReset202Response{}, nil
}

// ResetPassword consumes a link without issuing a session; MFA remains required at sign-in.
func (c consoleAPI) ResetPassword(ctx context.Context, r consoleapi.ResetPasswordRequestObject) (consoleapi.ResetPasswordResponseObject, error) {
	cl := callOf(ctx)
	if err := c.accounts.ResetPassword(ctx, cl.r.Host, r.Body.Token, r.Body.NewPassword, languageOf(r.Body.Language), cl.ip); err != nil {
		return nil, err
	}
	return consoleapi.ResetPassword204Response{}, nil
}

// ChangePassword replaces this device's session and returns its optional CSRF token for rolling clients.
func (c consoleAPI) ChangePassword(ctx context.Context, r consoleapi.ChangePasswordRequestObject) (consoleapi.ChangePasswordResponseObject, error) {
	cl := callOf(ctx)
	issued, codes, err := c.accounts.ChangePassword(ctx, cl.principal, accounts.PasswordChange{
		CurrentPassword: r.Body.CurrentPassword, NewPassword: r.Body.NewPassword, Language: languageOf(r.Body.Language),
		ClientIP: cl.ip, UserAgent: cl.r.UserAgent(),
	})
	if err != nil {
		return nil, err
	}
	cookie, err := c.sessions().cookie(cl.r.Host, issued)
	if err != nil {
		return nil, err
	}
	response := consoleapi.ChangePassword200JSONResponse{Headers: consoleapi.ChangePassword200ResponseHeaders{SetCookie: cookie}}
	response.Body.CsrfToken = &issued.CSRFToken
	if codes != nil {
		response.Body.RecoveryCodes = &codes
	}
	return response, nil
}

func languageOf[T ~string](language *T) string {
	if language == nil {
		return ""
	}
	return string(*language)
}
