// Package vertex is Google Cloud's Vertex AI, reached with a service account's JSON key (docs/domains/suppliers.md
// 「云平台」): Anthropic's models through Vertex's Anthropic endpoints (rawPredict and streamRawPredict) in Messages, and
// Gemini through Vertex's OpenAI-compatible Chat endpoint, since the gateway sends no Gemini-native call. Each region is
// a credential of its own. A call is authorized with an access token the key is exchanged for (token.go).
package vertex

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/cloud"
)

// Kind is the adapter's name in suppliers.kind.
const Kind adapter.Kind = "vertex"

// origin is what the supplier shows as its address; calls go to each region's own host (host).
const origin = "https://aiplatform.googleapis.com"

// Adapter is Vertex AI's adapter. It holds the access tokens of the service accounts it was asked to call with, in
// memory, until shortly before they expire.
type Adapter struct {
	cloud.Platform
	tokens *tokens
}

var (
	_ adapter.Adapter = (*Adapter)(nil)
	_ adapter.Cloud   = (*Adapter)(nil)
	_ adapter.Signer  = quirks{}
)

// New is the adapter.
func New() *Adapter { return &Adapter{tokens: newTokens(tokenURL)} }

// Kind implements adapter.Adapter.
func (*Adapter) Kind() adapter.Kind { return Kind }

// Origin implements adapter.Cloud.
func (*Adapter) Origin() string { return origin }

// Regional implements adapter.Cloud.
func (*Adapter) Regional() bool { return true }

// serviceAccount is what is kept of a service account's JSON key: what signs a token and what names the project. The
// rest of the file (its token_uri among it) is not read: tokens are asked of Google's one token service, never of an
// address a pasted file names.
type serviceAccount struct {
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
}

// ReadCredential implements adapter.Cloud: the JSON key with its project, account and private key, which must be an
// RSA key. What is sealed is those four members alone.
func (*Adapter) ReadCredential(in adapter.CloudInput) (adapter.CloudCredential, error) {
	const member = "service_account_json"
	if in.AccessKeyID != "" || in.SecretAccessKey != "" || in.APIKey != "" {
		return adapter.CloudCredential{}, &adapter.CredentialFormError{Member: "service_account_json", Reason: "is the only member Vertex AI takes"}
	}
	var sa serviceAccount
	if err := jsonx.Unmarshal([]byte(in.ServiceAccountJSON), &sa); err != nil {
		return adapter.CloudCredential{}, &adapter.CredentialFormError{Member: member, Reason: "is not a service account's JSON key"}
	}
	if sa.ProjectID == "" || sa.ClientEmail == "" || sa.PrivateKeyID == "" || !strings.Contains(sa.ClientEmail, "@") {
		return adapter.CloudCredential{}, &adapter.CredentialFormError{Member: member, Reason: "needs project_id, client_email and private_key_id"}
	}
	if _, err := privateKey(sa.PrivateKey); err != nil {
		return adapter.CloudCredential{}, &adapter.CredentialFormError{Member: member, Reason: "has no RSA private_key"}
	}
	sealed, err := jsonx.MarshalCanonical(sa)
	if err != nil {
		return adapter.CloudCredential{}, &adapter.CredentialFormError{Member: member, Reason: "is not a service account's JSON key"}
	}
	return adapter.CloudCredential{
		Key: secret.New(string(sealed)), Identity: sa.ClientEmail + "\x00" + sa.PrivateKeyID, Last4: last4(sa.PrivateKeyID),
		Account: adapter.CloudAccount{ProjectID: sa.ProjectID, ClientEmail: sa.ClientEmail},
	}, nil
}

// privateKey reads a PEM private key, PKCS #8 as Google writes it or PKCS #1.
func privateKey(text string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, errNoKey
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errNoKey
	}
	k, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errNoKey
	}
	return k, nil
}

func last4(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}

// host is a region's address: global and the multi-regions us and eu have their own.
func host(region string) string {
	switch region {
	case "global":
		return "aiplatform.googleapis.com"
	case "us", "eu":
		return "aiplatform." + region + ".rep.googleapis.com"
	}
	return region + "-aiplatform.googleapis.com"
}

// base is the region's path of the project, under which every call and the check go.
func base(project, region string) string {
	return "https://" + host(region) + "/v1/projects/" + project + "/locations/" + region
}

// CheckRegion implements adapter.Cloud: the key is exchanged for a token, which only a key Google accepts is, and the
// region is read with it (locations.get), which is not charged for. A refused exchange fails the credential in every
// region; a region the project cannot use (400 or 404) fails as region_unavailable. Vertex AI lists no models of the
// project, so a check has none and the catalog's are offered (Offer).
func (a *Adapter) CheckRegion(ctx context.Context, d adapter.Doer, _, region string, key secret.Secret, at time.Time) adapter.CloudCheck {
	ctx, cancel := cloud.Checked(ctx)
	defer cancel()
	acct, err := a.tokens.get(ctx, d, key, at)
	if err != nil {
		return tokenCheck(ctx, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base(acct.project, region), nil)
	if err != nil {
		return adapter.CloudCheck{Cause: adapter.CauseUnknown}
	}
	req.Header.Set("Authorization", "Bearer "+acct.token)
	resp, check, err := cloud.Fetch(ctx, d, req)
	if err != nil {
		return check
	}
	if resp.Status == http.StatusBadRequest || resp.Status == http.StatusNotFound {
		return adapter.CloudCheck{Outcome: adapter.KeyFailed, Cause: adapter.CauseRegionUnavailable, Status: resp.Status}
	}
	return cloud.Judge(resp.Status)
}

// tokenCheck is a check whose exchange of the key failed.
func tokenCheck(ctx context.Context, err error) adapter.CloudCheck {
	var refused *refusal
	if errors.As(err, &refused) {
		return adapter.CloudCheck{Outcome: adapter.KeyFailed, Cause: adapter.CauseRejected, Status: refused.status}
	}
	if ctx.Err() != nil {
		return adapter.CloudCheck{Cause: adapter.CauseTimeout}
	}
	return adapter.CloudCheck{Cause: adapter.CauseTransport}
}

// dated is a name that ends in its date, which Vertex AI names a version by after an @.
var dated = regexp.MustCompile(`^(.+)-([0-9]{8})$`)

// Offer implements adapter.Cloud. Anthropic's models are named as Anthropic names them, a dated one with @ before its
// date (claude-sonnet-4-5@20250929); Gemini is google/ and its name, as the OpenAI-compatible endpoint takes it.
func (*Adapter) Offer(m catalog.Model, official string) (string, bool) {
	switch m.Author {
	case "anthropic":
		if p := dated.FindStringSubmatch(official); p != nil {
			return p[1] + "@" + p[2], true
		}
		return official, true
	case "google":
		return "google/" + official, true
	}
	return "", false
}

// Formats implements adapter.Cloud: Messages for Anthropic's models, Chat for Gemini, for a model that generates.
func (*Adapter) Formats(m catalog.Model) []protocol.Format {
	switch {
	case !cloud.Generates(m):
	case m.Author == "anthropic":
		return []protocol.Format{protocol.Messages}
	case m.Author == "google":
		return []protocol.Format{protocol.Chat}
	}
	return nil
}

// Unmapped implements adapter.Cloud. Every name is offered from the catalog, so none is unmapped; one would be an
// Anthropic model's name.
func (*Adapter) Unmapped() []protocol.Format { return []protocol.Format{protocol.Messages} }

// Calls implements adapter.Adapter.
func (a *Adapter) Calls() adapter.CallQuirks { return quirks{a: a} }
