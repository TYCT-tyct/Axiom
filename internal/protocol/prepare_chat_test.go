package protocol_test

import (
	"encoding/json/jsontext" //nolint:depguard // the oracle keeps members raw, as in prepare_test.go
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// chatTarget is a Chat endpoint that streams, with the output cap the request was quoted at.
func chatTarget(max int64) protocol.Target {
	return protocol.Target{Format: protocol.Chat, Model: "up", Streams: true, MaxOutput: max}
}

// TestPrepareChat holds the members README.md「同格式改写」 rewrites for a Chat endpoint to the whole body, so that a byte
// changed outside them fails: the model, the end user and cache key derived, the routing parameters gone, a stream
// asked for with usage in it whatever the client wrote, and the output cap the request was quoted at when it set none.
// What the model's dialect owns — reasoning, cache markers, a reasoning_content sent back, image parts — stays as written.
func TestPrepareChat(t *testing.T) {
	const tail = `,"stream_options":{"include_usage":true},"stream":true}`
	for _, c := range []struct {
		name, body, want string
		max              int64
	}{
		{
			"plain", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			`{"model":"up","messages":[{"role":"user","content":"hi"}],"max_tokens":4096` + tail, 4096,
		},
		{
			"routing parameters, identities, and what the dialect owns",
			`{"provider":{"order":["a"]},"model":"m","user":"ué","prompt_cache_key":"k","safety_identifier":"s",` +
				`"models":["x"],"route":"fallback","transforms":[],"plugins":[],"usage":{"include":true},"session_id":"s","axiom":{}, ` +
				`"reasoning":{"effort":"high"},"reasoning_effort":"low","messages":[{"role":"assistant","reasoning_content":"café",` +
				`"content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}],"max_tokens":50}`,
			`{"model":"up","user":"h(ué)","prompt_cache_key":"h(k)","safety_identifier":"h(s)", ` +
				`"reasoning":{"effort":"high"},"reasoning_effort":"low","messages":[{"role":"assistant","reasoning_content":"café",` +
				`"content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}],"max_tokens":50` + tail, 4096,
		},
		{"the largest cap", `{"model":"m"}`, `{"model":"up","max_tokens":9223372036854775807` + tail, math.MaxInt64},
		{"no cap to send", `{"model":"m"}`, `{"model":"up"` + tail, 0},
		{"a cap kept", `{"model":"m","max_tokens":50,"stream":true}`, `{"model":"up","stream":true,"max_tokens":50,"stream_options":{"include_usage":true}}`, 4096},
		{"the other cap is the one the endpoint reads", `{"model":"m","max_completion_tokens":50}`, `{"model":"up","max_tokens":50` + tail, 4096},
		{"both caps, the smaller", `{"model":"m","max_tokens":60,"max_completion_tokens":50}`, `{"model":"up","max_tokens":50` + tail, 4096},
		{"a cap of 0 is none", `{"model":"m","max_tokens":0,"max_completion_tokens":0}`, `{"model":"up","max_tokens":4096` + tail, 4096},
		{"a null cap is the cap sent", `{"model":"m","max_tokens":null,"stream":true}`, `{"model":"up","stream":true,"max_tokens":4096,"stream_options":{"include_usage":true}}`, 4096},
		{"a first null cap", `{"max_tokens":null,"model":"m"}`, `{"model":"up","max_tokens":4096` + tail, 4096},
		{"a null cap beside a cap", `{"model":"m","max_tokens":null,"max_completion_tokens":50}`, `{"model":"up","max_tokens":50` + tail, 4096},
		{"both null", `{"model":"m","max_tokens":null,"max_completion_tokens":null}`, `{"model":"up","max_tokens":4096` + tail, 4096},
		{"stream false", `{"model":"m","stream":false}`, `{"model":"up","stream":true,"stream_options":{"include_usage":true}}`, 0},
		{"stream null", `{"model":"m","stream":null}`, `{"model":"up","stream":true,"stream_options":{"include_usage":true}}`, 0},
		{"options null", `{"model":"m","stream_options":null}`, `{"model":"up","stream_options":{"include_usage":true},"stream":true}`, 0},
		{"options empty", `{"model":"m","stream_options":{}}`, `{"model":"up","stream_options":{"include_usage":true},"stream":true}`, 0},
		{"options empty with space", `{"model":"m","stream_options":{ }}`, `{"model":"up","stream_options":{"include_usage":true },"stream":true}`, 0},
		{
			"usage turned off", `{"model":"m","stream_options":{"include_usage":false},"stream":true}`,
			`{"model":"up","stream_options":{"include_usage":true},"stream":true}`, 0,
		},
		{
			"usage null", `{"model":"m","stream_options":{"include_usage":null}}`,
			`{"model":"up","stream_options":{"include_usage":true},"stream":true}`, 0,
		},
		{
			"usage on, another option", `{"model":"m","stream_options":{"include_obfuscation":false,"include_usage":true}}`,
			`{"model":"up","stream_options":{"include_obfuscation":false,"include_usage":true},"stream":true}`, 0,
		},
		{
			"another option only", `{"model":"m","stream_options":{"include_obfuscation":false}}`,
			`{"model":"up","stream_options":{"include_obfuscation":false,"include_usage":true},"stream":true}`, 0,
		},
		{
			"another option and usage off", `{"model":"m","stream_options":{"include_usage":false, "continuous_usage_stats":true}}`,
			`{"model":"up","stream_options":{"include_usage":true, "continuous_usage_stats":true},"stream":true}`, 0,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := inspected(t, protocol.Chat, []byte(c.body))
			target, w := chatTarget(c.max), protocol.Rewrites{Identity: derive}
			u, err := protocol.Prepare(&r, []byte(c.body), target, w)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.want, string(u.Body)); diff != "" {
				t.Error(diff)
			}
			if cost := protocol.PrepareCost(&r, []byte(c.body), target, w); int64(cap(u.Body))+3*int64(len(c.body)) != cost {
				t.Errorf("the buffer was %d bytes, the cost %d: the rewrite outgrew its room", cap(u.Body), cost)
			}
		})
	}
}

