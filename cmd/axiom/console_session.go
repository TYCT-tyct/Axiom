package main

import (
	"context"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
)

func (c consoleAPI) sessions() sessionOps { return sessionOps{c.accounts} }

// grant is what a sign-in or a verification tells the page of the session it set.
func grant(issued accounts.Issued) consoleapi.SessionGrant {
	status := consoleapi.SessionGrantStatusSignedIn
	if issued.Pending {
		status = consoleapi.SessionGrantStatusMfaRequired
	}
	return consoleapi.SessionGrant{Status: status, CsrfToken: issued.CSRFToken}
}

// GetSite tells the sign-in and registration pages whose they are, and where a key's calls go.
func (consoleAPI) GetSite(ctx context.Context, _ consoleapi.GetSiteRequestObject) (consoleapi.GetSiteResponseObject, error) {
	site := callOf(ctx).site
	id := consoleapi.SiteId(site.ID.TypeID())
	response := consoleapi.Site{
		Id: &id, Name: site.Name, Currency: site.Currency.String(), KeyPrefix: site.KeyPrefix,
		Registration: consoleapi.SiteRegistration(site.Registration),
	}
	if site.APIBase != "" {
		response.ApiBaseUrl = &site.APIBase
	}
	if site.Registration == "open" {
		verified := true
		response.EmailVerification = &verified
		if site.RegistrationHumanCheck {
			response.HumanCheck = &struct {
				Bits int `json:"bits"`
			}{Bits: site.RegistrationHumanCheckBits}
		}
	}
	return consoleapi.GetSite200JSONResponse(response), nil
}

// Register makes a user from an invite code, or starts the email verification of an open registration.
func (c consoleAPI) Register(ctx context.Context, request consoleapi.RegisterRequestObject) (consoleapi.RegisterResponseObject, error) {
	cl := callOf(ctx)
	body := request.Body
	invite, password, human, language := "", "", "", ""
	if body.InviteCode != nil {
		invite = *body.InviteCode
	}
	if body.Password != nil {
		password = *body.Password
	}
	if body.HumanCheck != nil {
		human = *body.HumanCheck
	}
	if body.Language != nil {
		language = string(*body.Language)
	}
	out, err := c.accounts.Register(ctx, accounts.RegisterRequest{
		Host: cl.r.Host, InviteCode: invite, Email: body.Email, Password: password,
		HumanCheck: human, Language: language, ClientIP: cl.ip,
	})
	if err != nil {
		return nil, err
	}
	if out.Pending {
		return consoleapi.Register202JSONResponse{Status: consoleapi.VerificationSent}, nil
	}
	return consoleapi.Register201JSONResponse{UserId: out.User.TypeID()}, nil
}

// VerifyEmail turns the link's token and the chosen password into the pending user.
func (c consoleAPI) VerifyEmail(ctx context.Context, request consoleapi.VerifyEmailRequestObject) (consoleapi.VerifyEmailResponseObject, error) {
	if _, err := c.accounts.VerifyEmail(ctx, callOf(ctx).r.Host, request.Body.Token, request.Body.Password); err != nil {
		return nil, err
	}
	return consoleapi.VerifyEmail204Response{}, nil
}

// SignIn checks the password and sets a session, a pending one when the user has two-step verification.
func (c consoleAPI) SignIn(ctx context.Context, request consoleapi.SignInRequestObject) (consoleapi.SignInResponseObject, error) {
	issued, cookie, err := c.sessions().signIn(ctx, callOf(ctx), request.Body.Email, request.Body.Password)
	if err != nil {
		return nil, err
	}
	return consoleapi.SignIn200JSONResponse{Body: grant(issued), Headers: consoleapi.SignIn200ResponseHeaders{SetCookie: cookie}}, nil
}

// VerifyMfa turns the pending session into a verified one.
func (c consoleAPI) VerifyMfa(ctx context.Context, request consoleapi.VerifyMfaRequestObject) (consoleapi.VerifyMfaResponseObject, error) {
	issued, cookie, err := c.sessions().verify(ctx, callOf(ctx), request.Body.Code)
	if err != nil {
		return nil, err
	}
	return consoleapi.VerifyMfa200JSONResponse{Body: grant(issued), Headers: consoleapi.VerifyMfa200ResponseHeaders{SetCookie: cookie}}, nil
}

// SignOut ends the session of the cookie, whatever its level, and expires the cookie.
func (c consoleAPI) SignOut(ctx context.Context, _ consoleapi.SignOutRequestObject) (consoleapi.SignOutResponseObject, error) {
	cookie, err := c.sessions().signOut(ctx, callOf(ctx))
	if err != nil {
		return nil, err
	}
	return consoleapi.SignOut204Response{Headers: consoleapi.SignOut204ResponseHeaders{SetCookie: cookie}}, nil
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
	f, err := c.sessions().confirmTOTP(ctx, callOf(ctx), request.Body.Secret, request.Body.Password, request.Body.Code)
	if err != nil {
		return nil, err
	}
	return consoleapi.ConfirmTotpSetup200JSONResponse{
		Body: consoleapi.FactorChange{CsrfToken: f.csrf, RecoveryCodes: f.codes}, Headers: consoleapi.ConfirmTotpSetup200ResponseHeaders{SetCookie: f.cookie},
	}, nil
}

// RegenerateRecoveryCodes replaces the recovery codes; the old ones and the user's other sessions end.
func (c consoleAPI) RegenerateRecoveryCodes(ctx context.Context, request consoleapi.RegenerateRecoveryCodesRequestObject) (consoleapi.RegenerateRecoveryCodesResponseObject, error) {
	f, err := c.sessions().regenerateCodes(ctx, callOf(ctx), request.Body.Code)
	if err != nil {
		return nil, err
	}
	return consoleapi.RegenerateRecoveryCodes200JSONResponse{
		Body: consoleapi.FactorChange{CsrfToken: f.csrf, RecoveryCodes: f.codes}, Headers: consoleapi.RegenerateRecoveryCodes200ResponseHeaders{SetCookie: f.cookie},
	}, nil
}
