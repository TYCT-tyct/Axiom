package main

import (
	"context"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
)

// setCookie is the Set-Cookie header that gives the browser the new session.
func (c consoleAPI) setCookie(host string, issued accounts.Issued) (string, error) {
	cookie, err := c.accounts.Cookie(host, issued)
	if err != nil {
		return "", err
	}
	return cookie.String(), nil
}

// grant is what a sign-in or a verification tells the page of the session it set.
func grant(issued accounts.Issued) consoleapi.SessionGrant {
	status := consoleapi.SessionGrantStatusSignedIn
	if issued.Pending {
		status = consoleapi.SessionGrantStatusMfaRequired
	}
	return consoleapi.SessionGrant{Status: status, CsrfToken: issued.CSRFToken}
}

// GetSite tells the sign-in and registration pages whose they are.
func (consoleAPI) GetSite(ctx context.Context, _ consoleapi.GetSiteRequestObject) (consoleapi.GetSiteResponseObject, error) {
	site := callOf(ctx).site
	return consoleapi.GetSite200JSONResponse(consoleapi.Site{
		Name: site.Name, Currency: site.Currency.String(), KeyPrefix: site.KeyPrefix,
		Registration: consoleapi.SiteRegistration(site.Registration),
	}), nil
}

// Register makes a user from an invite code.
func (c consoleAPI) Register(ctx context.Context, request consoleapi.RegisterRequestObject) (consoleapi.RegisterResponseObject, error) {
	cl := callOf(ctx)
	user, err := c.accounts.Register(ctx, accounts.RegisterRequest{
		Host: cl.r.Host, InviteCode: request.Body.InviteCode, Email: request.Body.Email, Password: request.Body.Password, ClientIP: cl.ip,
	})
	if err != nil {
		return nil, err
	}
	return consoleapi.Register201JSONResponse{UserId: user.TypeID()}, nil
}

// SignIn checks the password and sets a session, a pending one when the user has two-step verification.
func (c consoleAPI) SignIn(ctx context.Context, request consoleapi.SignInRequestObject) (consoleapi.SignInResponseObject, error) {
	cl := callOf(ctx)
	issued, err := c.accounts.SignIn(ctx, accounts.SignInRequest{
		Host: cl.r.Host, Email: request.Body.Email, Password: request.Body.Password, ClientIP: cl.ip, UserAgent: cl.r.UserAgent(),
	})
	if err != nil {
		return nil, err
	}
	cookie, err := c.setCookie(cl.r.Host, issued)
	if err != nil {
		return nil, err
	}
	return consoleapi.SignIn200JSONResponse{Body: grant(issued), Headers: consoleapi.SignIn200ResponseHeaders{SetCookie: cookie}}, nil
}

// VerifyMfa turns the pending session into a verified one.
func (c consoleAPI) VerifyMfa(ctx context.Context, request consoleapi.VerifyMfaRequestObject) (consoleapi.VerifyMfaResponseObject, error) {
	cl := callOf(ctx)
	issued, err := c.accounts.VerifyMFA(ctx, cl.r, request.Body.Code, cl.ip, cl.r.UserAgent())
	if err != nil {
		return nil, err
	}
	cookie, err := c.setCookie(cl.r.Host, issued)
	if err != nil {
		return nil, err
	}
	return consoleapi.VerifyMfa200JSONResponse{Body: grant(issued), Headers: consoleapi.VerifyMfa200ResponseHeaders{SetCookie: cookie}}, nil
}

// SignOut ends the session of the cookie, whatever its level, and expires the cookie.
func (c consoleAPI) SignOut(ctx context.Context, _ consoleapi.SignOutRequestObject) (consoleapi.SignOutResponseObject, error) {
	cl := callOf(ctx)
	if err := c.accounts.SignOut(ctx, cl.principal); err != nil {
		return nil, err
	}
	cookie, err := c.accounts.ExpiredCookie(cl.r.Host)
	if err != nil {
		return nil, err
	}
	return consoleapi.SignOut204Response{Headers: consoleapi.SignOut204ResponseHeaders{SetCookie: cookie.String()}}, nil
}

// GetSession tells a page who is signed in, what the session may do, and its CSRF token.
func (c consoleAPI) GetSession(ctx context.Context, _ consoleapi.GetSessionRequestObject) (consoleapi.GetSessionResponseObject, error) {
	info, err := c.accounts.SessionInfo(ctx, callOf(ctx).principal)
	if err != nil {
		return nil, err
	}
	session := consoleapi.Session{
		Account: info.Account.TypeID(), CsrfToken: info.CSRFToken, ExpiresAt: info.ExpiresAt,
		Level: consoleapi.SessionLevel(info.Level), MfaEnabled: info.MFAEnabled, Permissions: info.Permissions,
	}
	session.User.Id, session.User.Email, session.User.InviteCode = info.User.TypeID(), info.Email, info.InviteCode
	return consoleapi.GetSession200JSONResponse(session), nil
}

// BeginTotpSetup makes a TOTP secret; nothing is kept until it is confirmed.
func (c consoleAPI) BeginTotpSetup(ctx context.Context, _ consoleapi.BeginTotpSetupRequestObject) (consoleapi.BeginTotpSetupResponseObject, error) {
	secret, uri := c.accounts.BeginTOTP(callOf(ctx).principal)
	return consoleapi.BeginTotpSetup200JSONResponse{Secret: secret, Uri: uri}, nil
}

// ConfirmTotpSetup turns on two-step verification with the secret the user proved.
func (c consoleAPI) ConfirmTotpSetup(ctx context.Context, request consoleapi.ConfirmTotpSetupRequestObject) (consoleapi.ConfirmTotpSetupResponseObject, error) {
	cl := callOf(ctx)
	issued, codes, err := c.accounts.ConfirmTOTP(ctx, cl.principal, accounts.FactorChange{
		Secret: request.Body.Secret, Password: request.Body.Password, Code: request.Body.Code, ClientIP: cl.ip, UserAgent: cl.r.UserAgent(),
	})
	if err != nil {
		return nil, err
	}
	cookie, err := c.setCookie(cl.r.Host, issued)
	if err != nil {
		return nil, err
	}
	body := consoleapi.FactorChange{CsrfToken: issued.CSRFToken, RecoveryCodes: codes}
	return consoleapi.ConfirmTotpSetup200JSONResponse{Body: body, Headers: consoleapi.ConfirmTotpSetup200ResponseHeaders{SetCookie: cookie}}, nil
}

// RegenerateRecoveryCodes replaces the recovery codes; the old ones and the user's other sessions end.
func (c consoleAPI) RegenerateRecoveryCodes(ctx context.Context, request consoleapi.RegenerateRecoveryCodesRequestObject) (consoleapi.RegenerateRecoveryCodesResponseObject, error) {
	cl := callOf(ctx)
	issued, codes, err := c.accounts.RegenerateRecoveryCodes(ctx, cl.principal, accounts.FactorChange{
		Code: request.Body.Code, ClientIP: cl.ip, UserAgent: cl.r.UserAgent(),
	})
	if err != nil {
		return nil, err
	}
	cookie, err := c.setCookie(cl.r.Host, issued)
	if err != nil {
		return nil, err
	}
	body := consoleapi.FactorChange{CsrfToken: issued.CSRFToken, RecoveryCodes: codes}
	return consoleapi.RegenerateRecoveryCodes200JSONResponse{Body: body, Headers: consoleapi.RegenerateRecoveryCodes200ResponseHeaders{SetCookie: cookie}}, nil
}
