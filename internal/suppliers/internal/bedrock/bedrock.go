// Package bedrock is Amazon Bedrock, reached with an access key or an API key (docs/domains/suppliers-direct.md「云平台」):
// Anthropic's models through InvokeModel and InvokeModelWithResponseStream in Messages, the others' through Converse
// and ConverseStream in Chat (converse.go), every request signed with AWS Signature Version 4 (sigv4.go) or carrying
// the API key as a bearer token, and a stream's event-stream framing turned into SSE as it is read (eventstream.go).
// Each region is a credential of its own.
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

// Type implements adapter.Described.
func (*Adapter) Type() adapter.Type {
	return adapter.Type{
		Kind: Kind, Mode: adapter.ModeCloud, Name: "Amazon Bedrock", Vendor: "aws", Serves: []string{"anthropic", "amazon"},
		Group: adapter.GroupCloud, Entries: []adapter.PriceEntry{{ID: "global", Origin: origin, Currency: "USD"}},
		Families: []adapter.Family{adapter.FamilyAnthropic, adapter.FamilyOpenAI}, Lists: true,
		Forms: []adapter.Form{
			{Name: "access_key", Fields: []adapter.Field{
				{Name: "access_key_id", Required: true, Pattern: `^[A-Z0-9]{16,128}$`, Max: 128},
				{Name: "secret_access_key", Required: true, Max: 256},
			}},
			{Name: "api_key", Fields: []adapter.Field{{Name: "api_key", Required: true, Max: 4096}}},
		},
		Fields: []adapter.Field{{Name: "regions", Required: true}},
	}
}

// ReadCredential implements adapter.Cloud: an access key ID and its secret, sealed together, or an API key alone. The
// secret is what is sent; a space or a control character in it is a paste gone wrong.
func (*Adapter) ReadCredential(form string, secrets map[string]string) (adapter.CloudCredential, error) {
	c, field := credential{AccessKeyID: secrets["access_key_id"], SecretAccessKey: secrets["secret_access_key"]}, "secret_access_key"
	if form == "api_key" {
		c, field = credential{APIKey: secrets["api_key"]}, "api_key"
	}
	if strings.ContainsFunc(c.SecretAccessKey+c.APIKey, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return adapter.CloudCredential{}, &adapter.FieldError{Field: field, Reason: "has no spaces"}
	}
	if form == "api_key" && len(c.APIKey) < minAPIKey {
		return adapter.CloudCredential{}, &adapter.FieldError{Field: field, Reason: "is too short to be a Bedrock API key"}
	}
	sealed, err := jsonx.MarshalCanonical(c)
	if err != nil {
		return adapter.CloudCredential{}, &adapter.FieldError{Field: field, Reason: "does not read"}
	}
	if c.APIKey != "" {
		tail := c.APIKey[len(c.APIKey)-4:]
		return adapter.CloudCredential{
			Key: secret.New(string(sealed)), Identity: c.APIKey, Last4: tail, Account: adapter.CloudAccount{KeyLast4: tail},
		}, nil
	}
	tail := c.AccessKeyID[len(c.AccessKeyID)-4:]
	return adapter.CloudCredential{
		Key: secret.New(string(sealed)), Identity: c.AccessKeyID, Last4: tail, Account: adapter.CloudAccount{AccessKeyLast4: tail},
	}, nil
}

// minAPIKey is the shortest API key read, so that the last four shown are a small part of it; Bedrock's are well over a
// hundred characters.
const minAPIKey = 16

// credential is a sealed credential: an access key, which signs each request, or an API key alone, which is sent as a
// bearer token. An access key seals as it did before API keys were taken, so the credentials stored then still open.
type credential struct {
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	APIKey          string `json:"api_key,omitempty"`
}

// credentialOf reads the sealed credential: one of the two kinds, whole.
func credentialOf(key secret.Secret) (credential, bool) {
	var c credential
	if jsonx.Unmarshal([]byte(key.Reveal()), &c) != nil {
		return c, false
	}
	access := c.AccessKeyID != "" && c.SecretAccessKey != ""
	return c, access && c.APIKey == "" || c.APIKey != "" && c.AccessKeyID == "" && c.SecretAccessKey == ""
}

// authorize puts the credential on req for Bedrock in region at at: an API key as a bearer token, an access key as a
// signature over payload, the body req sends.
func (c credential) authorize(req *http.Request, payload []byte, region string, at time.Time) {
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		return
	}
	sign(req, payload, c, region, "bedrock", at)
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

// CheckRegion implements adapter.Cloud: the region's active models of every provider that answer in text
// (ListFoundationModels), and the inference profiles a model the region serves only through one is called by
// (ListInferenceProfiles); listing is not charged for. The key needs bedrock:ListFoundationModels and
// bedrock:ListInferenceProfiles besides the calls'. A region with no such model it serves fails as region_unavailable.
func (*Adapter) CheckRegion(ctx context.Context, d adapter.Doer, _, region string, key secret.Secret, _ adapter.Settings, at time.Time) adapter.CloudCheck {
	ctx, cancel := cloud.Checked(ctx)
	defer cancel()
	k, ok := credentialOf(key)
	if !ok {
		return adapter.CloudCheck{Cause: adapter.CauseUnknown}
	}
	var listed summaries
	if check, ok := list(ctx, d, k, region, "/foundation-models", at, &listed); !ok {
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

// list GETs path of the region's control plane, authorized, into v; false with the check to answer with when it did not
// pass. A key the region refuses (UnrecognizedClientException, InvalidSignatureException) fails.
func list(ctx context.Context, d adapter.Doer, k credential, region, path string, at time.Time, v any) (adapter.CloudCheck, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://bedrock."+region+".amazonaws.com"+path, nil)
	if err != nil {
		return adapter.CloudCheck{Cause: adapter.CauseUnknown}, false
	}
	req.Header.Set("Accept", "application/json")
	k.authorize(req, nil, region, at)
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
func profileRoutes(ctx context.Context, d adapter.Doer, k credential, region string, at time.Time) map[string]string {
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

// versioned is a Bedrock model ID's vendor prefix and version suffix around the name its vendor calls the model. The
// name has no colon: an ID with more after its version (a context window's :200k) or a version of another shape
// (openai.gpt-oss-120b-1:0) does not say where the name ends.
var versioned = regexp.MustCompile(`^[a-z0-9-]+\.([a-z0-9][a-z0-9.-]*?)(-v[0-9]+(:[0-9]+)?)?$`)

// known is the name the catalog knows a Bedrock model by, "" when that cannot be told:
// anthropic.claude-sonnet-4-5-20250929-v1:0 is claude-sonnet-4-5-20250929, amazon.nova-pro-v1:0 is nova-pro.
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

// Formats implements adapter.Cloud, for a model that generates: Messages for Anthropic's, which InvokeModel takes as
// Anthropic's API does; Chat for any other's, which Converse takes once converted.
func (*Adapter) Formats(m catalog.Model) []protocol.Format {
	switch {
	case !cloud.Generates(m):
		return nil
	case m.Author == "anthropic":
		return []protocol.Format{protocol.Messages}
	}
	return []protocol.Format{protocol.Chat}
}

// Unmapped implements adapter.Cloud: a name the catalog does not map yet is called in Messages, as Anthropic's are.
func (*Adapter) Unmapped() []protocol.Format { return []protocol.Format{protocol.Messages} }

// Calls implements adapter.Adapter.
func (*Adapter) Calls() adapter.CallQuirks { return quirks{} }
