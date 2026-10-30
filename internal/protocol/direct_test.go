package protocol_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// TestDirectSamples replays the recorded embeddings and rerank replies: Inspect and Prepare read the request, the usage
// is the one the reply reports, and the rewritten reply is the upstream's data or results with Axiom's identity, usage
// and cost. TestDirectSamples is the non-streaming half of README.md「录制样本」.
func TestDirectSamples(t *testing.T) {
	for _, tc := range []struct {
		dir       string
		format    protocol.Format
		input     int64
		searches  int64
		wantField string
	}{
		{"embeddings/text", protocol.Embeddings, 8, 0, "data"},
		{"rerank/text", protocol.Rerank, 150, 1, "results"},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			request := sample(t, tc.dir+"/request.json")
			response := sample(t, tc.dir+"/response.json")
			meta := sample(t, tc.dir+"/meta.json")
			var m struct {
				Source string `json:"source"`
				Date   string `json:"date"`
			}
			if err := jsonx.Unmarshal(meta, &m); err != nil || m.Source == "" || m.Date == "" {
				t.Fatalf("meta.json: %v %+v", err, m)
			}
			req, err := protocol.Inspect(tc.format, request)
			if err != nil {
				t.Fatal(err)
			}
			if req.Format != tc.format || req.Model == "" || req.TextBytes == 0 {
				t.Errorf("request %+v", req)
			}
			up, err := protocol.Prepare(&req, request, protocol.Target{Format: tc.format, Model: "upstream/model"},
				protocol.Rewrites{Identity: func(string) string { return "derived" }, Device: "device"})
			if err != nil {
				t.Fatal(err)
			}
			var prepared map[string]any
			if err := jsonx.Unmarshal(up.Body, &prepared); err != nil {
				t.Fatalf("%v:\n%s", err, up.Body)
			}
			if prepared["model"] != "upstream/model" {
				t.Errorf("model was not rewritten:\n%s", up.Body)
			}
			u, err := protocol.DirectUsage(tc.format, response)
			if err != nil {
				t.Fatal(err)
			}
			if u.Invalid != nil || len(u.Segments) != 1 {
				t.Fatalf("usage %+v", u)
			}
			got := u.Segments[0].Usage
			if got.Count(catalog.ItemInput) != tc.input || got.Count(catalog.ItemWebSearch) != tc.searches {
				t.Errorf("usage input %d, searches %d; want %d, %d", got.Count(catalog.ItemInput),
					got.Count(catalog.ItemWebSearch), tc.input, tc.searches)
			}
			if err := protocol.ValidateDirectReply(tc.format, response); err != nil {
				t.Fatal(err)
			}
			body, err := protocol.RewriteDirectReply(tc.format, response, "gen-01", "client/model", u,
				&protocol.Final{Usage: u, Cost: price.Mul(1234), Currency: usd})
			if err != nil {
				t.Fatal(err)
			}
			if err := jsonx.Validate(body); err != nil {
				t.Fatalf("%v:\n%s", err, body)
			}
			var reply map[string]any
			if err := jsonx.Unmarshal(body, &reply); err != nil {
				t.Fatal(err)
			}
			if reply["model"] != "client/model" || reply[tc.wantField] == nil {
				t.Errorf("reply lost its data or kept the upstream's model:\n%s", body)
			}
			if usage := obj(reply["usage"]); usage["cost"] == nil {
				t.Errorf("no cost in the reply's usage:\n%s", body)
			}
			if tc.format == protocol.Rerank && (reply["id"] != "gen-01" || reply["usage"].(map[string]any)["search_units"] != 1.0) {
				t.Errorf("rerank identity or search_units:\n%s", body)
			}
		})
	}
}

