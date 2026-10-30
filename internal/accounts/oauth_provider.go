package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

// oauthProvider is one provider's fixed protocol endpoints. They are constants, never request input, and tests replace
// the whole entry to point at a fake server.
type oauthProvider struct {
	authorize, token string
	userinfo         string
	scope            string
	pkce             bool // whether the provider's authorization endpoint accepts PKCE
}

// oauthProviders are the three built-in providers. All three authorization endpoints take PKCE S256; GitHub's OAuth
// apps gained it in July 2025. The URLs are public authorization endpoints, not credentials.
var oauthProviders = map[string]oauthProvider{
	"github": { //nolint:gosec // G101: public authorization endpoint, not a credential
		authorize: "https://github.com/login/oauth/authorize",
		token:     "https://github.com/login/oauth/access_token", userinfo: "https://api.github.com/user",
		scope: "read:user", pkce: true,
	},
	"google": { //nolint:gosec // G101: public authorization endpoint, not a credential
		authorize: "https://accounts.google.com/o/oauth2/v2/auth",
		token:     "https://oauth2.googleapis.com/token", userinfo: "https://openidconnect.googleapis.com/v1/userinfo",
		scope: "openid email profile", pkce: true,
	},
	"linuxdo": { //nolint:gosec // G101: public authorization endpoint, not a credential
		authorize: "https://connect.linux.do/oauth2/authorize",
		token:     "https://connect.linux.do/oauth2/token", userinfo: "https://connect.linux.do/api/user",
		scope: "user", pkce: true,
	},
}

// oauthTimeout bounds one provider call; the two calls of a round trip share it.
const oauthTimeout = 10 * time.Second

// providerIdentity exchanges the code and asks who signed in. Every request goes through the module's outbound client
// with a deadline; the response body is bounded because it comes from a third party.
func (s *Service) providerIdentity(ctx context.Context, provider, clientID string, clientSecret secret.Secret, code, verifier, redirect string) (oauthIdentity, error) {
	p := s.providers[provider]
	ctx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()
	token, err := s.oauthAccessToken(ctx, p, clientID, clientSecret, code, verifier, redirect)
	if err != nil {
		return oauthIdentity{}, err
	}
	return s.oauthAccount(ctx, provider, p.userinfo, token)
}

// oauthAccessToken swaps the authorization code, with the flow's verifier when the provider takes PKCE.
func (s *Service) oauthAccessToken(ctx context.Context, p oauthProvider, clientID string, clientSecret secret.Secret, code, verifier, redirect string) (string, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {clientID}, "client_secret": {clientSecret.Reveal()},
	}
	if p.pkce {
		form.Set("code_verifier", verifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("exchange the authorization code: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := s.oauthJSON(req, &token); err != nil {
		return "", err
	}
	if token.AccessToken == "" {
		return "", ErrOAuthFailed
	}
	return token.AccessToken, nil
}

// oauthAccount reads the provider's userinfo. Each provider names its permanent subject in its own shape.
func (s *Service) oauthAccount(ctx context.Context, provider, userinfo, token string) (oauthIdentity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfo, nil)
	if err != nil {
		return oauthIdentity{}, fmt.Errorf("read the provider account: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	switch provider {
	case "github":
		return s.githubIdentity(req)
	case "google":
		return s.googleIdentity(req)
	case "linuxdo":
		return s.linuxdoIdentity(req)
	default:
		return oauthIdentity{}, ErrOAuthNotConfigured
	}
}

// githubIdentity reads GitHub's user object: the numeric id is the subject, the login the fallback name.
func (s *Service) githubIdentity(req *http.Request) (oauthIdentity, error) {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := s.oauthJSON(req, &user); err != nil {
		return oauthIdentity{}, err
	}
	if user.ID == 0 || user.Login == "" {
		return oauthIdentity{}, ErrOAuthFailed
	}
	return oauthIdentity{subject: fmt.Sprint(user.ID), display: firstText(user.Name, user.Login)}, nil
}

// googleIdentity reads Google's OpenID Connect claims: sub is the subject, the email the fallback name.
func (s *Service) googleIdentity(req *http.Request) (oauthIdentity, error) {
	var user struct {
		Subject string `json:"sub"`
		Name    string `json:"name"`
		Email   string `json:"email"`
	}
	if err := s.oauthJSON(req, &user); err != nil {
		return oauthIdentity{}, err
	}
	if user.Subject == "" {
		return oauthIdentity{}, ErrOAuthFailed
	}
	return oauthIdentity{subject: user.Subject, display: firstText(user.Name, user.Email)}, nil
}

// linuxdoIdentity reads LinuxDO's user object: the numeric id is the subject, the username the fallback name.
func (s *Service) linuxdoIdentity(req *http.Request) (oauthIdentity, error) {
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := s.oauthJSON(req, &user); err != nil {
		return oauthIdentity{}, err
	}
	if user.ID == 0 {
		return oauthIdentity{}, ErrOAuthFailed
	}
	return oauthIdentity{subject: fmt.Sprint(user.ID), display: firstText(user.Name, user.Username)}, nil
}

// oauthJSON runs req and decodes a bounded JSON body. The provider's error text is not repeated: it can hold the code.
func (s *Service) oauthJSON(req *http.Request, into any) error {
	resp, err := s.exit.Do(req)
	if err != nil {
		return fmt.Errorf("call the provider: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // a body already read to its end leaves nothing to learn from the close
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: provider answered %d", ErrOAuthFailed, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("read the provider response: %w", err)
	}
	if err := jsonx.Unmarshal(body, into); err != nil {
		return fmt.Errorf("%w: %w", ErrOAuthFailed, err)
	}
	return nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func firstText(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
