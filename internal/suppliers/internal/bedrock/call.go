package bedrock

import (
	"context"
	"net/http"
	"path"
	"slices"
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

// actions are, by call format, the runtime's actions a call goes to: the one that answers whole and the one that
// streams. Answer tells a Converse call's answer from InvokeModel's by the action (converses).
var actions = map[protocol.Format][2]string{
	protocol.Messages: {"invoke", "invoke-with-response-stream"},
	protocol.Chat:     {"converse", "converse-stream"},
}

// runtimeURL is where a call of model goes in region: the action, the streaming one for a stream. The model's ID is a
// path segment, its colon encoded.
func runtimeURL(region, model string, f protocol.Format, stream bool) string {
	action := actions[f][0]
	if stream {
		action = actions[f][1]
	}
	return "https://bedrock-runtime." + region + ".amazonaws.com/model/" + escape(model) + "/" + action
}

// Sign implements adapter.Signer, authorized with the region's credential. Messages is the body without its model and
// stream, which the path says, with Bedrock's version, and the beta features the client asked for in anthropic-beta as
// the body's anthropic_beta, where Bedrock reads them. Chat is the body converted to Converse's.
func (quirks) Sign(ctx context.Context, _ adapter.Doer, c adapter.SignedCall) (*http.Request, *adapter.Failure) {
	k, ok := credentialOf(c.Key)
	if !ok {
		return nil, cloud.BadBody()
	}
	var (
		body   []byte
		stream bool
		err    error
	)
	switch c.Format {
	case protocol.Messages:
		add := []cloud.Member{{Name: "anthropic_version", Value: anthropicVersion}}
		if beta := betas(c.Header.Values("Anthropic-Beta")); beta != nil {
			add = append(add, cloud.Member{Name: "anthropic_beta", Value: beta})
		}
		body, stream, err = cloud.Rewrite(c.Body, []string{"model", "stream"}, add...)
	case protocol.Chat:
		body, stream, err = converse(c.Body)
	case protocol.Responses, protocol.Embeddings, protocol.Rerank, protocol.Completions:
		return nil, cloud.BadBody()
	}
	if err != nil {
		return nil, cloud.BadBody()
	}
	req, f := cloud.Post(ctx, runtimeURL(c.Region, c.Model, c.Format, stream), body, nil)
	if f != nil {
		return nil, f
	}
	req.Header.Set("Accept", "application/json")
	if stream {
		req.Header.Set("Accept", eventStream)
	}
	k.authorize(req, body, c.Region, c.At)
	return req, nil
}

// accepted is the beta features Bedrock's InvokeModel takes (the client's name for a feature, Bedrock's name for it):
// the rest, Claude Code's own among them, fail the call. The list is AWS's「Anthropic Claude Messages API: request and
// response」(its anthropic_beta table) and「Anthropic Claude tool use」(the computer-use pairings and the legacy
// fine-grained streaming header), read 2026-11-26; compaction's header is the one Bedrock passes through for the
// compaction it serves. Tool search is GA there, so the earlier name still works and Anthropic's newer one maps to it.
var accepted = map[string]string{
	"computer-use-2025-01-24":                "computer-use-2025-01-24",
	"computer-use-2025-11-24":                "computer-use-2025-11-24",
	"token-efficient-tools-2025-02-19":       "token-efficient-tools-2025-02-19",
	"interleaved-thinking-2025-05-14":        "interleaved-thinking-2025-05-14",
	"output-128k-2025-02-19":                 "output-128k-2025-02-19",
	"dev-full-thinking-2025-05-14":           "dev-full-thinking-2025-05-14",
	"context-1m-2025-08-07":                  "context-1m-2025-08-07",
	"context-management-2025-06-27":          "context-management-2025-06-27",
	"effort-2025-11-24":                      "effort-2025-11-24",
	"compact-2026-01-12":                     "compact-2026-01-12",
	"fine-grained-tool-streaming-2025-05-14": "fine-grained-tool-streaming-2025-05-14",
	"tool-search-tool-2025-10-19":            "tool-search-tool-2025-10-19",
	"tool-examples-2025-10-29":               "tool-examples-2025-10-29",
	"advanced-tool-use-2025-11-20":           "tool-search-tool-2025-10-19", // Anthropic's tool search is Bedrock's
}

// betas is the anthropic-beta header's features that Bedrock takes, as a JSON array; nil for none. Tool search comes
// with its examples, which Bedrock wants beside it.
func betas(values []string) []byte {
	features := cloud.Betas(values, accepted)
	if slices.Contains(features, "tool-search-tool-2025-10-19") && !slices.Contains(features, "tool-examples-2025-10-29") {
		features = append(features, "tool-examples-2025-10-29")
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

// Answer implements adapter.Signer: a stream's event-stream framing becomes SSE as it is read, Anthropic's for
// InvokeModel and Chat's for Converse; Converse's whole answer becomes Chat's. InvokeModel's whole answer is Anthropic's
// already.
func (quirks) Answer(resp *http.Response) *http.Response {
	stream := strings.HasPrefix(resp.Header.Get("Content-Type"), eventStream)
	switch converse := converses(resp); {
	case converse && stream:
		resp.Body = newChatSSE(resp.Body, chatHeadOf(resp))
		resp.Header.Set("Content-Type", "text/event-stream")
	case converse:
		resp.Body = newChatAnswer(resp.Body, chatHeadOf(resp))
		resp.Header.Set("Content-Type", "application/json")
	case stream:
		resp.Body = newSSE(resp.Body)
		resp.Header.Set("Content-Type", "text/event-stream")
	default:
		return resp
	}
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	return resp
}

// converses says resp answers a Converse call. Answer is given only the response, and both actions answer in the same
// media types, so the request it answers tells: the outbound client's transport (net/http's) sets resp.Request to the
// request Sign made, whose path ends in the action. Reading it keeps the Signer interface as it is, where marking
// the call some other way would widen it for one platform. A response without its request is InvokeModel's, the
// format every Bedrock call was before Converse.
func converses(resp *http.Response) bool {
	if resp.Request == nil {
		return false
	}
	action := path.Base(resp.Request.URL.Path)
	return action == actions[protocol.Chat][0] || action == actions[protocol.Chat][1]
}

// chatHeadOf is what each Chat answer or chunk starts with: Converse answers with none of it, so the id is Bedrock's
// request ID, the model the one the path names and the time the response's Date. The gateway writes its own id and
// model over them.
func chatHeadOf(resp *http.Response) chatHead {
	h := chatHead{ID: "chatcmpl-" + resp.Header.Get("X-Amzn-Requestid"), Object: "chat.completion"}
	if at, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		h.Created = at.Unix()
	}
	h.Model = strings.TrimPrefix(path.Dir(resp.Request.URL.Path), "/model/")
	return h
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