// TestDirectInputs: the request members the direct formats read, including the shapes embeddings accept and the
// members that must be refused.
func TestDirectInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    protocol.Format
		body string
		// want is what Inspect reads, when the body is valid.
		want    func(protocol.Request) bool
		wantErr string
	}{
		{
			name: "embedding string", f: protocol.Embeddings, body: `{"model":"m","input":"hello"}`,
			want: func(r protocol.Request) bool { return r.TextBytes == 5 },
		},
		{
			name: "embedding token array", f: protocol.Embeddings, body: `{"model":"m","input":[1,2,3]}`,
			want: func(r protocol.Request) bool { return r.TextBytes == 12 },
		},
		{
			name: "embedding token arrays", f: protocol.Embeddings, body: `{"model":"m","input":[[1,2],[3]]}`,
			want: func(r protocol.Request) bool { return r.TextBytes == 12 },
		},
		{
			name: "embedding multimodal", f: protocol.Embeddings,
			body: `{"model":"m","input":[{"content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`,
			want: func(r protocol.Request) bool {
				return r.TextBytes == 2 && r.Images == 1 && r.Needs.Has(protocol.Images)
			},
		},
		{
			name: "embedding file", f: protocol.Embeddings,
			body: `{"model":"m","input":[{"content":[{"type":"file","file":{"file_data":"AAAA"}}]}]}`,
			want: func(r protocol.Request) bool { return r.Documents == 1 && r.Needs.Has(protocol.Documents) },
		},
		{
			name: "embedding stream", f: protocol.Embeddings, body: `{"model":"m","input":"x","stream":true}`,
			wantErr: "does not stream",
		},
		{
			name: "embedding dimensions", f: protocol.Embeddings, body: `{"model":"m","input":"x","dimensions":0}`,
			wantErr: "dimensions",
		},
		{
			name: "embedding encoding", f: protocol.Embeddings, body: `{"model":"m","input":"x","encoding_format":"hex"}`,
			wantErr: "encoding_format",
		},
		{
			name: "embedding input missing", f: protocol.Embeddings, body: `{"model":"m"}`,
			wantErr: "input is missing",
		},
		{
			name: "rerank documents", f: protocol.Rerank,
			body: `{"model":"m","query":"q","documents":["a",{"text":"b","image":"https://e/i.png"}]}`,
			want: func(r protocol.Request) bool {
				return r.TextBytes == 3 && r.Images == 1 && r.Needs.Has(protocol.Images)
			},
		},
		{
			name: "rerank empty documents", f: protocol.Rerank, body: `{"model":"m","query":"q","documents":[]}`,
			wantErr: "documents is empty",
		},
		{
			name: "rerank query missing", f: protocol.Rerank, body: `{"model":"m","documents":["a"]}`,
			wantErr: "query is missing",
		},
		{
			name: "rerank top_n", f: protocol.Rerank, body: `{"model":"m","query":"q","documents":["a"],"top_n":0}`,
			wantErr: "top_n",
		},
		{
			name: "completions string", f: protocol.Completions, body: `{"model":"m","prompt":"hello","max_tokens":4}`,
			want: func(r protocol.Request) bool { return r.TextBytes == 5 && r.MaxOutput == 4 && r.Choices == 1 },
		},
		{
			name: "completions prompts and n", f: protocol.Completions,
			body: `{"model":"m","prompt":["a","b"],"n":3,"stream":true}`,
			want: func(r protocol.Request) bool { return r.TextBytes == 2 && r.Choices == 6 && r.Stream },
		},
		{
			name: "completions prompt missing", f: protocol.Completions, body: `{"model":"m"}`,
			wantErr: "prompt is missing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := protocol.Inspect(tc.f, []byte(tc.body))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want an error saying %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.want(r) {
				t.Errorf("request %+v", r)
			}
		})
	}
}