// TestPrepareChatToAnEndpointThatDoesNotStream: the request keeps the stream the client asked for and the options it
// sent, since nothing is read from a stream there; the cap and the end users are still the endpoint's.
func TestPrepareChatToAnEndpointThatDoesNotStream(t *testing.T) {
	body := `{"model":"m","stream":false,"stream_options":{"include_usage":false},"user":"u"}`
	r := inspected(t, protocol.Chat, []byte(body))
	target := chatTarget(7)
	target.Streams = false
	u, err := protocol.Prepare(&r, []byte(body), target, protocol.Rewrites{Identity: derive})
	want := `{"model":"up","stream":false,"stream_options":{"include_usage":false},"user":"h(u)","max_tokens":7}`
	if err != nil || string(u.Body) != want {
		t.Errorf("got %s (%v), want %s", u.Body, err, want)
	}
}

// TestPrepareChatRejects: a member Prepare rewrites that is not of the type it rewrites is the client's to fix.
func TestPrepareChatRejects(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"model":"m","user":1}`, "user is not a string"},
		{`{"model":"m","prompt_cache_key":{}}`, "prompt_cache_key is not a string"},
		{`{"model":"m","stream_options":"x"}`, "stream_options is not an object"},
		{`{"model":"m","stream_options":[]}`, "stream_options is not an object"},
	} {
		r := inspected(t, protocol.Chat, []byte(c.body))
		_, err := protocol.Prepare(&r, []byte(c.body), chatTarget(1), protocol.Rewrites{Identity: derive})
		if !errors.Is(err, protocol.ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want ErrInvalid saying %q", c.body, err, c.want)
		}
	}
}

// FuzzPrepareChatStream holds a Chat request sent to a streaming endpoint to what admission read of it, as FuzzPrepare
// does for the rewrites that need no stream: whatever Inspect accepts is refused only for a member of the wrong type,
// and what is sent asks for a stream, with include_usage true, without a duplicate member and with the summary admission
// read unchanged but for the stream and the cap added.
func FuzzPrepareChatStream(f *testing.F) {
	for _, body := range []string{
		`{"model":"m"}`, `{"model":"m","stream":false,"stream_options":null,"max_tokens":null}`,
		`{"model":"m","stream_options":{"include_usage":false,"x":[1]},"max_completion_tokens":5}`,
		`{"model":"m","stream_options":{},"user":"u","max_tokens":0}`, `{"model":"m","stream_options":"x"}`,
		`{"stream_options":{ "include_usage" : null },"model":"m","stream":true}`,
	} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		r, err := protocol.Inspect(protocol.Chat, body)
		if err != nil {
			return
		}
		u, err := protocol.Prepare(&r, body, chatTarget(7), protocol.Rewrites{Identity: derive})
		if want := chatStreamRejects(t, body); errors.Is(err, protocol.ErrInvalid) != want {
			t.Fatalf("%s: got %v, want ErrInvalid %t", body, err, want)
		}
		if err == nil {
			checkChatStreamSent(t, r, body, u.Body)
		}
	})
}

// chatStreamRejects reports whether a member Prepare rewrites is neither null nor of its type in body, which Inspect
// accepted.
func chatStreamRejects(t *testing.T, body []byte) bool {
	t.Helper()
	var top map[string]jsontext.Value
	if err := jsonx.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	wrongType := func(name string, kind jsontext.Kind) bool {
		v := top[name]
		return v != nil && v.Kind() != 'n' && v.Kind() != kind
	}
	return wrongType("stream_options", '{') || wrongType("user", '"') || wrongType("prompt_cache_key", '"') ||
		wrongType("safety_identifier", '"')
}

// checkChatStreamSent: sent, which Prepare wrote from body, is read by Inspect (a duplicate member is not), asks for a
// stream with usage, and is the summary r but for what Prepare changes.
func checkChatStreamSent(t *testing.T, r protocol.Request, body, sent []byte) {
	t.Helper()
	got, err := protocol.Inspect(protocol.Chat, sent)
	if err != nil {
		t.Fatalf("%s: %v", sent, err)
	}
	var options struct {
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := jsonx.Unmarshal(sent, &options); err != nil {
		t.Fatalf("%s: %v", sent, err)
	}
	if !got.Stream || !options.StreamOptions.IncludeUsage {
		t.Fatalf("%s: stream %t, include_usage %t", sent, got.Stream, options.StreamOptions.IncludeUsage)
	}
	r.Model, r.Models, r.Provider, r.Previous, r.TextBytes = "up", nil, nil, "", r.TextBytes+int64(len(sent)-len(body))
	if r.MaxOutput > 0 && (got.MaxOutput == 0 || got.MaxOutput > r.MaxOutput) || r.MaxOutput == 0 && got.MaxOutput != 7 {
		t.Fatalf("%s: the cap sent is %d, the request read as %d and the call was quoted at 7", sent, got.MaxOutput, r.MaxOutput)
	}
	r.Stream, r.MaxOutput = true, got.MaxOutput
	if diff := cmp.Diff(r, got); diff != "" {
		t.Fatalf("%s:\n%s", sent, diff)
	}
}

// TestPrepareChatWritesTheCapTheEndpointReads: the output cap is written in the one member the endpoint reads, whichever
// the client wrote, so that an endpoint cannot ignore the cap the call was reserved at: a DeepSeek- or Qwen-like endpoint
// reads max_tokens and ignores max_completion_tokens, OpenAI's reasoning, GPT-5 and GPT-6 models read
// max_completion_tokens and refuse max_tokens (new-api's rule, GetOpenAIChatCapabilities).
func TestPrepareChatWritesTheCapTheEndpointReads(t *testing.T) {
	for _, c := range []struct {
		model, body, want string
	}{
		{"deepseek-chat", `{"model":"m","max_completion_tokens":1}`, `"max_tokens":1`},
		{"deepseek-chat", `{"model":"m","max_tokens":2}`, `"max_tokens":2`},
		{"deepseek-chat", `{"model":"m"}`, `"max_tokens":9`},
		{"gpt-4o", `{"model":"m","max_tokens":3}`, `"max_tokens":3`},
		{"gpt-5x", `{"model":"m"}`, `"max_tokens":9`},
		{"gpt-6-astra-x", `{"model":"m"}`, `"max_tokens":9`},
		{"o3-mini", `{"model":"m","max_tokens":4}`, `"max_completion_tokens":4`},
		{"o1", `{"model":"m"}`, `"max_completion_tokens":9`},
		{"o4-mini", `{"model":"m","max_completion_tokens":5}`, `"max_completion_tokens":5`},
		{"gpt-5", `{"model":"m","max_tokens":6}`, `"max_completion_tokens":6`},
		{"gpt-5-mini", `{"model":"m","max_tokens":6}`, `"max_completion_tokens":6`},
		{"gpt-5.2", `{"model":"m","max_tokens":6}`, `"max_completion_tokens":6`},
		{"gpt-6-astra", `{"model":"m","max_tokens":7}`, `"max_completion_tokens":7`},
		{"gpt-6-sol-2026-05-01", `{"model":"m","max_tokens":7}`, `"max_completion_tokens":7`},
		{"gpt-6-luna", `{"model":"m"}`, `"max_completion_tokens":9`},
	} {
		t.Run(c.model+" "+c.body, func(t *testing.T) {
			r := inspected(t, protocol.Chat, []byte(c.body))
			target := chatTarget(9)
			target.Model = c.model
			u, err := protocol.Prepare(&r, []byte(c.body), target, protocol.Rewrites{Identity: derive})
			if err != nil || !strings.Contains(string(u.Body), c.want) || strings.Count(string(u.Body), "max_") != 1 {
				t.Errorf("got %s (%v), want exactly %s", u.Body, err, c.want)
			}
		})
	}
}
