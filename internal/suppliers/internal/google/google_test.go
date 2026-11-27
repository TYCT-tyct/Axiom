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
	if err := google.New().Type().Check(); err != nil {
		t.Fatalf("type: %v", err)
	}
	c := &listClient{body: read(t, "models.json")}
	got := google.New().CheckKey(t.Context(), c, adapter.FamilyOpenAI, secret.New("sk-test-key"), nil)
	if got.Outcome != adapter.KeyPassed || !cmp.Equal(got.Models, []string{"gemini-2.5-flash", "text-embedding-004"}) {
		t.Fatalf("check: %+v", got)
	}
	if r := c.sent[0]; r.Path != "/v1beta/openai/models" || r.Header.Get("Authorization") != "Bearer sk-test-key" {
		t.Errorf("check asked %s with %q", r.Path, r.Header.Get("Authorization"))
	}
}

// chatBody is the Chat request the gateway prepares for Google's documented quickstart request; stream is whether it
// asks for one.
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
// key in x-goog-api-key, as Google's documented request (testdata/request.json); the answer, streamed or whole
// (testdata/stream.sse, in the shape of Google's documented GenerateContentResponse, not recorded against a live key),
// is billed exactly as the same reply was through the OpenAI-compatible layer, the cached and thinking tokens and the
// tier Google served it at included: the reply's own, or the x-gemini-service-tier header's for a priority request
// Google served at the standard tier. A whole answer is read by the relay, not while Answer runs.
func TestChatGoesNative(t *testing.T) {
	t.Parallel()
	native := string(read(t, "stream.sse"))
	for _, tier := range []struct {
		name, compat, native, header string
		asked                        catalog.ServiceTier
	}{
		{"standard", compatStream, native, "", catalog.ServiceTierStandard},
		{
			"priority",
			strings.Replace(compatStream, `"usage":`, `"service_tier":"priority","usage":`, 1),
			strings.Replace(native, `"thoughtsTokenCount": 27`, `"thoughtsTokenCount": 27,"serviceTier": "priority"`, 1),
			"", catalog.ServiceTierStandard,
		},
		{"downgraded", strings.Replace(compatStream, `"usage":`, `"service_tier":"default","usage":`, 1), native, "standard", catalog.ServiceTierPriority},
	} {
		want, wantTier := billed(t, io.NopCloser(strings.NewReader(tier.compat)), tier.asked)
		events := strings.Split(strings.TrimSpace(tier.native), "\n\n")
		whole := "{\n  " + strings.TrimPrefix(events[len(events)-1], "data: {") + "\n" // indented, as Google writes it
		for name, tc := range map[string]struct {
			stream bool
			path   string
			answer string
		}{
			"streamed": {true, "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", tier.native},
			"whole":    {false, "/v1beta/models/gemini-2.5-flash:generateContent", whole},
		} {
			t.Run(tier.name+" "+name, func(t *testing.T) {
				t.Parallel()
				got, gotTier := callNative(t, tc.stream, tc.path, tc.answer, tier.header, tier.asked)
				if !cmp.Equal(got, want) || gotTier != wantTier {
					t.Errorf("billed %v at %d, through the compatible layer %v at %d", got, gotTier, want, wantTier)
				}
			})
		}
	}
	if want, _ := billed(t, io.NopCloser(strings.NewReader(compatStream)), catalog.ServiceTierStandard); want[catalog.ItemCacheRead] != 4 ||
		want[catalog.ItemReasoning] != 27 || want[catalog.ItemInput] != 6 || want[catalog.ItemOutput] != 9 {
		t.Errorf("the compatible layer's reply billed %v", want)
	}
}

