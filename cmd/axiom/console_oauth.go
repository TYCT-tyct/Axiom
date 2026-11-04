package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/webui"
)

// StartOAuthSignIn begins a third-party sign-in and returns the provider's authorization URL.
func (c consoleAPI) StartOAuthSignIn(ctx context.Context, request consoleapi.StartOAuthSignInRequestObject) (consoleapi.StartOAuthSignInResponseObject, error) {
	cl := callOf(ctx)
	out, err := c.accounts.StartOAuth(ctx, cl.r.Host, string(request.Body.Provider), nil, cl.ip)
	if err != nil {
		return nil, err
	}
	headers := consoleapi.StartOAuthSignIn200ResponseHeaders{}
	if out.Nonce != "" {
		cookie, err := c.accounts.OAuthNonceCookie(cl.r.Host, out.Nonce)
		if err != nil {
			return nil, err
		}
		headers.SetCookie = cookie.String()
	}
	return consoleapi.StartOAuthSignIn200JSONResponse{Body: consoleapi.OAuthStart{AuthorizeUrl: out.URL}, Headers: headers}, nil
}

// StartOAuthLink begins linking a provider account to the signed-in user.
func (c consoleAPI) StartOAuthLink(ctx context.Context, request consoleapi.StartOAuthLinkRequestObject) (consoleapi.StartOAuthLinkResponseObject, error) {
	cl := callOf(ctx)
	p := cl.principal
	out, err := c.accounts.StartOAuth(ctx, cl.r.Host, string(request.Body.Provider), &p, cl.ip)
	if err != nil {
		return nil, err
	}
	return consoleapi.StartOAuthLink200JSONResponse{AuthorizeUrl: out.URL}, nil
}

// CompleteOAuth is the provider's redirect: it signs in or links, sets the cookie, and sends the browser to a page
// rather than to JSON. A failed round trip is a redirect too, so a provider's denial never leaves the user on an error
// body; an internal failure is still the API's 500.
func (c consoleAPI) CompleteOAuth(ctx context.Context, request consoleapi.CompleteOAuthRequestObject) (consoleapi.CompleteOAuthResponseObject, error) {
	cl := callOf(ctx)
	code, state := "", ""
	if request.Params.Code != nil {
		code = *request.Params.Code
	}
	if request.Params.State != nil {
		state = *request.Params.State
	}
	out, err := c.accounts.CompleteOAuth(ctx, cl.r, string(request.Provider), code, state, cl.ip, cl.r.UserAgent())
	if err != nil {
		if failure(err) == nil {
			return nil, err
		}
		// A link failure belongs to the signed-in page, not the sign-in page, and keeps the reason the service
		// distinguished (this account already has the provider vs. another account owns the subject).
		if errors.Is(err, accounts.ErrIdentityAlreadyLinked) {
			return consoleapi.CompleteOAuth303Response{Headers: consoleapi.CompleteOAuth303ResponseHeaders{
				Location: webui.ConsoleMount + "/security?oauth=identity_already_linked",
			}}, nil
		}
		if errors.Is(err, accounts.ErrIdentityLinked) {
			return consoleapi.CompleteOAuth303Response{Headers: consoleapi.CompleteOAuth303ResponseHeaders{
				Location: webui.ConsoleMount + "/security?oauth=identity_linked",
			}}, nil
		}
		return consoleapi.CompleteOAuth303Response{Headers: consoleapi.CompleteOAuth303ResponseHeaders{
			Location: webui.ConsoleMount + "/sign-in?oauth=failed",
		}}, nil
	}
	headers := consoleapi.CompleteOAuth303ResponseHeaders{Location: webui.ConsoleMount + "/"}
	if out.Linked {
		headers.Location = webui.ConsoleMount + "/security"
	}
	if out.Issued != nil {
		cookie, err := c.sessions().cookie(cl.r.Host, *out.Issued)
		if err != nil {
			return nil, err
		}
		headers.SetCookie = &cookie
	}
	return consoleapi.CompleteOAuth303Response{Headers: headers}, nil
}

// BeginPasskeySignIn starts a discoverable assertion.
func (c consoleAPI) BeginPasskeySignIn(ctx context.Context, _ consoleapi.BeginPasskeySignInRequestObject) (consoleapi.BeginPasskeySignInResponseObject, error) {
	cl := callOf(ctx)
	challenge, err := c.accounts.BeginPasskeySignIn(ctx, cl.r.Host, cl.ip)
	if err != nil {
		return nil, err
	}
	return consoleapi.BeginPasskeySignIn200JSONResponse(challengeBody(challenge)), nil
}