// TestDirectPrepare: the rewrites drop the members that route, derive the identifiers, and legacy completions ask for a
// stream. A direct format does not convert, and the request says so before any attempt.
func TestDirectPrepare(t *testing.T) {
	identity := func(string) string { return "derived" }
	for _, tc := range []struct {
		name, body string
		f          protocol.Format
		want       string
		absent     []string
	}{
		{
			name: "embeddings", f: protocol.Embeddings,
			body:   `{"model":"m","input":"x","user":"alice","provider":{"only":["a"]},"session_id":"s"}`,
			want:   `{"model":"upstream","input":"x","user":"derived"}`,
			absent: []string{"provider", "session_id"},
		},
		{
			name: "completions", f: protocol.Completions,
			body: `{"model":"m","prompt":"x","user":"alice","max_tokens":5}`,
			want: `{"model":"upstream","prompt":"x","user":"derived","max_tokens":5,"stream":true}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := protocol.Inspect(tc.f, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			up, err := protocol.Prepare(&req, []byte(tc.body), protocol.Target{Format: tc.f, Model: "upstream", Streams: true},
				protocol.Rewrites{Identity: identity, Device: "d"})
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if err := jsonx.Unmarshal(up.Body, &got); err != nil {
				t.Fatal(err)
			}
			if err := jsonx.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("body (-want +got):\n%s\n%s", diff, up.Body)
			}
			for _, name := range tc.absent {
				if _, ok := got[name]; ok {
					t.Errorf("%s was not dropped:\n%s", name, up.Body)
				}
			}
		})
	}

	req, err := protocol.Inspect(protocol.Embeddings, []byte(`{"model":"m","input":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.Prepare(&req, []byte(`{"model":"m","input":"x"}`), protocol.Target{Format: protocol.Chat}, protocol.Rewrites{}); err == nil {
		t.Fatal("embeddings converted to Chat")
	}
}

// TestDirectErrors: a reply without its data or results is the upstream breaking its format, before anything is
// committed; a usage that cannot be read is Invalid, and billing estimates it as it does for a stream.
func TestDirectErrors(t *testing.T) {
	if err := protocol.ValidateDirectReply(protocol.Embeddings, []byte(`{"object":"list","usage":{}}`)); err == nil {
		t.Fatal("an embeddings reply without data was accepted")
	}
	if err := protocol.ValidateDirectReply(protocol.Rerank, []byte(`{"id":"x","results":{}}`)); err == nil {
		t.Fatal("a rerank reply with object results was accepted")
	}
	for _, body := range []string{
		`{"data":[],"usage":{"prompt_tokens":-1}}`,
		`{"data":[],"usage":{"prompt_tokens":"x"}}`,
		`{"data":[],"usage":[]}`,
	} {
		u, err := protocol.DirectUsage(protocol.Embeddings, []byte(body))
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if u.Invalid == nil {
			t.Errorf("%s: usage was accepted", body)
		}
	}
	if u, err := protocol.DirectUsage(protocol.Embeddings, []byte(`{"data":[]}`)); err != nil || len(u.Segments) != 0 {
		t.Errorf("a reply without usage: %+v, %v", u, err)
	}
}

// TestCompletionsSample replays the recorded legacy completions stream: the aggregate is one text completion, its text
// is the chunks' text in order, and the usage and cost are the settlement's.
func TestCompletionsSample(t *testing.T) {
	stream := sample(t, "completions/text/upstream.sse")
	request := sample(t, "completions/text/request.json")
	req, err := protocol.Inspect(protocol.Completions, request)
	if err != nil {
		t.Fatal(err)
	}
	if !req.Stream || req.MaxOutput != 7 {
		t.Errorf("request %+v", req)
	}
	got := aggregate(t, protocol.Completions, protocol.Completions, bytes.NewReader(stream), protocol.NewMemory(0), func(protocol.Usage) *protocol.Final { return nil })
	if got.err != nil || got.status != http.StatusOK {
		t.Fatalf("error %v, status %d:\n%s", got.err, got.status, got.body)
	}
	body := decode(t, got.body)
	if body["object"] != "text_completion" {
		t.Errorf("object %v", body["object"])
	}
	choices := objects(body["choices"])
	if len(choices) != 1 || choices[0]["text"] != "Say this is" || choices[0]["finish_reason"] != "length" {
		t.Errorf("choices %v", body["choices"])
	}
	if usage := obj(body["usage"]); usage["cost"] == nil {
		t.Errorf("no cost in the aggregate:\n%s", got.body)
	}

	// The streaming client gets the chunks in order, our identity, and a usage chunk of ours before [DONE].
	relayed := relay(t, protocol.Completions, bytes.NewReader(stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
	if relayed.err != nil {
		t.Fatal(relayed.err)
	}
	out := string(relayed.out)
	if !strings.Contains(out, `"id":"gen-01"`) || strings.Contains(out, "cmpl-123") {
		t.Errorf("the client's id is not ours:\n%s", out)
	}
	if !strings.Contains(out, `"usage":{`) || !strings.Contains(out, "data: [DONE]") {
		t.Errorf("the stream has no usage before its end:\n%s", out)
	}
	var last map[string]any
	for _, line := range strings.Split(out, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := jsonx.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		last = chunk
	}
	if obj(last["usage"])["cost"] == nil {
		t.Errorf("the last chunk has no cost: %v", last)
	}
}

// TestCompletionsStreamEvents: the reader ends only after every started choice finished, and an error chunk fails the
// stream instead of reaching the client as content.
func TestCompletionsStreamEvents(t *testing.T) {
	for _, tc := range []struct {
		name, stream, want string
	}{
		{
			name:   "done before finish",
			stream: "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"text\":\"a\"}]}\n\ndata: [DONE]\n\n",
			want:   "[DONE] before choice 0 finished",
		},
		{
			name: "error chunk",
			stream: "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"text\":\"a\"}]}\n\n" +
				"data: {\"error\":{\"message\":\"bad\",\"type\":\"invalid_request_error\"}}\n\n",
			want: "upstream",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := relay(t, protocol.Completions, strings.NewReader(tc.stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if got.err == nil || !strings.Contains(got.err.Error(), tc.want) {
				t.Fatalf("got %v, want an error saying %q", got.err, tc.want)
			}
		})
	}
}

// TestDirectFormatNames keeps the formats' numbers, which suppliers store by name and routing keeps in a bit set, from
// moving silently.
func TestDirectFormatNames(t *testing.T) {
	var b bytes.Buffer
	for f := protocol.Messages; f <= protocol.Rerank; f++ {
		b.WriteString(string(rune('0' + f)))
	}
	if b.String() != "012345" {
		t.Errorf("formats %s", b.String())
	}
	if !protocol.Embeddings.Direct() || !protocol.Rerank.Direct() || protocol.Completions.Direct() {
		t.Error("Direct is not embeddings and rerank")
	}
}
