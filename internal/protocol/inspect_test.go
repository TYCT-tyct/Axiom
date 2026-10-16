package protocol_test

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

const (
	pngData  = `"QUJD"`
	pdfData  = `"JVBERi0="`
	pngURL   = `"data:image/png;base64,QUJD"`
	pdfURL   = `"data:application/pdf;base64,JVBERi0="`
	standard = catalog.ServiceTierSet(1 << catalog.ServiceTierStandard)
)

func items(its ...catalog.Item) (s catalog.ItemSet) {
	for _, it := range its {
		s.Add(it)
	}
	return s
}

var inspectCases = []struct {
	name   string
	format protocol.Format
	body   string // or, when it names a file, the sample request in testdata
	want   func(r *protocol.Request)
}{
	{"messages sample with thinking", protocol.Messages, "messages/thinking/request.json", func(r *protocol.Request) {
		r.Model, r.Stream, r.MaxOutput, r.Needs = "claude-opus-5-5", true, 20000, protocol.Reasoning
	}},
	{"messages sample with tools", protocol.Messages, "messages/tool-use/request.json", func(r *protocol.Request) {
		r.Model, r.Stream, r.MaxOutput, r.Needs = "claude-opus-5", true, 1024, protocol.Tools
	}},
	{"responses sample", protocol.Responses, "responses/text/request.json", func(r *protocol.Request) {
		r.Model, r.Stream = "gpt-6-astra", true
	}},
	{"chat sample", protocol.Chat, "chat/text/request.json", func(r *protocol.Request) {
		r.Model, r.Stream = "gpt-6-astra", true
	}},
	{
		"messages media and cache markers", protocol.Messages, `{"model":"m","max_tokens":10,
		"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"messages":[{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"t","content":[{"source":{"data":` + pngData + `,"type":"base64"},"type":"image"}]},
			{"type":"document","source":{"type":"text","media_type":"text/plain","data":"plain"}},
			{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":` + pdfData + `}}]}]}`,
		func(r *protocol.Request) {
			r.Model, r.MaxOutput, r.Images, r.Documents = "m", 10, 1, 2
			r.TextBytes -= int64(len(pngData) + len(pdfData))
			r.InputItems = items(catalog.ItemInput, catalog.ItemCacheWrite, catalog.ItemCacheWrite1h)
			r.Needs = protocol.Images | protocol.Documents | protocol.PromptCache
		},
	},
	{
		"messages document of parts", protocol.Messages, `{"model":"m","messages":[{"role":"user","content":[
			{"type":"document","source":{"content":[
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":` + pngData + `}},
				{"type":"text","text":"t","cache_control":{"type":"ephemeral","ttl":"1h"}}],"type":"content"}}]}]}`,
		func(r *protocol.Request) {
			r.Model, r.Images, r.Documents = "m", 1, 1
			r.TextBytes -= int64(len(pngData))
			r.InputItems = items(catalog.ItemInput, catalog.ItemCacheWrite, catalog.ItemCacheWrite1h)
			r.Needs = protocol.Images | protocol.Documents | protocol.PromptCache
		},
	},
	{
		"messages server tool results of one part", protocol.Messages, `{"model":"m","messages":[{"role":"assistant","content":[
			{"type":"code_execution_tool_result","tool_use_id":"a","content":{"type":"code_execution_result","stdout":"",
				"stderr":"","return_code":0,"content":[{"type":"code_execution_output","file_id":"f"}]}},
			{"type":"web_search_tool_result","tool_use_id":"b","content":{"type":"web_search_tool_result_error",
				"error_code":"too_many_requests"}},
			{"type":"web_fetch_tool_result","tool_use_id":"c","content":{"type":"web_fetch_result","url":"https://example.com/a.pdf",
				"content":{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":` + pdfData + `}}}}]}]}`,
		func(r *protocol.Request) {
			r.Model, r.Documents, r.Needs = "m", 1, protocol.Documents
			r.TextBytes -= int64(len(pdfData))
		},
	},
	{
		"messages client data is not read", protocol.Messages, `{"model":"m",
		"tools":[{"name":"t","input_schema":{"type":"object","properties":{"cache_control":{},"max_uses":{},"type":{}}}}],
		"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"t",
			"input":{"type":"image","cache_control":{"ttl":"1h"},"content":[{"type":"image"}]}}]}]}`,
		func(r *protocol.Request) { r.Model, r.Needs = "m", protocol.Tools },
	},
	{
		"messages server tools, hidden reasoning and fast mode", protocol.Messages, `{"model":"m","stream":true,
		"tools":[{"type":"web_search_20260209","name":"web_search","max_uses":3},{"type":"web_search_20250305","max_uses":2},
			{"type":"code_execution_20250825","name":"code_execution"},{"type":"bash_20250124","name":"bash"}],
		"thinking":{"type":"adaptive","display":"omitted"},"output_config":{"format":{"type":"json_schema","schema":{}}},
		"speed":"fast","service_tier":"standard_only"}`,
		func(r *protocol.Request) {
			r.Model, r.Stream, r.WebSearches, r.HidesReasoning = "m", true, 5, true
			r.ServiceTiers = 1 << catalog.ServiceTierFast
			r.Needs = protocol.WebSearch | protocol.CodeExecution | protocol.Tools | protocol.Reasoning |
				protocol.StructuredOutput
		},
	},
	{
		"messages search without a limit, escaped names, thinking off", protocol.Messages, `{"m\u006fdel":"m",
		"tools":[{"type":"web_search_20260209","max_uses":3},{"type":"web_search_20260209"}],
		"thinking":{"type":"disabled"},"cache\u005fcontrol":{"type":"ephemeral"},"service_tier":"\u0061uto"}`,
		func(r *protocol.Request) {
			r.Model, r.Needs = "m", protocol.WebSearch|protocol.PromptCache
			r.InputItems = items(catalog.ItemInput, catalog.ItemCacheWrite)
		},
	},
	{
		"messages search limits adding up to the largest count", protocol.Messages, `{"model":"m",
		"tools":[{"type":"web_search_20260209","max_uses":9223372036854775806},{"type":"web_search_20250305","max_uses":1}]}`,
		func(r *protocol.Request) { r.Model, r.WebSearches, r.Needs = "m", math.MaxInt64, protocol.WebSearch },
	},
	{
		"chat everything", protocol.Chat, `{"model":"m","n":3,"max_completion_tokens":200,"max_tokens":100,
		"messages":[{"role":"user","content":[
			{"type":"image_url","image_url":{"url":` + pngURL + `,"detail":"high"}},
			{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},
			{"type":"file","file":{"filename":"a.pdf","file_data":` + pdfURL + `}},
			{"type":"text","text":"t","cache_control":{"type":"ephemeral","ttl":"1h"}}]}],
		"tools":[{"type":"function","function":{"name":"f"}}],"web_search_options":{},
		"reasoning":{"effort":"high","exclude":true},"response_format":{"type":"json_object"},"service_tier":"flex"}`,
		func(r *protocol.Request) {
			r.Model, r.MaxOutput, r.Choices, r.Images, r.Documents, r.HidesReasoning = "m", 200, 3, 2, 1, true
			r.TextBytes -= int64(len(pngURL) + len(pdfURL))
			r.InputItems = items(catalog.ItemInput, catalog.ItemCacheWrite, catalog.ItemCacheWrite1h)
			r.ServiceTiers = 1 << catalog.ServiceTierFlex
			r.Needs = protocol.Images | protocol.Documents | protocol.PromptCache | protocol.Tools | protocol.WebSearch |
				protocol.Reasoning | protocol.StructuredOutput
		},
	},
	{
		"chat reasoning effort none, plain text, default tier", protocol.Chat,
		`{"model":"m","reasoning_effort":"none","response_format":{"type":"text"},"service_tier":"default"}`,
		func(r *protocol.Request) { r.Model, r.ServiceTiers = "m", standard },
	},
	{
		"chat reasoning disabled, null members", protocol.Chat, `{"model":"m","reasoning":{"enabled":false},
		"stream":null,"messages":null,"tools":[{"type":null}],"max_tokens":null,"service_tier":null}`,
		func(r *protocol.Request) { r.Model, r.Needs = "m", protocol.Tools },
	},
	{
		"chat reasoning at effort none", protocol.Chat, `{"model":"m","reasoning":{"effort":"none"}}`,
		func(r *protocol.Request) { r.Model = "m" },
	},
	{
		"responses everything", protocol.Responses, `{"model":"m","max_output_tokens":50,
		"previous_response_id":"resp_1","service_tier":"priority",
		"input":[{"role":"user","content":[{"type":"input_image","image_url":` + pngURL + `},
				{"type":"input_file","file_data":` + pdfData + `}]},
			{"type":"function_call_output","call_id":"c","output":[{"type":"input_image","image_url":"https://example.com/a.png"}]}],
		"tools":[{"type":"web_search"},{"type":"code_interpreter","container":{"type":"auto"}},{"type":"function","name":"f"}],
		"max_tool_calls":4,"reasoning":{"effort":"high","summary":"auto"},
		"text":{"format":{"type":"json_schema","name":"x","schema":{}}}}`,
		func(r *protocol.Request) {
			r.Model, r.MaxOutput, r.Previous, r.Images, r.Documents = "m", 50, "resp_1", 2, 1
			r.TextBytes -= int64(len(pngURL) + len(pdfData))
			r.WebSearches, r.CodeExecutions = 4, 4
			r.ServiceTiers = 1 << catalog.ServiceTierPriority
			r.Needs = protocol.Images | protocol.Documents | protocol.WebSearch | protocol.CodeExecution | protocol.Tools |
				protocol.Reasoning | protocol.StructuredOutput
		},
	},
	{
		"responses computer screenshot", protocol.Responses, `{"model":"m","input":[{"type":"computer_call_output",
		"call_id":"c","output":{"type":"computer_screenshot","image_url":` + pngURL + `}}]}`,
		func(r *protocol.Request) {
			r.Model, r.Images, r.Needs = "m", 1, protocol.Images
			r.TextBytes -= int64(len(pngURL))
		},
	},
	{
		"responses call limit only for enabled tools", protocol.Responses, `{"model":"m",
		"tools":[{"type":"web_search"},{"type":"function","name":"f"}],"max_tool_calls":4}`,
		func(r *protocol.Request) { r.Model, r.WebSearches, r.Needs = "m", 4, protocol.WebSearch|protocol.Tools },
	},
}

