package main

import (
	"context"

	"axiom/internal/accounts"
)

// sessionOps is what the console's and the admin's session operations share: the console and the admin sign in, verify,
// sign out and change the second factor alike (internal/platform/README.md「接口约定」: the operations are written once in
// each contract and are identical). Each surface only maps the results to the types of its own contract.
type sessionOps struct{ accounts *accounts.Service }

// cookie is the Set-Cookie header that gives the browser the new session.
func (s sessionOps) cookie(host string, issued accounts.Issued) (string, error) {
	cookie, err := s.accounts.Cookie(host, issued)
	if err != nil {
		return "", err
	}
	return cookie.String(), nil
}

// signIn checks the password and issues a session, a pending one when the user has two-step verification, with the
// cookie that carries it.
func (s sessionOps) signIn(ctx context.Context, cl call, email, password string) (accounts.Issued, string, error) {
	issued, err := s.accounts.SignIn(ctx, accounts.SignInRequest{
		Host: cl.r.Host, Email: email, Password: password, ClientIP: cl.ip, UserAgent: cl.r.UserAgent(),
	})
	if err != nil {
		return accounts.Issued{}, "", err
	}
	cookie, err := s.cookie(cl.r.Host, issued)
	return issued, cookie, err
}

// verify turns the pending session of the request into a verified one, with the cookie that carries it.
func (s sessionOps) verify(ctx context.Context, cl call, code string) (accounts.Issued, string, error) {
	issued, err := s.accounts.VerifyMFA(ctx, cl.r, code, cl.ip, cl.r.UserAgent())
	if err != nil {
		return accounts.Issued{}, "", err
	}
	cookie, err := s.cookie(cl.r.Host, issued)
	return issued, cookie, err
}

// signOut ends the session of the request and returns the cookie that expires it.
func (s sessionOps) signOut(ctx context.Context, cl call) (string, error) {
	if err := s.accounts.SignOut(ctx, cl.principal); err != nil {
		return "", err
	}
	cookie, err := s.accounts.ExpiredCookie(cl.r.Host)
	if err != nil {
		return "", err
	}
	return cookie.String(), nil
}

// factorChange is a changed second factor: the new session's cookie and CSRF token, and the recovery codes.
type factorChange struct {
	cookie, csrf string
	codes        []string
}

// confirmTOTP turns on two-step verification with the secret the user proved.
func (s sessionOps) confirmTOTP(ctx context.Context, cl call, secret, password, code string) (factorChange, error) {
	return s.changed(cl, func() (accounts.Issued, []string, error) {
		return s.accounts.ConfirmTOTP(ctx, cl.principal, accounts.FactorChange{
			Secret: secret, Password: password, Code: code, ClientIP: cl.ip, UserAgent: cl.r.UserAgent(),
		})
	})
}

// regenerateCodes replaces the recovery codes; the old ones and the user's other sessions end.
func (s sessionOps) regenerateCodes(ctx context.Context, cl call, code string) (factorChange, error) {
	return s.changed(cl, func() (accounts.Issued, []string, error) {
		return s.accounts.RegenerateRecoveryCodes(ctx, cl.principal, accounts.FactorChange{
			Code: code, ClientIP: cl.ip, UserAgent: cl.r.UserAgent(),
		})
	})
}

// replaceTOTP replaces the second factor; the old secret, the old recovery codes and the user's other sessions end.
func (s sessionOps) replaceTOTP(ctx context.Context, cl call, secret, currentCode, code string) (factorChange, error) {
	return s.changed(cl, func() (accounts.Issued, []string, error) {
		return s.accounts.ReplaceTOTP(ctx, cl.principal, accounts.FactorChange{
			Secret: secret, CurrentCode: currentCode, Code: code, ClientIP: cl.ip, UserAgent: cl.r.UserAgent(),
		})
	})
}

func (s sessionOps) changed(cl call, change func() (accounts.Issued, []string, error)) (factorChange, error) {
	issued, codes, err := change()
	if err != nil {
		return factorChange{}, err
	}
	cookie, err := s.cookie(cl.r.Host, issued)
	return factorChange{cookie: cookie, csrf: issued.CSRFToken, codes: codes}, err
}
