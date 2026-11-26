package bedrock

import (
	"context"
	"net/http"
	"strings"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/cloud"
)

// anthropicVersion is the version of Anthropic's API Bedrock takes, in the body in place of the model, which is in the
// path (https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages.html).
var anthropicVersion = []byte(`"bedrock-2023-05-31"`)

// eventStream is the media type of Bedrock's streamed answer.
const eventStream = "application/vnd.amazon.eventstream"

type quirks struct{ cloud.Quirks }

// invokeURL is where a call of model goes in region: InvokeModel, or InvokeModelWithResponseStream for a stream. The
// model's ID is a path segment, its colon encoded.
func invokeURL(region, model string, stream bool) string {
	action := "/invoke"
	if stream {
		action = "/invoke-with-response-stream"
	}
	return "https://bedrock-runtime." + region + ".amazonaws.com/model/" + escape(model) + action
}

// Sign implements adapter.Signer: the Messages body without its model and stream, which the path says, with Bedrock's
// version, and the beta features the client asked for in anthropic-beta as the body's anthropic_beta, where Bedrock
// reads them; signed with the region's key.
func (quirks) Sign(ctx context.Context, _ adapter.Doer, c adapter.SignedCall) (*http.Request, *adapter.Failure) {
	k, ok := keysOf(c.Key)
	if c.Format != protocol.Messages || !ok {
		return nil, cloud.BadBody()
	}
	add := []cloud.Member{{Name: "anthropic_version", Value: anthropicVersion}}
	if beta := betas(c.Header.Values("Anthropic-Beta")); beta != nil {
		add = append(add, cloud.Member{Name: "anthropic_beta", Value: beta})
	}
	body, stream, err := cloud.Rewrite(c.Body, []string{"model", "stream"}, add...)
	if err != nil {
		return nil, cloud.BadBody()
	}
	req, f := cloud.Post(ctx, invokeURL(c.Region, c.Model, stream), body, nil)
	if f != nil {
		return nil, f
	}
	req.Header.Set("Accept", "application/json")
	if stream {
		req.Header.Set("Accept", eventStream)
	}
	sign(req, body, k, c.Region, "bedrock", c.At)
	return req, nil
}

// betas is the anthropic-beta header's features as a JSON array; nil for none.
func betas(values []string) []byte {
	var features []string
	for _, v := range values {
		for f := range strings.SplitSeq(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				features = append(features, f)
			}
		}
	}
	if len(features) == 0 {
		return nil
	}
	b, err := jsonx.MarshalCanonical(features)
	if err != nil {
		return nil
	}
	return b
}

// Answer implements adapter.Signer: a stream's event-stream framing becomes Anthropic's SSE as it is read.
func (quirks) Answer(resp *http.Response) *http.Response {
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), eventStream) {
		return resp
	}
	resp.Body = newSSE(resp.Body)
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	return resp
}

// Classify implements adapter.CallQuirks. AWS answers a key it does not know with 403 and the error type in a
// header, which is the credential refused (refusesKey); its other errors by status, a ValidationException that says
// the input is too long being the model's context.
func (quirks) Classify(status int, h http.Header, body []byte) adapter.Failure {
	kind, _, _ := strings.Cut(h.Get("X-Amzn-Errortype"), ":")
	switch {
	case refusesKey(status, h):
		return cloud.Rejected()
	case kind == "ThrottlingException", kind == "ServiceQuotaExceededException":
		return adapter.Failure{Scope: adapter.ScopeCredential, Cause: adapter.RateLimited{RetryAfter: adapter.RetryAfter(h)}}
	case kind == "ModelNotReadyException":
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Overloaded{}}
	case status == http.StatusBadRequest && strings.Contains(strings.ToLower(string(body)), "too long"):
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.ContextTooLong{}}
	}
	return cloud.Classify(status, h, body)
}