func TestInspect(t *testing.T) {
	for _, c := range inspectCases {
		t.Run(c.name, func(t *testing.T) {
			body := caseBody(t, c.body)
			got, err := protocol.Inspect(c.format, body)
			if err != nil {
				t.Fatal(err)
			}
			want := protocol.Request{
				Format: c.format, Choices: 1, TextBytes: int64(len(body)), InputItems: items(catalog.ItemInput),
				ServiceTiers: standard | 1<<catalog.ServiceTierPriority,
			}
			if c.format != protocol.Messages {
				want.InputItems.Add(catalog.ItemCacheWrite)
			}
			c.want(&want)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Error(diff)
			}
		})
	}
}

// samples is testdata, laid out as README.md「录制样本」 describes.
var samples = os.DirFS("testdata")

func caseBody(tb testing.TB, body string) []byte {
	if !strings.HasSuffix(body, ".json") {
		return []byte(body)
	}
	b, err := fs.ReadFile(samples, body)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

func TestInspectRejects(t *testing.T) {
	deep := func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }
	for _, c := range []struct {
		name   string
		format protocol.Format
		body   string
	}{
		{"truncated", protocol.Chat, `{"model":"m",`},
		{"duplicate member", protocol.Chat, `{"model":"m","model":"n"}`},
		{"invalid UTF-8", protocol.Chat, "{\"model\":\"\xff\"}"},
		{"too deep in a member not read", protocol.Chat, `{"model":"m","x":` + deep(jsonx.MaxDepth) + `}`},
		{"data after the body", protocol.Chat, `{"model":"m"} {}`},
		{"not an object", protocol.Chat, `[]`},
		{"null", protocol.Chat, `null`},
		{"no model", protocol.Chat, `{"stream":true}`},
		{"model not a string", protocol.Chat, `{"model":1}`},
		{"stream not a boolean", protocol.Chat, `{"model":"m","stream":"yes"}`},
		{"negative cap", protocol.Chat, `{"model":"m","max_tokens":-1}`},
		{"fractional cap", protocol.Responses, `{"model":"m","max_output_tokens":1.5}`},
		{"search limits adding up past int64", protocol.Messages, `{"model":"m",
			"tools":[{"type":"web_search_20260209","max_uses":9223372036854775807},{"type":"web_search_20250305","max_uses":1}]}`},
		{"unknown service tier", protocol.Chat, `{"model":"m","service_tier":"scale"}`},
		{"unknown speed", protocol.Messages, `{"model":"m","speed":"turbo"}`},
		{"messages not an array", protocol.Messages, `{"model":"m","messages":{}}`},
		{"part not an object", protocol.Messages, `{"model":"m","messages":[{"content":["x"]}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := protocol.Inspect(c.format, []byte(c.body)); !errors.Is(err, protocol.ErrInvalid) {
				t.Errorf("got %v, want ErrInvalid", err)
			}
		})
	}
	// The deepest body jsonx accepts is accepted: the top object is one level.
	if _, err := protocol.Inspect(protocol.Chat, []byte(`{"model":"m","x":`+deep(jsonx.MaxDepth-1)+`}`)); err != nil {
		t.Errorf("nesting at jsonx.MaxDepth: %v", err)
	}
}

// FuzzInspect holds Inspect to jsonx's policy and to a full decode of the members both read.
func FuzzInspect(f *testing.F) {
	for _, c := range inspectCases {
		f.Add(uint8(c.format), caseBody(f, c.body))
	}
	f.Fuzz(func(t *testing.T, format uint8, body []byte) {
		r, err := protocol.Inspect(protocol.Format(format%3), body)
		if jsonx.Validate(body) != nil && err == nil {
			t.Fatal("accepted a body jsonx refuses")
		}
		if err != nil {
			return
		}
		var full struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := jsonx.Unmarshal(body, &full); err != nil || full.Model != r.Model || full.Stream != r.Stream {
			t.Fatalf("decoded %+v (%v), inspected %q %v", full, err, r.Model, r.Stream)
		}
		if r.TextBytes < 0 || r.TextBytes > int64(len(body)) {
			t.Fatalf("TextBytes %d of a %d-byte body", r.TextBytes, len(body))
		}
	})
}

// turns are one exchange in each format, with text, an image and a tool call, repeated to make bodies of any size.
var turns = [...]struct{ head, turn string }{
	protocol.Messages: {
		`{"model":"m","max_tokens":1024,"stream":true,"tools":[{"name":"f","input_schema":{}}],"messages":[`,
		`{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("hello ", 40) + `"},` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + strings.Repeat("QUJD", 100) + `"}}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"f","input":{"q":"x"}}]},`,
	},
	protocol.Responses: {
		`{"model":"m","stream":true,"tools":[{"type":"function","name":"f"}],"input":[`,
		`{"role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("hello ", 40) + `"},` +
			`{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("QUJD", 100) + `"}]},` +
			`{"type":"function_call","call_id":"c","name":"f","arguments":"{\"q\":\"x\"}"},`,
	},
	protocol.Chat: {
		`{"model":"m","stream":true,"tools":[{"type":"function","function":{"name":"f"}}],"messages":[`,
		`{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("hello ", 40) + `"},` +
			`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + strings.Repeat("QUJD", 100) + `"}}]},` +
			`{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},`,
	},
}

// wideTurn is a message with a member Inspect skips holding an object of more than 64 names, past which jsontext's
// duplicate-name check allocates for each name.
var wideTurn = func() string {
	names := make([]string, 100)
	for i := range names {
		names[i] = fmt.Sprintf(`"p%d":{}`, i)
	}
	return `{"role":"user","x":{` + strings.Join(names, ",") + `},"content":[]},`
}()

// body returns a request in format f of about size bytes, each turn led by lead.
func body(f protocol.Format, size int, lead string) []byte {
	t := turns[f]
	t.turn = lead + t.turn
	n := max(1, size/len(t.turn))
	return []byte(t.head + strings.TrimSuffix(strings.Repeat(t.turn, n), ",") + "]}")
}

var sizes = []int{1 << 10, 1 << 20}

// TestAllocs holds that reading or rewriting a request allocates a constant more than jsonx.Validate does on it,
// whatever its size (README.md「不变量」): Inspect's and Prepare's own allocations do not grow with the body. The
// tokenizer's grow with objects wider than 64 names, so the bodies come both without and with them.
func TestAllocs(t *testing.T) {
	for _, c := range []struct {
		name    string
		formats protocol.Format // the formats below it
		read    func(protocol.Format, []byte) error
	}{
		{"Inspect", protocol.Format(len(turns)), func(f protocol.Format, b []byte) error {
			_, err := protocol.Inspect(f, b)
			return err
		}},
		{"Prepare", protocol.Chat, prepareInspected()},
	} {
		for f := range c.formats {
			for _, lead := range []string{"", wideTurn} {
				if extra := extraAllocs(t, f, lead, c.read); extra[0] != extra[len(extra)-1] {
					t.Errorf("%s, format %d, wide %t: allocations beyond jsonx.Validate %v grow with the body sizes %v",
						c.name, f, lead != "", extra, sizes)
				}
			}
		}
	}
}

// prepareInspected prepares a body with the summary Inspect read from it the first time, which AllocsPerRun's warm-up
// run is, so that only Prepare's allocations are counted.
func prepareInspected() func(protocol.Format, []byte) error {
	seen := map[*byte]protocol.Request{}
	return func(f protocol.Format, b []byte) error {
		r, ok := seen[&b[0]]
		if !ok {
			var err error
			if r, err = protocol.Inspect(f, b); err != nil {
				return err
			}
			seen[&b[0]] = r
		}
		_, err := protocol.Prepare(&r, b, protocol.Target{Format: f, Model: "up", EncryptedReasoning: true}, protocol.Rewrites{Identity: derive})
		return err
	}
}

// extraAllocs returns how many more allocations read makes than jsonx.Validate on a body of each of sizes.
func extraAllocs(t *testing.T, f protocol.Format, lead string, read func(protocol.Format, []byte) error) []float64 {
	t.Helper()
	allocs := func(read func() error) float64 {
		return steadyAllocs(func() {
			if err := read(); err != nil {
				t.Fatal(err)
			}
		})
	}
	var extra []float64
	for _, size := range sizes {
		b := body(f, size, lead)
		extra = append(extra, allocs(func() error { return read(f, b) })-allocs(func() error { return jsonx.Validate(b) }))
	}
	return extra
}

func BenchmarkInspect(b *testing.B) {
	for f := range protocol.Format(len(turns)) {
		for _, size := range sizes {
			body := body(f, size, "")
			b.Run(fmt.Sprintf("format=%d/size=%d", f, size), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					if _, err := protocol.Inspect(f, body); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