// FinishPasskeySignIn verifies the assertion and issues a session.
func (c consoleAPI) FinishPasskeySignIn(ctx context.Context, request consoleapi.FinishPasskeySignInRequestObject) (consoleapi.FinishPasskeySignInResponseObject, error) {
	cl := callOf(ctx)
	issued, err := c.accounts.FinishPasskeySignIn(ctx, credentialRequest(cl.r, *request.Body), request.Params.FlowId, cl.ip, cl.r.UserAgent())
	if err != nil {
		return nil, err
	}
	cookie, err := c.sessions().cookie(cl.r.Host, issued)
	if err != nil {
		return nil, err
	}
	return consoleapi.FinishPasskeySignIn200JSONResponse{
		Body: grant(issued), Headers: consoleapi.FinishPasskeySignIn200ResponseHeaders{SetCookie: cookie},
	}, nil
}

// BeginPasskeyMfa starts the passkey factor of a pending session.
func (c consoleAPI) BeginPasskeyMfa(ctx context.Context, _ consoleapi.BeginPasskeyMfaRequestObject) (consoleapi.BeginPasskeyMfaResponseObject, error) {
	cl := callOf(ctx)
	challenge, err := c.accounts.BeginPasskeyMFA(ctx, cl.r, cl.principal, cl.ip)
	if err != nil {
		return nil, err
	}
	return consoleapi.BeginPasskeyMfa200JSONResponse(challengeBody(challenge)), nil
}

// FinishPasskeyMfa verifies the pending session's assertion and issues a verified session.
func (c consoleAPI) FinishPasskeyMfa(ctx context.Context, request consoleapi.FinishPasskeyMfaRequestObject) (consoleapi.FinishPasskeyMfaResponseObject, error) {
	cl := callOf(ctx)
	issued, err := c.accounts.FinishPasskeyMFA(ctx, credentialRequest(cl.r, *request.Body), cl.principal, request.Params.FlowId, cl.ip, cl.r.UserAgent())
	if err != nil {
		return nil, err
	}
	cookie, err := c.sessions().cookie(cl.r.Host, issued)
	if err != nil {
		return nil, err
	}
	return consoleapi.FinishPasskeyMfa200JSONResponse{
		Body: grant(issued), Headers: consoleapi.FinishPasskeyMfa200ResponseHeaders{SetCookie: cookie},
	}, nil
}

// BeginPasskeyRegistration starts adding a passkey.
func (c consoleAPI) BeginPasskeyRegistration(ctx context.Context, request consoleapi.BeginPasskeyRegistrationRequestObject) (consoleapi.BeginPasskeyRegistrationResponseObject, error) {
	cl := callOf(ctx)
	name := ""
	if request.Body.Name != nil {
		name = *request.Body.Name
	}
	registration, err := c.accounts.BeginPasskeyRegistration(ctx, cl.r, cl.principal, name, request.Body.Password)
	if err != nil {
		return nil, err
	}
	return consoleapi.BeginPasskeyRegistration200JSONResponse{
		FlowId: registration.FlowID, PublicKey: mustOptions(registration.PublicKey),
	}, nil
}

// FinishPasskeyRegistration verifies the attestation and stores the credential.
func (c consoleAPI) FinishPasskeyRegistration(ctx context.Context, request consoleapi.FinishPasskeyRegistrationRequestObject) (consoleapi.FinishPasskeyRegistrationResponseObject, error) {
	cl := callOf(ctx)
	passkey, issued, codes, err := c.accounts.FinishPasskeyRegistration(ctx, credentialRequest(cl.r, *request.Body), cl.principal, request.Params.FlowId, cl.ip, cl.r.UserAgent())
	if err != nil {
		return nil, err
	}
	cookie, err := c.sessions().cookie(cl.r.Host, issued)
	if err != nil {
		return nil, err
	}
	body := passkeyBody(passkey)
	if codes != nil {
		body.RecoveryCodes = &codes
	}
	return consoleapi.FinishPasskeyRegistration200JSONResponse{
		Body: body, Headers: consoleapi.FinishPasskeyRegistration200ResponseHeaders{SetCookie: cookie},
	}, nil
}

