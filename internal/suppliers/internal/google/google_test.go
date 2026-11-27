package google_test

import (
	"bytes"
	"context"
	"embed"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/google"
)

const origin = "https://generativelanguage.googleapis.com"

// listClient answers every request with one model list and keeps what it was sent.
type listClient struct {
	body []byte
	sent []adapter.Request
}

func (c *listClient) Do(_ context.Context, r adapter.Request) (adapter.Response, error) {
	c.sent = append(c.sent, r)
	return adapter.Response{Status: http.StatusOK, Body: c.body}, nil
}

// A key is checked at the OpenAI-compatible layer's model list, with a bearer token, and named without "models/".
func TestAKeyIsCheckedAtTheCompatibleList(t *testing.T) {
	t.Parallel()
	c := &listClient{body: read(t, "models.json")}
	got := google.New().CheckKey(t.Context(), c, adapter.FamilyOpenAI, secret.New("sk-test-key"), nil)
	if got.Outcome != adapter.KeyPassed || !cmp.Equal(got.Models, []string{"gemini-2.5-flash", "text-embedding-004"}) {
		t.Fatalf("check: %+v", got)
	}
	if r := c.sent[0]; r.Path != "/v1beta/openai/models" || r.Header.Get("Authorization") != "Bearer sk-test-key" {
		t.Errorf("check asked %s with %q", r.Path, r.Header.Get("Authorization"))
	}
}

// chatBody is the Chat request the gateway prepares for the documented example; stream is whether it asks for one.
func chatBody(stream bool) []byte {
	s := "false"
	if stream {
		s = `true,"stream_options":{"include_usage":true}`
	}
	return []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"Explain how AI works in a few words"}],"stream":` + s + `}`)
}

// compatStream is the same reply as testdata/stream.sse in the framing Google's OpenAI-compatible layer gave it, which
// the type was billed by before calls went native: the prompt includes the cached tokens, the completion the thoughts.
const compatStream = `data: {"id":"r1","object":"chat.completion.chunk","created":0,"model":"gemini-2.5-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"AI learns patterns"}}]}

data: {"id":"r1","object":"chat.completion.chunk","created":0,"model":"gemini-2.5-flash","choices":[{"index":0,"delta":{"content":" from data to make predictions."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":36,"total_tokens":46,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":27}}}

data: [DONE]

`

// TestChatGoesNative: Chat is sent to Gemini's generateContent, streamed or whole, with the model in the path and the
// key in x-goog-api-key, as the documented request (testdata/request.json); the answer, streamed or whole
// (testdata/stream.sse, in the shape of Google's documented streamed answer, not recorded against a live key), is
// billed exactly as the same reply was through the OpenAI-compatible layer, the cached and thinking tokens included.
func TestChatGoesNative(t *testing.T) {
	t.Parallel()
	stream := read(t, "stream.sse")
	events := strings.Split(strings.TrimSpace(string(stream)), "\n\n")
	whole := "{\n  " + strings.TrimPrefix(events[len(events)-1], "data: {") + "\n" // indented, as Google writes it
	want := usage(t, io.NopCloser(strings.NewReader(compatStream)))
	for name, tc := range map[string]struct {
		stream bool
		path   string
		answer string
	}{
		"streamed": {true, "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", string(stream)},
		"whole":    {false, "/v1beta/models/gemini-2.5-flash:generateContent", whole},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			signer := google.New().Calls().(adapter.Signer)
			req, f := signer.Sign(t.Context(), nil, adapter.SignedCall{
				Format: protocol.Chat, Origin: origin, Model: "gemini-2.5-flash", Body: chatBody(tc.stream), Key: secret.New("sk-test-key"),
			})
			if f != nil {
				t.Fatalf("sign: %+v", f)
			}
			checkNative(t, req, tc.path)
			resp := signer.Answer(&http.Response{
				StatusCode: http.StatusOK, Request: req, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(tc.answer)),
			})
			if got := usage(t, resp.Body); !cmp.Equal(got, want) {
				t.Errorf("billed %v, through the compatible layer %v", got, want)
			}
			if err := resp.Body.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if want[catalog.ItemCacheRead] != 4 || want[catalog.ItemReasoning] != 27 || want[catalog.ItemInput] != 6 || want[catalog.ItemOutput] != 9 {
		t.Errorf("the compatible layer's reply billed %v", want)
	}
}

// checkNative checks a native request: Google's host and the path, the key in x-goog-api-key alone, the documented body.
func checkNative(t *testing.T, req *http.Request, path string) {
	t.Helper()
	if got := req.URL.RequestURI(); req.URL.Host != "generativelanguage.googleapis.com" || got != path {
		t.Errorf("sent to %s%s, want %s", req.URL.Host, got, path)
	}
	if req.Header.Get("X-Goog-Api-Key") != "sk-test-key" || req.Header.Get("Authorization") != "" {
		t.Errorf("key headers %v", req.Header)
	}
	body, err := io.ReadAll(req.Body)
	if want := bytes.TrimSpace(read(t, "request.json")); err != nil || !bytes.Equal(body, want) {
		t.Errorf("body\n%s (%v)\nwant\n%s", body, err, want)
	}
}

// Embeddings stay on the OpenAI-compatible layer, with a bearer token and the model as the key names it; formats the
// type does not serve are refused before anything is sent.
func TestOtherFormats(t *testing.T) {
	t.Parallel()
	signer := google.New().Calls().(adapter.Signer)
	req, f := signer.Sign(t.Context(), nil, adapter.SignedCall{
		Format: protocol.Embeddings, Origin: origin, Model: "text-embedding-004",
		Body: []byte(`{"model":"embed","input":"hi"}`), Key: secret.New("sk-test-key"),
	})
	if f != nil {
		t.Fatalf("sign: %+v", f)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil || req.URL.Path != "/v1beta/openai/embeddings" || req.Header.Get("Authorization") != "Bearer sk-test-key" ||
		!bytes.Contains(body, []byte(`"model":"text-embedding-004"`)) {
		t.Errorf("embeddings sent to %s with %q: %s", req.URL.Path, req.Header.Get("Authorization"), body)
	}
	resp := &http.Response{Request: req, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}
	answer := signer.Answer(resp)
	if err := answer.Body.Close(); err != nil || answer != resp || resp.Header.Get("Content-Type") != "" {
		t.Error("an embeddings answer was rewritten")
	}
	if _, f := signer.Sign(t.Context(), nil, adapter.SignedCall{Format: protocol.Messages, Origin: origin, Body: []byte(`{}`)}); f == nil {
		t.Error("Messages was signed")
	}
}

// usage is what the relay bills for a Chat stream, item by item.
func usage(t *testing.T, body io.ReadCloser) map[catalog.Item]int64 {
	t.Helper()
	r := protocol.NewRelay(protocol.Chat, protocol.Chat, body, protocol.UsageContext{}, protocol.RelayOptions{Memory: protocol.NewMemory(64 << 20)})
	for {
		step, err := r.Next()
		if err != nil {
			t.Fatalf("relay: %v", err)
		}
		if step.Kind == protocol.StepUpstreamDone {
			break
		}
	}
	u := r.Usage()
	if u.Invalid != nil || len(u.Segments) != 1 {
		t.Fatalf("usage %+v", u)
	}
	out := map[catalog.Item]int64{}
	for it := range catalog.NumItems {
		if n := u.Segments[0].Usage.Count(it); n != 0 {
			out[it] = n
		}
	}
	return out
}

//go:embed testdata
var samples embed.FS

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := samples.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
