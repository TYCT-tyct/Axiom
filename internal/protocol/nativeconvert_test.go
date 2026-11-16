package protocol_test

import (
	"errors"
	"io/fs"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// nativeCase is one row of the Messages ↔ Responses conversion matrix: the body, given as its own format writes it,
// and what the other format's endpoint gets, or why it cannot have it. The target is the sample endpoint of the
// other format, with the whole of its dialect claimed (claudeLike).
type nativeCase struct {
	name, body string
	target     func(*protocol.Target)
	want       string // the upstream body; or "unsupported" or "invalid"
}

func nativeConvertCases(t *testing.T, from, to protocol.Format, cases []nativeCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := protocol.Inspect(from, []byte(c.body))
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			target := claudeLike(to)
			if to == protocol.Messages {
				target.MaxOutput = 0 // the request's own cap is the one written here
			}
			if c.target != nil {
				c.target(&target)
			}
			got, err := protocol.Prepare(&r, []byte(c.body), target, rewrites())
			ns, _ := errors.AsType[*protocol.NotSupported](err)
			switch {
			case c.want == "unsupported" && ns == nil, c.want == "invalid" && !errors.Is(err, protocol.ErrInvalid):
				t.Fatalf("got %s, %v; want %s", got.Body, err, c.want)
			case c.want == "unsupported", c.want == "invalid":
			case err != nil:
				t.Fatal(err)
			default:
				sameJSON(t, c.want, got.Body)
			}
		})
	}
}

// TestConvertMessagesToResponses: a Messages request for a Responses endpoint (README.md「跨格式转换」).
func TestConvertMessagesToResponses(t *testing.T) {
	t.Parallel()
	nativeConvertCases(t, protocol.Messages, protocol.Responses, []nativeCase{
		{
			"system and text", `{"model":"m","max_tokens":64,"system":[{"type":"text","text":"Be brief."}],"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`, nil,
			`{"model":"up","input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"Be brief."}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],"store":false,"max_output_tokens":64}`,
		},
		{
			"images and a document", `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},` +
				`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"BBBB"},"title":"a.pdf"}]}]}`, nil,
			`{"model":"up","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"},` +
				`{"type":"input_file","file_data":"data:application/pdf;base64,BBBB","filename":"a.pdf"}]}],"store":false,"max_output_tokens":64}`,
		},
		{
			"tools, a call and its result", `{"model":"m","max_tokens":64,"tools":[{"name":"f","description":"d","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"},` +
				`"messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"f","input":{"a":1}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"}]}]}`, nil,
			`{"model":"up","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]},` +
				`{"type":"function_call","call_id":"toolu_1","name":"f","arguments":"{\"a\":1}"},` +
				`{"type":"function_call_output","call_id":"toolu_1","output":"done"}],"store":false,` +
				`"tools":[{"type":"function","name":"f","description":"d","parameters":{"type":"object"},"strict":false}],"tool_choice":"required","max_output_tokens":64}`,
		},
		{
			"thinking becomes an effort", `{"model":"m","max_tokens":20000,"thinking":{"type":"enabled","budget_tokens":10000},` +
				`"messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"S"},{"type":"text","text":"a"}]},{"role":"user","content":"again"}]}`, nil,
			`{"model":"up","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]},` +
				`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}],"store":false,"max_output_tokens":20000,` +
				`"reasoning":{"effort":"medium","summary":"auto"}}`,
		},
		{
			"the cache markers become breakpoints", `{"model":"m","max_tokens":64,"system":[{"type":"text","text":"Be brief.","cache_control":{"type":"ephemeral"}}],` +
				`"messages":[{"role":"user","content":"Hi"}]}`, breakpoints,
			`{"model":"up","input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"Be brief.","prompt_cache_breakpoint":{"mode":"explicit"}}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],"store":false,"max_output_tokens":64}`,
		},
		{
			"the metadata identifies the user", `{"model":"m","max_tokens":64,"metadata":{"user_id":"u"},"messages":[{"role":"user","content":"Hi"}]}`, nil,
			`{"model":"up","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]}],"store":false,"max_output_tokens":64,"user":"h(u)"}`,
		},
		{
			"a file id has no Responses counterpart in a Messages request", `{"model":"m","max_tokens":64,` +
				`"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file-1"}}]}]}`, nil, "unsupported",
		},
		{
			"stop sequences have no Responses counterpart", `{"model":"m","max_tokens":64,"stop_sequences":["x"],"messages":[{"role":"user","content":"Hi"}]}`, nil, "unsupported",
		},
	})
}

