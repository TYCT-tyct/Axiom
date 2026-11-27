// Package google is Google AI Studio's Gemini API, reached with pasted keys (docs/domains/suppliers-direct.md「直连接口」):
// its keys and model list are those of Google's OpenAI-compatible layer, as for a row of the vendors table, but Chat
// goes to Gemini's own generateContent, which is a protocol of its own and so a package of its own.
package google

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/cloud"
	"axiom/internal/suppliers/internal/direct"
)

// compatRoot is where Google serves the OpenAI formats: the model list a key is checked at, and embeddings.
const compatRoot = "/v1beta/openai"

// Adapter is the direct adapter of Google's OpenAI-compatible layer, whose calls are made natively (quirks).
type Adapter struct {
	*direct.Adapter
}

var _ adapter.Direct = Adapter{}

// New is the adapter.
func New() Adapter {
	return Adapter{direct.New(direct.Provider{
		Type: adapter.Type{
			Kind: "google", Name: "Google Gemini", Vendor: "google", Serves: []string{"google"}, Group: adapter.GroupOfficial,
			Entries:  []adapter.PriceEntry{{ID: "global", Origin: "https://generativelanguage.googleapis.com", Currency: "USD"}},
			Families: []adapter.Family{adapter.FamilyOpenAI}, Forms: []adapter.Form{direct.Keys}, Lists: true,
		},
		Root: compatRoot,
	})}
}

// Calls implements adapter.Adapter: the provider's policy, classification and charge reference, with the request
// made here (adapter.Signer) rather than at a path and header (adapter.Wire).
func (a Adapter) Calls() adapter.CallQuirks { return quirks{a.Adapter.Calls()} }

type quirks struct{ adapter.CallQuirks }

var _ adapter.Signer = quirks{}

// Sign implements adapter.Signer. Chat becomes Gemini's generateContent body for the model the key names, posted to
// its streamGenerateContent?alt=sse for a stream and generateContent otherwise, with the key in x-goog-api-key
// (https://ai.google.dev/api/generate-content). Embeddings stay on the OpenAI-compatible layer, with the key as a bearer
// token. The gateway read the body as sound Chat, so a Chat request Gemini cannot say is the endpoint's failure, not the
// request's: nothing is sent and another endpoint is tried.
func (quirks) Sign(ctx context.Context, _ adapter.Doer, c adapter.SignedCall) (*http.Request, *adapter.Failure) {
	switch c.Format {
	case protocol.Chat:
		var head struct {
			Stream bool `json:"stream"`
		}
		if err := jsonx.Unmarshal(c.Body, &head); err != nil {
			return nil, cloud.BadBody()
		}
		body, err := protocol.ChatToGemini(c.Body, c.Model)
		if err != nil {
			return nil, &adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeEndpoint, Cause: adapter.BadRequest{}}
		}
		action := ":generateContent"
		if head.Stream {
			action = ":streamGenerateContent?alt=sse"
		}
		req, f := cloud.Post(ctx, c.Origin+"/v1beta/models/"+url.PathEscape(c.Model)+action, body, nil)
		if f != nil {
			return nil, f
		}
		req.Header.Set("X-Goog-Api-Key", c.Key.Reveal())
		return req, nil
	case protocol.Embeddings:
		model, err := jsonx.MarshalCanonical(c.Model)
		if err != nil {
			return nil, cloud.BadBody()
		}
		body, _, err := cloud.Rewrite(c.Body, nil, cloud.Member{Name: "model", Value: model})
		if err != nil {
			return nil, cloud.BadBody()
		}
		req, f := cloud.Post(ctx, c.Origin+compatRoot+"/embeddings", body, c.Header)
		if f != nil {
			return nil, f
		}
		req.Header.Set("Authorization", "Bearer "+c.Key.Reveal())
		return req, nil
	case protocol.Messages, protocol.Responses, protocol.Rerank, protocol.Completions:
	}
	return nil, cloud.BadBody()
}

// Answer implements adapter.Signer: Gemini's answer becomes the Chat stream the relay reads from every Chat endpoint
// (protocol.ChatFromGemini), with Gemini's usage. A whole generateContent answer is one GenerateContentResponse, the
// shape of each streamed event, so it is read as a stream of that one event. Embeddings answer in OpenAI's framing
// already. Which call a response answers is read from its request's path, as bedrock's Answer does.
func (quirks) Answer(resp *http.Response) *http.Response {
	if resp.Request == nil || !strings.Contains(resp.Request.URL.Path, ":") {
		return resp
	}
	body := resp.Body
	if resp.Request.URL.Query().Get("alt") != "sse" {
		body = event(resp.Body)
	}
	resp.Body = protocol.ChatFromGemini(body)
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	return resp
}

// maxAnswer is the most of a whole answer read, as much as one streamed event may hold.
const maxAnswer = 32 << 20

// event is a whole answer as the one event of a stream. Google writes it indented; a line break in JSON is only ever
// whitespace between tokens (one in a string is escaped), so dropping them puts it on the one data line an event is.
// A body that fails to read ends the stream with that failure, after what was read.
func event(body io.ReadCloser) io.ReadCloser {
	v, err := io.ReadAll(io.LimitReader(body, maxAnswer))
	v = bytes.ReplaceAll(bytes.ReplaceAll(v, []byte("\r"), nil), []byte("\n"), nil)
	end := io.Reader(strings.NewReader("\n\n"))
	if err != nil {
		end = failing{err}
	}
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(strings.NewReader("data: "), bytes.NewReader(v), end), body}
}

// failing is a reader that fails with err.
type failing struct{ err error }

func (f failing) Read([]byte) (int, error) { return 0, f.err }
