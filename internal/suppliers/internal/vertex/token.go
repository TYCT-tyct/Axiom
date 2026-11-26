package vertex

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// A service account is authorized as Google's OAuth 2.0 for server-to-server applications has it: a JWT the account's
// key signs with RS256 is exchanged at the token service for an access token, which calls carry as a bearer token
// (https://developers.google.com/identity/protocols/oauth2/service-account#authorizingrequests).
const (
	// tokenURL is the token service, and the JWT's audience.
	tokenURL = "https://oauth2.googleapis.com/token"
	// scope is what the token may do: Vertex AI is under the cloud platform's scope.
	scope = "https://www.googleapis.com/auth/cloud-platform"
	// lifetime is how long the JWT asks the token to last: the most Google grants.
	lifetime = time.Hour
	// margin is how long before its expiry a token is replaced, so that no call starts with a token about to expire.
	margin = 5 * time.Minute
	// exchangeTimeout bounds an exchange, which a call waits for.
	exchangeTimeout = 10 * time.Second
	// maxTokenAnswer is the most of the token service's answer that is read; a token is about 1 KiB.
	maxTokenAnswer = 64 << 10
	// keepAccounts is how many accounts are held before the ones whose tokens expired are let go.
	keepAccounts = 64
)

var (
	errNoKey      = errors.New("vertex: no RSA private key")
	errNoToken    = errors.New("vertex: the token service gave no token")
	errUnreadable = errors.New("vertex: the sealed service account does not read")
	errUnanswered = errors.New("vertex: the token service did not answer")
)

// refusal is the token service refusing the key: the account or the key was deleted or disabled, or the key is not
// the account's. Its status is the service's.
type refusal struct{ status int }

func (*refusal) Error() string { return "vertex: the token service refused the service account" }

// tokens holds each service account's access token, by the SHA-256 of its sealed text: a replaced key is a new text
// and so never gets the old key's token. The private key is parsed only to sign an exchange and is not kept.
type tokens struct {
	url string
	mu  sync.Mutex
	by  map[[sha256.Size]byte]*account
}

// account is one service account's project and token. exchange is held while one call exchanges the key, so the calls
// that find the token stale at once wait for that one exchange instead of making their own.
type account struct {
	project  string
	exchange sync.Mutex
	current  atomic.Pointer[token]
}

// token is an access token and when to replace it.
type token struct {
	text    string
	replace time.Time
}

// authorized is what a call is made with: the project and a token.
type authorized struct{ project, token string }

func newTokens(url string) *tokens { return &tokens{url: url, by: map[[sha256.Size]byte]*account{}} }

// get is a token of key's account that is good at at, exchanging the key for one when there is none.
func (t *tokens) get(ctx context.Context, d adapter.Doer, key secret.Secret, at time.Time) (authorized, error) {
	text := key.Reveal()
	acct, err := t.account(sha256.Sum256([]byte(text)), text, at)
	if err != nil {
		return authorized{}, err
	}
	if tok := acct.current.Load(); tok != nil && at.Before(tok.replace) {
		return authorized{acct.project, tok.text}, nil
	}
	acct.exchange.Lock()
	defer acct.exchange.Unlock()
	if tok := acct.current.Load(); tok != nil && at.Before(tok.replace) { // another call exchanged it meanwhile
		return authorized{acct.project, tok.text}, nil
	}
	tok, err := t.exchangeKey(ctx, d, text, at)
	if err != nil {
		return authorized{}, err
	}
	acct.current.Store(tok)
	return authorized{acct.project, tok.text}, nil
}

// account is the held account of the key whose text has sum, read from text the first time.
func (t *tokens) account(sum [sha256.Size]byte, text string, at time.Time) (*account, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if a, ok := t.by[sum]; ok {
		return a, nil
	}
	var sa serviceAccount
	if err := jsonx.Unmarshal([]byte(text), &sa); err != nil || sa.ProjectID == "" {
		return nil, errUnreadable
	}
	if len(t.by) >= keepAccounts {
		for k, a := range t.by {
			if tok := a.current.Load(); tok == nil || !at.Before(tok.replace) {
				delete(t.by, k)
			}
		}
	}
	a := &account{project: sa.ProjectID}
	t.by[sum] = a
	return a, nil
}

// exchangeKey signs a JWT with the account's key and exchanges it for an access token.
func (t *tokens) exchangeKey(ctx context.Context, d adapter.Doer, text string, at time.Time) (*token, error) {
	var sa serviceAccount
	if err := jsonx.Unmarshal([]byte(text), &sa); err != nil {
		return nil, errUnreadable
	}
	key, err := privateKey(sa.PrivateKey)
	if err != nil {
		return nil, errUnreadable
	}
	assertion, err := signJWT(sa, key, t.url, at)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, exchangeTimeout)
	defer cancel()
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errUnanswered
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := d.Do(req)
	if err != nil {
		return nil, errUnanswered
	}
	defer resp.Body.Close()                                            //nolint:errcheck // the answer was read; closing it says nothing more
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenAnswer)) // read only for the token; nothing else of it is kept
	if err != nil {
		return nil, errUnanswered
	}
	return granted(resp.StatusCode, body, at)
}

// granted reads the token service's answer. 400 and 401 are the key refused (invalid_grant, invalid_client); anything
// else that is not a token says nothing about the key.
func granted(status int, body []byte, at time.Time) (*token, error) {
	if status == http.StatusBadRequest || status == http.StatusUnauthorized {
		return nil, &refusal{status: status}
	}
	var answer struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if status != http.StatusOK || jsonx.Unmarshal(body, &answer) != nil || answer.AccessToken == "" {
		return nil, errNoToken
	}
	lasts := lifetime
	if answer.ExpiresIn > 0 {
		lasts = time.Duration(min(answer.ExpiresIn, int64(lifetime/time.Second))) * time.Second
	}
	return &token{text: answer.AccessToken, replace: at.Add(lasts - margin)}, nil
}

// signJWT is the assertion an exchange sends: the account as issuer, the token service as audience, the scope, and the
// hour it is good for, signed RS256 (RSASSA-PKCS1-v1_5 over SHA-256) with the account's key.
func signJWT(sa serviceAccount, key *rsa.PrivateKey, aud string, at time.Time) (string, error) {
	header, err := jsonx.MarshalCanonical(map[string]string{"alg": "RS256", "typ": "JWT", "kid": sa.PrivateKeyID})
	if err != nil {
		return "", err
	}
	iat := at.Unix()
	claims, err := jsonx.MarshalCanonical(map[string]any{
		"iss": sa.ClientEmail, "scope": scope, "aud": aud, "iat": iat, "exp": iat + int64(lifetime/time.Second),
	})
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
