// Package azure is Azure OpenAI, reached with a resource's address and key (docs/domains/suppliers-direct.md「云平台」):
// the deployments a person names are its models, called in OpenAI's Chat and embeddings at the deployment's path with
// an api-version, and in Responses at the resource's v1 path, the key in the api-key header. A resource has no region to choose: it is one credential.
package azure

import (
	"context"
	"net/http"
	"net/url"
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
const Kind adapter.Kind = "azure"

// apiVersion is the data plane's version calls and the check name when the credential's settings give none: the
// generally available one that streams usage. No version after 2022-12-01 lists a resource's deployments to its key
// (listing moved to Azure Resource Manager, which takes an Entra ID token), so the check does not list them and a person
// names them.
const apiVersion = "2024-10-21"

// Adapter is Azure OpenAI's adapter. It holds nothing: the key comes from the credential the call picked.
type Adapter struct {
	cloud.Platform
}

var (
	_ adapter.Adapter = (*Adapter)(nil)
	_ adapter.Cloud   = (*Adapter)(nil)
	_ adapter.Signer  = quirks{}
)

// New is the adapter.
func New() *Adapter { return &Adapter{} }

// Kind implements adapter.Adapter.
func (*Adapter) Kind() adapter.Kind { return Kind }

// Type implements adapter.Described: the resource's address and settlement currency are a person's to give, and so are
// its deployments, which are not listed to its key.
func (*Adapter) Type() adapter.Type {
	return adapter.Type{
		Kind: Kind, Mode: adapter.ModeCloud, Name: "Azure OpenAI", Vendor: "azure", Serves: []string{"openai"},
		Group: adapter.GroupCloud, Families: []adapter.Family{adapter.FamilyOpenAI},
		Forms: []adapter.Form{{Name: "api_key", Fields: []adapter.Field{{Name: "api_key", Required: true, Max: 4096}}}},
		Fields: []adapter.Field{
			{Name: "api_version", Pattern: `^[0-9]{4}-[0-9]{2}-[0-9]{2}(-preview)?$`, Max: 18},
			{Name: "deployments", Required: true},
		},
	}
}

// version is the api-version the credential's calls name.
func version(s adapter.Settings) string {
	if v := s["api_version"]; v != "" {
		return v
	}
	return apiVersion
}

// ReadCredential implements adapter.Cloud: the resource's key.
func (*Adapter) ReadCredential(_ string, secrets map[string]string) (adapter.CloudCredential, error) {
	key := secrets["api_key"]
	if len(key) < 8 || strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return adapter.CloudCredential{}, &adapter.FieldError{Field: "api_key", Reason: "is at least 8 characters with no spaces"}
	}
	tail := key[len(key)-4:]
	return adapter.CloudCredential{Key: secret.New(key), Identity: key, Last4: tail, Account: adapter.CloudAccount{KeyLast4: tail}}, nil
}

// models is the resource's list of the models it can deploy, read only to tell the resource's answer from another page.
type models struct {
	Data []struct{} `json:"data"`
}

// CheckRegion implements adapter.Cloud: one request, the list of the models the resource can deploy, which is not
// charged for and answers on the version calls use. It shows the address is a resource that takes the key. It lists no
// models: the deployments are named, and the quality check calls each before it takes traffic.
func (*Adapter) CheckRegion(ctx context.Context, d adapter.Doer, origin, _ string, key secret.Secret, s adapter.Settings, _ time.Time) adapter.CloudCheck {
	ctx, cancel := cloud.Checked(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/openai/models?api-version="+url.QueryEscape(version(s)), nil)
	if err != nil {
		return adapter.CloudCheck{Cause: adapter.CauseUnknown}
	}
	req.Header.Set("Api-Key", key.Reveal())
	resp, check, err := cloud.Fetch(ctx, d, req)
	if err != nil {
		return check
	}
	if check := cloud.Judge(resp.Status); check.Outcome != adapter.KeyPassed {
		return check
	}
	var listed models
	if jsonx.Unmarshal(resp.Body, &listed) != nil || listed.Data == nil {
		return adapter.CloudCheck{Cause: adapter.CauseUnknown, Status: resp.Status}
	}
	return adapter.CloudCheck{Outcome: adapter.KeyPassed, Status: resp.Status}
}

// Offer implements adapter.Cloud: the deployments are named.
func (*Adapter) Offer(catalog.Model, string) (string, bool) { return "", false }

// Formats implements adapter.Cloud: Chat and Responses for a model that generates, embeddings for one that only reads.
func (*Adapter) Formats(m catalog.Model) []protocol.Format {
	items := m.Reportable().Items
	_, direct := m.ServingDirect()
	switch {
	case cloud.Generates(m):
		return []protocol.Format{protocol.Chat, protocol.Responses}
	case !items.Has(catalog.ItemOutput) && items.Has(catalog.ItemInput) && direct:
		return []protocol.Format{protocol.Embeddings}
	}
	return nil
}

// Unmapped implements adapter.Cloud: every deployment is called in Chat.
func (*Adapter) Unmapped() []protocol.Format { return []protocol.Format{protocol.Chat} }

// Calls implements adapter.Adapter.
func (*Adapter) Calls() adapter.CallQuirks { return quirks{} }

type quirks struct{ cloud.Quirks }

// Sign implements adapter.Signer. Chat and embeddings go to the deployment's path, where the deployment, not the body's
// model, says which model answers. Responses go to the resource's v1 path, which takes the deployment as the body's
// model, so the body is sent with the deployment's name in it.
func (quirks) Sign(ctx context.Context, _ adapter.Doer, c adapter.SignedCall) (*http.Request, *adapter.Failure) {
	body, target := c.Body, ""
	switch c.Format {
	case protocol.Chat:
		target = deployment(c, "chat/completions")
	case protocol.Embeddings:
		target = deployment(c, "embeddings")
	case protocol.Responses:
		model, err := jsonx.MarshalCanonical(c.Model)
		if err != nil {
			return nil, cloud.BadBody()
		}
		if body, _, err = cloud.Rewrite(c.Body, nil, cloud.Member{Name: "model", Value: model}); err != nil {
			return nil, cloud.BadBody()
		}
		target = c.Origin + "/openai/v1/responses"
	default:
		return nil, cloud.BadBody()
	}
	req, f := cloud.Post(ctx, target, body, c.Header)
	if f != nil {
		return nil, f
	}
	req.Header.Set("Api-Key", c.Key.Reveal())
	return req, nil
}

// deployment is the address of an operation of the call's deployment, at the credential's api-version.
func deployment(c adapter.SignedCall, operation string) string {
	return c.Origin + "/openai/deployments/" + url.PathEscape(c.Model) + "/" + operation + "?api-version=" + url.QueryEscape(version(c.Settings))
}

// Answer implements adapter.Signer: Azure OpenAI answers in OpenAI's framing.
func (quirks) Answer(resp *http.Response) *http.Response { return resp }

// Classify implements adapter.CallQuirks: OpenAI's error codes, then the status (cloud.Classify). A refusal by the
// resource's content filter is the content's.
func (quirks) Classify(status int, h http.Header, body []byte) adapter.Failure {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	code := ""
	if jsonx.Unmarshal(body, &e) == nil { // a malformed error body still classifies by status
		code = e.Error.Code
	}
	switch {
	case status == http.StatusBadRequest && code == "content_filter":
		return adapter.Failure{Scope: adapter.ScopeRequest, Cause: adapter.ContentRejected{}}
	case status == http.StatusBadRequest && code == "context_length_exceeded":
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.ContextTooLong{}}
	}
	return cloud.Classify(status, h, body)
}
