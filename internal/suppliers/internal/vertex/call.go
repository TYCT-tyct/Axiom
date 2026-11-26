package vertex

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/cloud"
)

// anthropicVersion is the version of Anthropic's API Vertex AI's Anthropic endpoints take, in the body in place of the
// model, which is in the path (https://docs.anthropic.com/en/api/claude-on-vertex-ai).
var anthropicVersion = []byte(`"vertex-2023-10-16"`)

// accepted is the beta features Vertex AI's (Google managed) Anthropic endpoints take (the client's name for a feature,
// Vertex's name for it): the rest, Claude Code's own among them, fail the call. The list is Anthropic's「Claude Models on
// Google Cloud (Google managed)」feature list and the beta header each of those features documents (1M context,
// context editing, interleaved thinking in manual mode, the computer use tool's two versions, fine-grained tool
// streaming), with tool search, which Claude Code's「Google Cloud's Agent Platform」page turns on for Claude 4.5 and
// later, read 2026-11-26.
var accepted = map[string]string{
	"context-1m-2025-08-07":                  "context-1m-2025-08-07",
	"context-management-2025-06-27":          "context-management-2025-06-27",
	"fine-grained-tool-streaming-2025-05-14": "fine-grained-tool-streaming-2025-05-14",
	"interleaved-thinking-2025-05-14":        "interleaved-thinking-2025-05-14",
	"computer-use-2025-01-24":                "computer-use-2025-01-24",
	"computer-use-2025-11-24":                "computer-use-2025-11-24",
	"tool-search-tool-2025-10-19":            "tool-search-tool-2025-10-19",
	"advanced-tool-use-2025-11-20":           "tool-search-tool-2025-10-19", // Anthropic's tool search, by Vertex's name
}

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
	if r, refused := errors.AsType[*refusal](err); refused && r != nil {
		f := cloud.Rejected()
		return nil, &f
	}
	if err != nil {
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
	case protocol.Responses, protocol.Embeddings, protocol.Rerank, protocol.Completions:
		return nil, cloud.BadBody()
	}
	req, f := cloud.Post(ctx, target, body, c.Header)
	if f != nil {
		return nil, f
	}
	req.Header.Del("Anthropic-Version") // the version is the body's
	req.Header.Del("Anthropic-Beta")
	if betas := cloud.Betas(c.Header.Values("Anthropic-Beta"), accepted); betas != nil {
		req.Header.Set("Anthropic-Beta", strings.Join(betas, ","))
	}
	req.Header.Set("Authorization", "Bearer "+acct.token)
	return req, nil
}

// Answer implements adapter.Signer: Vertex AI answers in the formats' own framing, Anthropic's SSE and OpenAI's.
func (quirks) Answer(resp *http.Response) *http.Response { return resp }