// A Chat request the native conversion cannot say exactly goes, in the same attempt, to Google's OpenAI-compatible
// layer, as every Chat request did before calls went native: with the model the key names and the key as a bearer
// token. Its answer is the compatible layer's, unchanged, so it is billed as it was.
func TestChatFallsBackToTheCompatibleLayer(t *testing.T) {
	t.Parallel()
	hi := `"messages":[{"role":"user","content":"Hi"}]`
	tool := `"tools":[{"type":"function","function":{"name":"f","strict":true}},{"type":"function","function":{"name":"g"}}]`
	want, _ := billed(t, io.NopCloser(strings.NewReader(compatStream)), catalog.ServiceTierStandard)
	for name, c := range map[string]struct{ model, members string }{
		"an image by address": {"gemini-2.5-flash", `"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/i.png"}}]}]`},
		"an image's detail": {
			"gemini-2.5-flash",
			`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"high"}}]}]`,
		},
		"audio":                            {"gemini-2.5-flash", `"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]`},
		"a cached content":                 {"gemini-2.5-flash", hi + `,"extra_body":{"google":{"cached_content":"cachedContents/0000aaaa1111bbbb2222cccc3333dddd4444eeee"}}`},
		"a thinking config":                {"gemini-2.5-flash", hi + `,"extra_body":{"google":{"thinking_config":{"thinking_budget":2048,"include_thoughts":true}}}`},
		"mixed strictness":                 {"gemini-2.5-flash", hi + "," + tool},
		"one call at a time":               {"gemini-2.5-flash", hi + `,"tools":[{"type":"function","function":{"name":"f"}}],"parallel_tool_calls":false`},
		"a level the model lacks":          {"gemini-3.1-pro-preview", hi + `,"reasoning_effort":"minimal"`},
		"thinking on at a default of off":  {"gemini-2.5-flash-lite", hi + `,"reasoning":{"enabled":true,"exclude":true}`},
		"a model not in the table":         {"gemini-2.5-flash-preview-09-2025", hi + `,"reasoning_effort":"low"`},
		"an output past one request's cap": {"gemini-4-neon", hi + `,"max_tokens":70000,"stream":true`},
	} {
		signer := google.New().Calls().(adapter.Signer)
		req, f := signer.Sign(t.Context(), nil, adapter.SignedCall{
			Format: protocol.Chat, Origin: origin, Model: c.model, Body: []byte(`{"model":"endpoint-name",` + c.members + `}`), Key: secret.New("sk-test-key"),
		})
		if f != nil {
			t.Fatalf("%s: sign: %+v", name, f)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil || req.URL.RequestURI() != "/v1beta/openai/chat/completions" || req.Header.Get("Authorization") != "Bearer sk-test-key" ||
			!bytes.Contains(body, []byte(`"model":"`+c.model+`"`)) {
			t.Errorf("%s: sent to %s with %q: %s", name, req.URL.RequestURI(), req.Header.Get("Authorization"), body)
		}
		resp := signer.Answer(&http.Response{StatusCode: http.StatusOK, Request: req, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(compatStream))})
		if got, _ := billed(t, resp.Body, catalog.ServiceTierStandard); !cmp.Equal(got, want) {
			t.Errorf("%s: billed %v, want %v", name, got, want)
		}
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
	}
}

// callNative signs the documented Chat request, streamed or not, checks it was made natively at path, and gives what
// the answer, with the x-gemini-service-tier header given, is billed for a request of the tier asked: the items and the
// tier. Nothing of the answer is read before the relay reads it.
func callNative(t *testing.T, stream bool, path, answer, header string, asked catalog.ServiceTier) (map[catalog.Item]int64, catalog.ServiceTier) {
	t.Helper()
	signer := google.New().Calls().(adapter.Signer)
	req, f := signer.Sign(t.Context(), nil, adapter.SignedCall{
		Format: protocol.Chat, Origin: origin, Model: "gemini-2.5-flash", Body: chatBody(stream), Key: secret.New("sk-test-key"),
	})
	if f != nil {
		t.Fatalf("sign: %+v", f)
	}
	checkNative(t, req, path)
	h := http.Header{"Content-Type": {"application/json"}}
	if header != "" {
		h.Set("X-Gemini-Service-Tier", header)
	}
	src := &unread{r: strings.NewReader(answer)}
	resp := signer.Answer(&http.Response{StatusCode: http.StatusOK, Request: req, Header: h, Body: src})
	if src.read {
		t.Error("the answer was read before the relay read it")
	}
	defer resp.Body.Close() //nolint:errcheck // a test body over a string
	return billed(t, resp.Body, asked)
}

// unread is a body that tells whether it was read.
type unread struct {
	r    io.Reader
	read bool
}

func (u *unread) Read(p []byte) (int, error) {
	u.read = true
	return u.r.Read(p)
}

func (*unread) Close() error { return nil }

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

// billed is what the relay bills for a Chat stream answering a request of the tier asked, item by item, and the tier.
func billed(t *testing.T, body io.ReadCloser, asked catalog.ServiceTier) (map[catalog.Item]int64, catalog.ServiceTier) {
	t.Helper()
	r := protocol.NewRelay(protocol.Chat, protocol.Chat, body, protocol.UsageContext{Tier: asked}, protocol.RelayOptions{Memory: protocol.NewMemory(64 << 20)})
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
	return out, u.Segments[0].Usage.ServiceTier()
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
