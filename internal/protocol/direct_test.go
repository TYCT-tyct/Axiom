package protocol_test

import (
	"bytes"
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
//
//nolint:cyclop,gocognit // the sample table and its assertions are the test
func TestDirectSamples(t *testing.T) {
	t.Parallel()
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
//
//nolint:cyclop,gocognit // the input table is the test
func TestDirectInputs(t *testing.T) {
	t.Parallel()
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
			want: func(r protocol.Request) bool { return r.TextBytes == 12 && r.MaxInputBytes == 12 },
		},
		{
			name: "embedding token arrays", f: protocol.Embeddings, body: `{"model":"m","input":[[1,2],[3]]}`,
			want: func(r protocol.Request) bool { return r.TextBytes == 12 && r.MaxInputBytes == 8 },
		},
		{
			name: "embedding multimodal", f: protocol.Embeddings,
			body: `{"model":"m","input":[{"content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`,
			want: func(r protocol.Request) bool {
				return r.TextBytes == 2 && r.MaxInputBytes == 2 && r.Images == 1 && r.Needs.Has(protocol.Images)
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
				return r.TextBytes == 3 && r.MaxInputBytes == 2 && r.Images == 1 && r.Needs.Has(protocol.Images)
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

// TestDirectPrepare: the rewrites drop the members that route and derive the identifiers. A direct format does not
// convert, and the request says so before any attempt.
//
//nolint:gocognit // the prepare table is the test
func TestDirectPrepare(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// TestDirectFormatNames keeps the formats' numbers, which suppliers store by name and routing keeps in a bit set, from
// moving silently.
func TestDirectFormatNames(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	for f := protocol.Messages; f <= protocol.Rerank; f++ {
		b.WriteString(string(rune('0' + f)))
	}
	if b.String() != "01234" {
		t.Errorf("formats %s", b.String())
	}
	if !protocol.Embeddings.Direct() || !protocol.Rerank.Direct() || protocol.Chat.Direct() {
		t.Error("Direct is not embeddings and rerank")
	}
}

// TestDirectReplyBoundaries: a reply may have whitespace around it, but one JSON object and nothing else. The usage
// and data a call is charged by must come from the same complete value, not from a prefix of one.
func TestDirectReplyBoundaries(t *testing.T) {
	t.Parallel()
	body := []byte(" \n {\"object\":\"list\",\"data\":[],\"usage\":{\"prompt_tokens\":1,\"total_tokens\":1}}\n ")
	if err := protocol.ValidateDirectReply(protocol.Embeddings, body); err != nil {
		t.Fatalf("leading whitespace: %v", err)
	}
	u, err := protocol.DirectUsage(protocol.Embeddings, body)
	if err != nil || len(u.Segments) != 1 {
		t.Fatalf("usage with leading whitespace: %+v, %v", u, err)
	}
	out, err := protocol.RewriteDirectReply(protocol.Embeddings, body, "id", "model", u, &protocol.Final{Usage: u})
	if err != nil {
		t.Fatal(err)
	}
	var rewritten map[string]any
	if err := jsonx.Unmarshal(out, &rewritten); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(listOf(rewritten["data"])) != 0 || rewritten["object"] != "list" {
		t.Errorf("rewritten with leading whitespace:\n%s", out)
	}
	for _, bad := range [][]byte{
		[]byte(`{"object":"list","data":[]} {"data":[]}`),
		[]byte(`{"object":"list","data":[]} x`),
		[]byte(`[]`),
		[]byte(``),
	} {
		if err := protocol.ValidateDirectReply(protocol.Embeddings, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
		if _, err := protocol.DirectUsage(protocol.Embeddings, bad); err == nil {
			t.Errorf("usage accepted %q", bad)
		}
	}
}

// TestDirectErrorBodies: an error that carries usage is a complete JSON object for both direct formats, settled or
// pending, so a client's parser can read the charge it was billed.
//
//nolint:gocognit // the two formats and two settlement states are the test
func TestDirectErrorBodies(t *testing.T) {
	t.Parallel()
	for _, f := range []protocol.Format{protocol.Embeddings, protocol.Rerank} {
		u, err := protocol.DirectUsage(f, []byte(`{"data":[],"results":[],"usage":{"prompt_tokens":3,"total_tokens":3}}`))
		if err != nil {
			t.Fatal(err)
		}
		for _, fin := range []*protocol.Final{{Usage: u}, {Usage: u, Pending: true}} {
			_, body := protocol.RenderError(f, protocol.Error{Type: protocol.InvalidRequest}, fin)
			var got map[string]any
			if err := jsonx.Unmarshal(body, &got); err != nil {
				t.Fatalf("%v: %v\n%s", f, err, body)
			}
			usage, _ := got["axiom"].(map[string]any)["usage"].(map[string]any)
			if usage == nil {
				t.Fatalf("%v: no axiom.usage:\n%s", f, body)
			}
			want := 3.0
			if f == protocol.Embeddings && usage["prompt_tokens"] != want || f == protocol.Rerank && usage["total_tokens"] != want {
				t.Errorf("%v usage %v:\n%s", f, usage, body)
			}
		}
	}
}

// TestDirectTokenArrayScales: a token array is scanned one number at a time; a million descriptors would dwarf the
// request body the node already charged. The count is still the same, and the scan allocates per element only when it
// has to keep the value.
func TestDirectTokenArrayScales(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"model":"m","input":[`)
	const n = 100000
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('1')
	}
	b.WriteString(`]}`)
	body := []byte(b.String())
	req, err := protocol.Inspect(protocol.Embeddings, body)
	if err != nil {
		t.Fatal(err)
	}
	if req.TextBytes != n*4 || req.MaxInputBytes != n*4 {
		t.Errorf("text %d, input %d; want %d", req.TextBytes, req.MaxInputBytes, n*4)
	}
	allocs := testing.AllocsPerRun(3, func() {
		if _, err := protocol.Inspect(protocol.Embeddings, body); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 2000 {
		t.Errorf("%.0f allocations for %d numbers; each number is copied", allocs, n)
	}
}
