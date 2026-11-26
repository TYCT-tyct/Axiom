// Package bedrock is Amazon Bedrock, reached with an access key (docs/domains/suppliers.md「云平台」): Anthropic's models
// through InvokeModel and InvokeModelWithResponseStream in Messages, every request signed with AWS Signature Version 4
// (sigv4.go), and a stream's event-stream framing turned into Anthropic's SSE as it is read (eventstream.go). Each
// region is a credential of its own.
package bedrock

import (
	"context"
	"net/http"
	"regexp"
	"slices"
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
const Kind adapter.Kind = "bedrock"

// origin is what the supplier shows as its address; calls go to each region's own host (invokeURL).
const origin = "https://bedrock-runtime.amazonaws.com"

// Adapter is Bedrock's adapter. It holds nothing: the key comes from the credential the call picked.
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

// Origin implements adapter.Cloud.
func (*Adapter) Origin() string { return origin }

// Regional implements adapter.Cloud.
func (*Adapter) Regional() bool { return true }

// accessKeyID is an access key ID's form: upper-case letters and digits.
var accessKeyID = regexp.MustCompile(`^[A-Z0-9]{16,128}$`)

// ReadCredential implements adapter.Cloud: an access key ID and its secret, sealed together.
func (*Adapter) ReadCredential(in adapter.CloudInput) (adapter.CloudCredential, error) {
	switch {
	case in.ServiceAccountJSON != "" || in.APIKey != "":
		return adapter.CloudCredential{}, &adapter.CredentialFormError{Member: "access_key_id", Reason: "and secret_access_key are the only members Bedrock takes"}
	case !accessKeyID.MatchString(in.AccessKeyID):
		return adapter.CloudCredential{}, &adapter.CredentialFormError{Member: "access_key_id", Reason: "is 16 to 128 upper-case letters and digits"}
	case in.SecretAccessKey == "" || strings.ContainsFunc(in.SecretAccessKey, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return adapter.CloudCredential{}, &adapter.CredentialFormError{Member: "secret_access_key", Reason: "is required and has no spaces"}
	}
	sealed, err := jsonx.MarshalCanonical(keys{AccessKeyID: in.AccessKeyID, SecretAccessKey: in.SecretAccessKey})
	if err != nil {
		return adapter.CloudCredential{}, &adapter.CredentialFormError{Member: "access_key_id", Reason: "does not read"}
	}
	tail := in.AccessKeyID[len(in.AccessKeyID)-4:]
	return adapter.CloudCredential{
		Key: secret.New(string(sealed)), Identity: in.AccessKeyID, Last4: tail, Account: adapter.CloudAccount{AccessKeyLast4: tail},
	}, nil
}

// keysOf reads the sealed access key.
func keysOf(key secret.Secret) (keys, bool) {
	var k keys
	return k, jsonx.Unmarshal([]byte(key.Reveal()), &k) == nil && k.AccessKeyID != "" && k.SecretAccessKey != ""
}

// summaries is ListFoundationModels' answer, as far as it is read.
type summaries struct {
	ModelSummaries []struct {
		ModelID                 string   `json:"modelId"`
		OutputModalities        []string `json:"outputModalities"`
		InferenceTypesSupported []string `json:"inferenceTypesSupported"`
		ModelLifecycle          struct {
			Status string `json:"status"`
		} `json:"modelLifecycle"`
	} `json:"modelSummaries"`
}

// profiles is a page of ListInferenceProfiles' answer, as far as it is read.
type profiles struct {
	InferenceProfileSummaries []struct {
		InferenceProfileID string `json:"inferenceProfileId"`
		Status             string `json:"status"`
	} `json:"inferenceProfileSummaries"`
	NextToken string `json:"nextToken"`
}

// maxProfilePages is the most pages of inference profiles a check reads; a region has a few dozen profiles.
const maxProfilePages = 5

// CheckRegion implements adapter.Cloud: the region's Anthropic models (ListFoundationModels), and the inference
// profiles a model the region serves only through one is called by (ListInferenceProfiles); listing is not charged
// for. The key needs bedrock:ListFoundationModels and bedrock:ListInferenceProfiles besides the calls'. A region with
// no Anthropic model it serves fails as region_unavailable.
func (*Adapter) CheckRegion(ctx context.Context, d adapter.Doer, _, region string, key secret.Secret, at time.Time) adapter.CloudCheck {
	ctx, cancel := cloud.Checked(ctx)
	defer cancel()
	k, ok := keysOf(key)
	if !ok {
		return adapter.CloudCheck{Cause: adapter.CauseUnknown}
	}
	var listed summaries
	if check, ok := list(ctx, d, k, region, "/foundation-models?byProvider=Anthropic", at, &listed); !ok {
		return check
	}
	routes := profileRoutes(ctx, d, k, region, at)
	var models []adapter.CloudModel
	for _, m := range listed.ModelSummaries {
		if m.ModelLifecycle.Status != "ACTIVE" || !slices.Contains(m.OutputModalities, "TEXT") {
			continue
		}
		model := adapter.CloudModel{Name: m.ModelID, Known: known(m.ModelID)}
		if !slices.Contains(m.InferenceTypesSupported, "ON_DEMAND") {
			if model.Call = routes[m.ModelID]; model.Call == "" {
				continue // served only through a profile, and the region has none for it
			}
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return adapter.CloudCheck{Outcome: adapter.KeyFailed, Cause: adapter.CauseRegionUnavailable, Status: http.StatusOK}
	}
	return adapter.CloudCheck{Outcome: adapter.KeyPassed, Status: http.StatusOK, Models: models}
}

// list GETs path of the region's control plane, signed, into v; false with the check to answer with when it did not
// pass. A key the region refuses (UnrecognizedClientException, InvalidSignatureException) fails.
func list(ctx context.Context, d adapter.Doer, k keys, region, path string, at time.Time, v any) (adapter.CloudCheck, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://bedrock."+region+".amazonaws.com"+path, nil)
	if err != nil {
		return adapter.CloudCheck{Cause: adapter.CauseUnknown}, false
	}
	req.Header.Set("Accept", "application/json")
	sign(req, nil, k, region, "bedrock", at)
	resp, check, err := cloud.Fetch(ctx, d, req)
	switch {
	case err != nil:
		return check, false
	case refusesKey(resp.Status, resp.Header):
		return adapter.CloudCheck{Outcome: adapter.KeyFailed, Cause: adapter.CauseRejected, Status: resp.Status}, false
	}
	if check := cloud.Judge(resp.Status); check.Outcome != adapter.KeyPassed {
		return check, false
	}
	if jsonx.Unmarshal(resp.Body, v) != nil {
		return adapter.CloudCheck{Cause: adapter.CauseUnknown, Status: resp.Status}, false
	}
	return adapter.CloudCheck{}, true
}

// profileRoutes is, by model ID, the system inference profile the region calls it through: a geography's profile
// (us., eu., apac.) before the global one. A region whose profiles cannot be read routes none.
func profileRoutes(ctx context.Context, d adapter.Doer, k keys, region string, at time.Time) map[string]string {
	routes := map[string]string{}
	next := ""
	for range maxProfilePages {
		var page profiles
		path := "/inference-profiles?maxResults=1000&typeEquals=SYSTEM_DEFINED"
		if next != "" {
			path += "&nextToken=" + escape(next)
		}
		if _, ok := list(ctx, d, k, region, path, at, &page); !ok {
			return routes
		}
		page.route(routes)
		if next = page.NextToken; next == "" {
			return routes
		}
	}
	return routes
}

// route adds the page's active profiles to routes, a geography's in place of the global one.
func (page profiles) route(routes map[string]string) {
	for _, p := range page.InferenceProfileSummaries {
		geography, model, found := strings.Cut(p.InferenceProfileID, ".")
		if !found || p.Status != "ACTIVE" {
			continue
		}
		if current, taken := routes[model]; !taken || strings.HasPrefix(current, "global.") && geography != "global" {
			routes[model] = p.InferenceProfileID
		}
	}
}

// versioned is a Bedrock model ID's vendor prefix and version suffix around the name Anthropic calls the model.
var versioned = regexp.MustCompile(`^anthropic\.(.+?)(-v[0-9]+(:[0-9]+)?)?$`)

// known is the name the catalog knows a Bedrock model by: anthropic.claude-sonnet-4-5-20250929-v1:0 is
// claude-sonnet-4-5-20250929.
func known(id string) string {
	if m := versioned.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	return ""
}

// refusesKey is a 403 by which AWS says the key itself is not valid, which the error type header names; AWS answers
// a key it does not know with 403, not 401. AccessDeniedException is a permission of a valid key, and is not this.
func refusesKey(status int, h http.Header) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	if status != http.StatusForbidden {
		return false
	}
	kind, _, _ := strings.Cut(h.Get("X-Amzn-Errortype"), ":")
	return kind == "UnrecognizedClientException" || kind == "InvalidSignatureException"
}

// Offer implements adapter.Cloud: a check lists the models.
func (*Adapter) Offer(catalog.Model, string) (string, bool) { return "", false }

// Formats implements adapter.Cloud: Messages, for a model that generates.
func (*Adapter) Formats(m catalog.Model) []protocol.Format {
	if cloud.Generates(m) {
		return []protocol.Format{protocol.Messages}
	}
	return nil
}

// Unmapped implements adapter.Cloud: every model is called in Messages.
func (*Adapter) Unmapped() []protocol.Format { return []protocol.Format{protocol.Messages} }

// Calls implements adapter.Adapter.
func (*Adapter) Calls() adapter.CallQuirks { return quirks{} }
