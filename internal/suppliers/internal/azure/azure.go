// Package azure is Azure OpenAI, reached with a resource's address and key (docs/domains/suppliers.md「云平台」): the
// deployments a person names are its models, each called in OpenAI's Chat at the deployment's path with an api-version,
// the key in the api-key header. A resource has no region to choose: it is one credential.
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

// apiVersion is the data plane's version calls and the check name: the generally available one that streams usage.
// No version after 2022-12-01 lists a resource's deployments to its key (listing moved to Azure Resource Manager, which
// takes an Entra ID token), so the check does not list them and a person names them.
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

// Origin implements adapter.Cloud: the resource's address is a person's to give.
func (*Adapter) Origin() string { return "" }

// Regional implements adapter.Cloud.
func (*Adapter) Regional() bool { return false }

// Named implements adapter.Cloud: a resource's deployments are not listed to its key.
func (*Adapter) Named() bool { return true }

// ReadCredential implements adapter.Cloud: the resource's key.
func (*Adapter) ReadCredential(in adapter.CloudInput) (adapter.CloudCredential, error) {
	switch {
	case in.ServiceAccountJSON != "" || in.AccessKeyID != "" || in.SecretAccessKey != "":
		return adapter.CloudCredential{}, &adapter.CredentialFormError{Member: "api_key", Reason: "is the only member Azure OpenAI takes"}
	case len(in.APIKey) < 8 || strings.ContainsFunc(in.APIKey, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return adapter.CloudCredential{}, &adapter.CredentialFormError{Member: "api_key", Reason: "is at least 8 characters with no spaces"}
	}
	tail := in.APIKey[len(in.APIKey)-4:]
	return adapter.CloudCredential{Key: secret.New(in.APIKey), Identity: in.APIKey, Last4: tail, Account: adapter.CloudAccount{KeyLast4: tail}}, nil
}

// models is the resource's list of the models it can deploy, read only to tell the resource's answer from another page.
type models struct {
	Data []struct{} `json:"data"`
}

// CheckRegion implements adapter.Cloud: one request, the list of the models the resource can deploy, which is not
// charged for and answers on the version calls use. It shows the address is a resource that takes the key. It lists no
// models: the deployments are named, and the quality check calls each before it takes traffic.
func (*Adapter) CheckRegion(ctx context.Context, d adapter.Doer, origin, _ string, key secret.Secret, _ time.Time) adapter.CloudCheck {
	ctx, cancel := cloud.Checked(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/openai/models?api-version="+apiVersion, nil)
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

// Formats implements adapter.Cloud: Chat, for a model that generates.
func (*Adapter) Formats(m catalog.Model) []protocol.Format {
	if cloud.Generates(m) {
		return []protocol.Format{protocol.Chat}
	}
	return nil
}

// Unmapped implements adapter.Cloud: every deployment is called in Chat.
func (*Adapter) Unmapped() []protocol.Format { return []protocol.Format{protocol.Chat} }

// Calls implements adapter.Adapter.
func (*Adapter) Calls() adapter.CallQuirks { return quirks{} }

type quirks struct{ cloud.Quirks }

// Sign implements adapter.Signer: Chat at the deployment's path, as it is; the deployment, not the body's model, says
// which model answers.
func (quirks) Sign(ctx context.Context, _ adapter.Doer, c adapter.SignedCall) (*http.Request, *adapter.Failure) {
	if c.Format != protocol.Chat {
		return nil, cloud.BadBody()
	}
	target := c.Origin + "/openai/deployments/" + url.PathEscape(c.Model) + "/chat/completions?api-version=" + apiVersion
	req, f := cloud.Post(ctx, target, c.Body, c.Header)
	if f != nil {
		return nil, f
	}
	req.Header.Set("Api-Key", c.Key.Reveal())
	return req, nil
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