// ListPasskeys lists the signed-in user's credentials.
func (c consoleAPI) ListPasskeys(ctx context.Context, _ consoleapi.ListPasskeysRequestObject) (consoleapi.ListPasskeysResponseObject, error) {
	passkeys, err := c.accounts.Passkeys(ctx, callOf(ctx).principal)
	if err != nil {
		return nil, err
	}
	out := make([]consoleapi.Passkey, 0, len(passkeys))
	for _, passkey := range passkeys {
		out = append(out, passkeyBody(passkey))
	}
	return consoleapi.ListPasskeys200JSONResponse{Passkeys: out}, nil
}

// DeletePasskey removes a credential and replaces the session.
func (c consoleAPI) DeletePasskey(ctx context.Context, request consoleapi.DeletePasskeyRequestObject) (consoleapi.DeletePasskeyResponseObject, error) {
	cl := callOf(ctx)
	id, err := accounts.ParsePasskeyID(request.Id)
	if err != nil {
		return nil, accounts.ErrNotFound
	}
	issued, codes, err := c.accounts.DeletePasskey(ctx, cl.principal, id, request.Body.Password, cl.ip, cl.r.UserAgent())
	if err != nil {
		return nil, err
	}
	cookie, err := c.sessions().cookie(cl.r.Host, issued)
	if err != nil {
		return nil, err
	}
	body := consoleapi.CredentialChange{CsrfToken: issued.CSRFToken}
	if codes != nil {
		body.RecoveryCodes = &codes
	}
	return consoleapi.DeletePasskey200JSONResponse{
		Body: body, Headers: consoleapi.DeletePasskey200ResponseHeaders{SetCookie: cookie},
	}, nil
}

// ListIdentities lists the signed-in user's linked provider accounts.
func (c consoleAPI) ListIdentities(ctx context.Context, _ consoleapi.ListIdentitiesRequestObject) (consoleapi.ListIdentitiesResponseObject, error) {
	identities, err := c.accounts.Identities(ctx, callOf(ctx).principal)
	if err != nil {
		return nil, err
	}
	out := make([]consoleapi.Identity, 0, len(identities))
	for _, identity := range identities {
		out = append(out, consoleapi.Identity{
			Provider: consoleapi.OAuthProvider(identity.Provider), DisplayName: identity.DisplayName, CreatedAt: identity.CreatedAt,
		})
	}
	return consoleapi.ListIdentities200JSONResponse{Identities: out}, nil
}

// UnlinkIdentity detaches a provider account and replaces the session.
func (c consoleAPI) UnlinkIdentity(ctx context.Context, request consoleapi.UnlinkIdentityRequestObject) (consoleapi.UnlinkIdentityResponseObject, error) {
	cl := callOf(ctx)
	issued, codes, err := c.accounts.UnlinkIdentity(ctx, cl.principal, string(request.Provider), cl.ip, cl.r.UserAgent())
	if err != nil {
		return nil, err
	}
	cookie, err := c.sessions().cookie(cl.r.Host, issued)
	if err != nil {
		return nil, err
	}
	body := consoleapi.CredentialChange{CsrfToken: issued.CSRFToken}
	if codes != nil {
		body.RecoveryCodes = &codes
	}
	return consoleapi.UnlinkIdentity200JSONResponse{
		Body: body, Headers: consoleapi.UnlinkIdentity200ResponseHeaders{SetCookie: cookie},
	}, nil
}

// challengeBody turns a WebAuthn challenge into the contract's raw options object.
func challengeBody(challenge accounts.PasskeyChallenge) consoleapi.PasskeyChallenge {
	return consoleapi.PasskeyChallenge{FlowId: challenge.FlowID, PublicKey: mustOptions(challenge.PublicKey)}
}

// mustOptions encodes options the browser's WebAuthn API consumes; a value built by the verifier cannot fail to encode
// (it is made of strings, numbers and byte arrays), so a failure here is a bug and panics instead of answering a
// ceremony that could never be finished.
func mustOptions(options any) jsonx.Raw {
	encoded, err := jsonx.MarshalCanonical(options)
	if err != nil {
		panic(err)
	}
	return encoded
}

// credentialRequest rebuilds the request the WebAuthn verifier reads the credential from: the guard and the generated
// handler already read the body, and the verifier wants it as the browser sent it.
func credentialRequest(r *http.Request, body jsonx.Raw) *http.Request {
	clone := r.Clone(r.Context())
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return clone
}

func passkeyBody(passkey accounts.Passkey) consoleapi.Passkey {
	id := passkey.ID.TypeID()
	return consoleapi.Passkey{
		Id: id, Name: passkey.Name, CreatedAt: passkey.CreatedAt, LastUsedAt: passkey.LastUsedAt,
		BackupEligible: passkey.BackupEligible,
	}
}
