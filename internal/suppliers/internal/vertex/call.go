package vertex

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/cloud"
)

// anthropicVersion is the version of Anthropic's API Vertex AI's Anthropic endpoints take, in the body in place of the
// model, which is in the path (https://docs.anthropic.com/en/api/claude-on-vertex-ai).
var anthropicVersion = []byte(`"vertex-2023-10-16"`)

type quirks struct {
	cloud.Quirks
	a *Adapter
}

// Classify implements adapter.CallQuirks: Google's errors read by their status (cloud.Classify), a 401 being the
// token refused.
func (quirks) Classify(status int, h http.Header, body []byte) adapter.Failure {
	return cloud.Classify(status, h, body)
}

// Sign implements adapter.Signer. Messages go to the model's rawPredict, or streamRawPredict for a stream, with the
// model out of the body and the version in it; Chat goes to the OpenAI-compatible endpoint as it is, the model (google/
// and its name) in the body. A token the key is refused for fails the call as the credential's, which marks the region's
// credential rejected.
func (q quirks) Sign(ctx context.Context, d adapter.Doer, c adapter.SignedCall) (*http.Request, *adapter.Failure) {
	var (
		target string
		body   = c.Body
	)
	acct, err := q.a.tokens.get(ctx, d, c.Key, c.At)
	var refused *refusal
	switch {
	case errors.As(err, &refused):
		f := cloud.Rejected()
		return nil, &f
	case err != nil:
		return nil, cloud.Unsigned()
	}
	switch c.Format {
	case protocol.Messages:
		var stream bool
		if body, stream, err = cloud.Rewrite(c.Body, []string{"model"}, cloud.Member{Name: "anthropic_version", Value: anthropicVersion}); err != nil {
			return nil, cloud.BadBody()
		}
		action := ":rawPredict"
		if stream {
			action = ":streamRawPredict"
		}
		target = base(acct.project, c.Region) + "/publishers/anthropic/models/" + url.PathEscape(c.Model) + action
	case protocol.Chat:
		target = base(acct.project, c.Region) + "/endpoints/openapi/chat/completions"
	default:
		return nil, cloud.BadBody()
	}
	req, f := cloud.Post(ctx, target, body, c.Header)
	if f != nil {
		return nil, f
	}
	req.Header.Del("Anthropic-Version") // the version is the body's
	req.Header.Set("Authorization", "Bearer "+acct.token)
	return req, nil
}

// Answer implements adapter.Signer: Vertex AI answers in the formats' own framing, Anthropic's SSE and OpenAI's.
func (quirks) Answer(resp *http.Response) *http.Response { return resp }