// TestConvertResponsesToMessages: a Responses request for a Messages endpoint (README.md「跨格式转换」).
func TestConvertResponsesToMessages(t *testing.T) {
	t.Parallel()
	nativeConvertCases(t, protocol.Responses, protocol.Messages, []nativeCase{
		{
			"instructions and input", `{"model":"m","instructions":"Be brief.","input":"Hi","max_output_tokens":64}`, nil,
			`{"model":"up","max_tokens":64,"system":[{"type":"text","text":"Be brief."}],"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`,
		},
		{
			"items, a call and its result", `{"model":"m","max_output_tokens":64,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"},` +
				`{"type":"input_image","image_url":"data:image/png;base64,AAAA"},{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,BBBB"}]},` +
				`{"type":"function_call","call_id":"call_1","name":"f","arguments":"{\"a\":1}"},{"type":"function_call_output","call_id":"call_1","output":"done"}]}`, nil,
			`{"model":"up","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"},` +
				`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},` +
				`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"BBBB"},"title":"a.pdf"}]},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"f","input":{"a":1}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]}]}`,
		},
		{
			"an effort becomes a budget", `{"model":"m","max_output_tokens":20000,"reasoning":{"effort":"high","summary":"auto"},` +
				`"input":[{"type":"message","role":"user","content":"Hi"},{"type":"reasoning","summary":[{"type":"summary_text","text":"hm"}],"encrypted_content":"E"},` +
				`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"}]},{"type":"message","role":"user","content":"again"}]}`, nil,
			`{"model":"up","max_tokens":20000,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]},` +
				`{"role":"assistant","content":[{"type":"text","text":"a"}]},{"role":"user","content":[{"type":"text","text":"again"}]}],` +
				`"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`,
		},
		{
			"the JSON schema reply", `{"model":"m","max_output_tokens":64,"text":{"format":{"type":"json_schema","name":"s",` +
				`"schema":{"type":"object","additionalProperties":false}}},"input":"Hi"}`, nil,
			`{"model":"up","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}],` +
				`"output_config":{"format":{"type":"json_schema","schema":{"type":"object","additionalProperties":false}}}}`,
		},
		{
			"a file id cannot reach Messages", `{"model":"m","max_output_tokens":64,"input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-1"}]}]}`, nil, "unsupported",
		},
		{
			"a file's default detail", `{"model":"m","max_output_tokens":64,"input":[{"type":"message","role":"user","content":[{"type":"input_file","filename":"a.pdf",` +
				`"file_data":"data:application/pdf;base64,BBBB","detail":"auto"}]}]}`, nil,
			`{"model":"up","max_tokens":64,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"BBBB"},"title":"a.pdf"}]}]}`,
		},
		{
			"a file's high detail", `{"model":"m","max_output_tokens":64,"input":[{"type":"message","role":"user","content":[{"type":"input_file","filename":"a.pdf",` +
				`"file_data":"data:application/pdf;base64,BBBB","detail":"high"}]}]}`, nil, "unsupported",
		},
		{
			"a file's low detail", `{"model":"m","max_output_tokens":64,"input":[{"type":"message","role":"user","content":[{"type":"input_file","filename":"a.pdf",` +
				`"file_data":"data:application/pdf;base64,BBBB","detail":"low"}]}]}`, nil, "unsupported",
		},
		{
			"a file detail outside the three", `{"model":"m","max_output_tokens":64,"input":[{"type":"message","role":"user","content":[{"type":"input_file","filename":"a.pdf",` +
				`"file_data":"data:application/pdf;base64,BBBB","detail":"medium"}]}]}`, nil, "invalid",
		},
		{
			"an image detail outside the three", `{"model":"m","max_output_tokens":64,"input":[{"type":"message","role":"user","content":[{"type":"input_image",` +
				`"image_url":"https://example.test/i.png","detail":"medium"}]}]}`, nil, "invalid",
		},
		{
			"auto truncation has no Messages counterpart", `{"model":"m","max_output_tokens":64,"truncation":"auto","input":"Hi"}`, nil, "unsupported",
		},
		{
			"a replayed output with empty logprobs", `{"model":"m","max_output_tokens":64,"input":[{"role":"user","content":"Hi"},{"role":"assistant",` +
				`"content":[{"type":"output_text","text":"a","annotations":[],"logprobs":[]}]}]}`, nil,
			`{"model":"up","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]},` +
				`{"role":"assistant","content":[{"type":"text","text":"a"}]}]}`,
		},
		{
			"a replayed output with logprobs", `{"model":"m","max_output_tokens":64,"input":[{"role":"user","content":"Hi"},{"role":"assistant",` +
				`"content":[{"type":"output_text","text":"a","annotations":[],"logprobs":[{"token":"a","logprob":-0.1}]}]}]}`, nil, "unsupported",
		},
		{
			"a reasoning summary is not a thinking block", `{"model":"m","max_output_tokens":20000,"input":[{"type":"message","role":"user","content":"Hi"},` +
				`{"type":"reasoning","summary":[{"type":"summary_text","text":"hm"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"}]}]}`, nil,
			`{"model":"up","max_tokens":20000,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]},` +
				`{"role":"assistant","content":[{"type":"text","text":"a"}]}]}`,
		},
		{
			"instructions must be a string", `{"model":"m","max_output_tokens":64,"instructions":[{"type":"input_text","text":"a"}],"input":"Hi"}`, nil, "invalid",
		},
	})
}

// TestConvertNativeSamples converts each recorded Messages and Responses request for the other format's endpoint and
// compares the result with its golden copy, to-messages.json or to-responses.json, which go test -update rewrites.
func TestConvertNativeSamples(t *testing.T) {
	t.Parallel()
	for _, up := range formats[:2] {
		to, ext := protocol.Responses, "responses"
		if up.f == protocol.Responses {
			to, ext = protocol.Messages, "messages"
		}
		for _, dir := range sampleDirs(t, up.dir) {
			t.Run(dir+"/"+ext, func(t *testing.T) { checkNativeConvert(t, up.f, dir, to, ext) })
		}
	}
}

// sampleDirs is every sample directory of a format's testdata directory.
func sampleDirs(t *testing.T, dir string) []string {
	t.Helper()
	dirs, err := fs.Glob(samples, dir+"/*")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no %s samples: %v", dir, err)
	}
	return dirs
}

// checkNativeConvert prepares the sample's request for the other native format and compares it with its golden copy.
func checkNativeConvert(t *testing.T, from protocol.Format, dir string, to protocol.Format, ext string) {
	t.Helper()
	r := inspected(t, from, sample(t, dir+"/request.json"))
	target := claudeLike(to)
	target.MaxOutput = 4096
	if to == protocol.Responses {
		target.EncryptedReasoning = catalog.ClaimYes
	}
	u, err := protocol.Prepare(&r, sample(t, dir+"/request.json"), target, rewrites())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, dir+"/to-"+ext+".json", append(u.Body, '\n'))
}
