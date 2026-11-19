package main

import (
	"context"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
)

func (c adminAPI) sessions() sessionOps { return sessionOps{c.accounts} }

// adminGrant is what a sign-in or a verification tells the page of the session it set.
func adminGrant(issued accounts.Issued) adminapi.SessionGrant {
	status := adminapi.SessionGrantStatusSignedIn
	if issued.Pending {
		status = adminapi.SessionGrantStatusMfaRequired
	}
	return adminapi.SessionGrant{Status: status, CsrfToken: issued.CSRFToken}
}

// SignIn checks the password and sets a session, a pending one when the user has two-step verification.
func (c adminAPI) SignIn(ctx context.Context, request adminapi.SignInRequestObject) (adminapi.SignInResponseObject, error) {
	issued, cookie, err := c.sessions().signIn(ctx, callOf(ctx), request.Body.Email, request.Body.Password)
	if err != nil {
		return nil, err
	}
	return adminapi.SignIn200JSONResponse{Body: adminGrant(issued), Headers: adminapi.SignIn200ResponseHeaders{SetCookie: cookie}}, nil
}

// VerifyMfa turns the pending session into a verified one.
func (c adminAPI) VerifyMfa(ctx context.Context, request adminapi.VerifyMfaRequestObject) (adminapi.VerifyMfaResponseObject, error) {
	issued, cookie, err := c.sessions().verify(ctx, callOf(ctx), request.Body.Code)
	if err != nil {
		return nil, err
	}
	return adminapi.VerifyMfa200JSONResponse{Body: adminGrant(issued), Headers: adminapi.VerifyMfa200ResponseHeaders{SetCookie: cookie}}, nil
}

// SignOut ends the session of the cookie, whatever its level, and expires the cookie.
func (c adminAPI) SignOut(ctx context.Context, _ adminapi.SignOutRequestObject) (adminapi.SignOutResponseObject, error) {
	cookie, err := c.sessions().signOut(ctx, callOf(ctx))
	if err != nil {
		return nil, err
	}
	return adminapi.SignOut204Response{Headers: adminapi.SignOut204ResponseHeaders{SetCookie: cookie}}, nil
}

// GetSession tells a page who is signed in, what the session may do on the platform, and its CSRF token.
func (c adminAPI) GetSession(ctx context.Context, _ adminapi.GetSessionRequestObject) (adminapi.GetSessionResponseObject, error) {
	info, err := c.accounts.SessionInfo(ctx, callOf(ctx).principal)
	if err != nil {
		return nil, err
	}
	var session adminapi.Session // field by field: the console's contract has a type of its own for the same schema
	session.Account, session.CsrfToken, session.ExpiresAt = info.Account.TypeID(), info.CSRFToken, info.ExpiresAt
	session.Level, session.MfaEnabled, session.Permissions = adminapi.SessionLevel(info.Level), info.MFAEnabled, info.Permissions
	session.TotpEnabled = &info.TOTP
	session.User.Id, session.User.Email, session.User.InviteCode = info.User.TypeID(), info.Email, info.InviteCode
	return adminapi.GetSession200JSONResponse(session), nil
}

// BeginTotpSetup makes a TOTP secret; nothing is kept until it is confirmed.
func (c adminAPI) BeginTotpSetup(ctx context.Context, _ adminapi.BeginTotpSetupRequestObject) (adminapi.BeginTotpSetupResponseObject, error) {
	secret, uri := c.accounts.BeginTOTP(callOf(ctx).principal)
	return adminapi.BeginTotpSetup200JSONResponse{Secret: secret, Uri: uri}, nil
}

// ConfirmTotpSetup turns on two-step verification with the secret the user proved.
func (c adminAPI) ConfirmTotpSetup(ctx context.Context, request adminapi.ConfirmTotpSetupRequestObject) (adminapi.ConfirmTotpSetupResponseObject, error) {
	f, err := c.sessions().confirmTOTP(ctx, callOf(ctx), request.Body.Secret, request.Body.Password, request.Body.Code)
	if err != nil {
		return nil, err
	}
	return adminapi.ConfirmTotpSetup200JSONResponse{
		Body: adminapi.FactorChange{CsrfToken: f.csrf, RecoveryCodes: f.codes}, Headers: adminapi.ConfirmTotpSetup200ResponseHeaders{SetCookie: f.cookie},
	}, nil
}

// RegenerateRecoveryCodes replaces the recovery codes; the old ones and the user's other sessions end.
func (c adminAPI) RegenerateRecoveryCodes(ctx context.Context, request adminapi.RegenerateRecoveryCodesRequestObject) (adminapi.RegenerateRecoveryCodesResponseObject, error) {
	f, err := c.sessions().regenerateCodes(ctx, callOf(ctx), request.Body.Code)
	if err != nil {
		return nil, err
	}
	return adminapi.RegenerateRecoveryCodes200JSONResponse{
		Body: adminapi.FactorChange{CsrfToken: f.csrf, RecoveryCodes: f.codes}, Headers: adminapi.RegenerateRecoveryCodes200ResponseHeaders{SetCookie: f.cookie},
	}, nil
}
