package protocol_test

import (
	"encoding/json/jsontext" //nolint:depguard // the oracle keeps members raw, which jsonx's Go values cannot; jsonx still decodes
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

func derive(s string) string { return "h(" + s + ")" }

// history is a stored chain: a user message and the compaction item the reply carried (OpenAI《Compaction》,
// https://developers.openai.com/api/docs/guides/compaction, read 2026-10-14; the item's id and encrypted_content are
// made up, as the guide shows none).
var history = [][]byte{
	[]byte(`{"type":"message","role":"user","content":"Let's begin a long coding task."}`),
	[]byte(`{"type":"compaction","id":"cmp_1","encrypted_content":"enc_1"}`),
}

const spliced = `{"type":"message","role":"user","content":"Let's begin a long coding task."},` +
	`{"type":"compaction","id":"cmp_1","encrypted_content":"enc_1"}`

var prepareCases = []struct {
	name      string
	format    protocol.Format
	encrypted bool // the target model returns encrypted reasoning
	continues bool
	body      string
	want      string
}{
	{
		// The thinking blocks are Anthropic's (《Extended thinking》, 《Compaction》, read 2026-10-14) with made-up
		// signatures; the escaped text and the leading space show that the bytes around a rewrite stay as written.
		"messages: routing parameters, end user, reasoning kept", protocol.Messages, false, false,
		`{"provider":{"order":["a"]}, "model":"claude-opus-5-5","metadata":{"user_id":"u\u00e9","x":1},` +
			`"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"caf\u00e9","signature":"sig_1"},` +
			`{"type":"redacted_thinking","data":"red_1"},{"type":"compaction","content":"sum"}]}],"axiom":{}, "max_tokens":5}`,
		`{ "model":"up","metadata":{"user_id":"h(ué)","x":1},` +
			`"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"caf\u00e9","signature":"sig_1"},` +
			`{"type":"redacted_thinking","data":"red_1"},{"type":"compaction","content":"sum"}]}], "max_tokens":5}`,
	},
	{
		"messages: two routing parameters first, null metadata", protocol.Messages, false, false,
		`{"usage":{"include":true},"session_id":"s","m\u006fdel":"m","metadata":null}`,
		`{"m\u006fdel":"up","metadata":null}`,
	},
	{
		// Codex's second turn after compaction, as the guide's input-array loop sends it.
		"responses: compaction kept, cache key derived", protocol.Responses, true, false,
		`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":"Let's begin a long coding task."},` +
			`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc_1"},` +
			`{"type":"compaction","id":"cmp_1","encrypted_content":"enc_2"},` +
			`{"type":"message","role":"user","content":"Now implement the next step."}],"store":false,` +
			`"include":["reasoning.encrypted_content"],"context_management":[{"type":"compaction","compact_threshold":200000}],` +
			`"prompt_cache_key":"conv_1","stream":true}`,
		`{"model":"up","input":[{"type":"message","role":"user","content":"Let's begin a long coding task."},` +
			`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc_1"},` +
			`{"type":"compaction","id":"cmp_1","encrypted_content":"enc_2"},` +
			`{"type":"message","role":"user","content":"Now implement the next step."}],"store":false,` +
			`"include":["reasoning.encrypted_content"],"context_management":[{"type":"compaction","compact_threshold":200000}],` +
			`"prompt_cache_key":"h(conv_1)","stream":true}`,
	},
	{
		"responses: empty previous_response_id with no history continues nothing", protocol.Responses, false, false,
		`{"model":"m","previous_response_id":"","input":"next"}`,
		`{"model":"up","input":"next","store":false}`,
	},
	{
		"responses: history before input items, storage off, end users derived", protocol.Responses, true, true,
		`{"model":"m","previous_response_id":"resp_1","input":[{"role":"user","content":"next"}],"store":true,` +
			`"user":"u","safety_identifier":"s"}`,
		`{"model":"up","input":[` + spliced + `,{"role":"user","content":"next"}],"store":false,` +
			`"user":"h(u)","safety_identifier":"h(s)","include":["reasoning.encrypted_content"]}`,
	},
	{
		"responses: history before string input, include left alone", protocol.Responses, false, true,
		`{"model":"m","input":"next","previous_response_id":"resp_1","user":null,"include":["x"]}`,
		`{"model":"up","input":[` + spliced + `,{"type":"message","role":"user","content":"next"}],"user":null,` +
			`"include":["x"],"store":false}`,
	},
	{
		"responses: history as the input, include null", protocol.Responses, true, true,
		`{"model":"m","previous_response_id":"resp_1","include":null}`,
		`{"model":"up","include":["reasoning.encrypted_content"],"store":false,"input":[` + spliced + `]}`,
	},
	{
		"responses: history into empty input, include without encrypted reasoning", protocol.Responses, true, true,
		`{"model":"m","previous_response_id":"resp_1","input":[ ],"include":["message.output_text.logprobs"]}`,
		`{"model":"up","input":[` + spliced + ` ],` +
			`"include":["reasoning.encrypted_content","message.output_text.logprobs"],"store":false}`,
	},
	{
		"responses: history in place of null input, empty include", protocol.Responses, true, true,
		`{"model":"m","previous_response_id":"resp_1","input":null,"include":[]}`,
		`{"model":"up","input":[` + spliced + `],"include":["reasoning.encrypted_content"],"store":false}`,
	},
}

func prepare(f protocol.Format, body []byte, encrypted, continues bool) ([]byte, error) {
	w := protocol.Rewrites{Identity: derive}
	if continues {
		w.History = history
	}
	return protocol.Prepare(f, body, protocol.Target{Model: "up", EncryptedReasoning: encrypted}, w)
}

// TestPrepare compares whole bodies, so a byte changed outside the rewrites fails it (README.md「同格式改写」, I1, I2).
func TestPrepare(t *testing.T) {
	for _, c := range prepareCases {
		t.Run(c.name, func(t *testing.T) {
			got, err := prepare(c.format, []byte(c.body), c.encrypted, c.continues)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.want, string(got)); diff != "" {
				t.Error(diff)
			}
		})
	}
}

// routingParameters are the members README.md「同格式改写」 lists, written out so that a name lost from Prepare's list shows.
var routingParameters = []string{"provider", "models", "route", "transforms", "plugins", "usage", "session_id", "axiom"}

// TestPrepareDropsRoutingParameters holds every routing parameter to being dropped (README.md「同格式改写」).
func TestPrepareDropsRoutingParameters(t *testing.T) {
	for _, f := range []protocol.Format{protocol.Messages, protocol.Responses} {
		want := `{"model":"up"}`
		if f == protocol.Responses {
			want = `{"model":"up","store":false}`
		}
		for _, name := range routingParameters {
			got, err := prepare(f, []byte(`{"model":"m","`+name+`":{}}`), false, false)
			if err != nil || string(got) != want {
				t.Errorf("format %d, %s: got %s (%v), want %s", f, name, got, err, want)
			}
		}
	}
}

// TestPrepareBuffer holds the output buffer to the room README.md「同格式改写」 promises: whatever Prepare adds, with
// derived identifiers as long as Rewrites.IdentityLen says, fits without the body being copied again. The body is small
// so that the allocator's rounding up leaves less room than what is added.
func TestPrepareBuffer(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef"
	body := []byte(`{"model":"m","previous_response_id":"r","input":"next","prompt_cache_key":"k","user":"u","safety_identifier":"s"}`)
	allocs := func(identity string, identityLen int) float64 {
		w := protocol.Rewrites{Identity: func(string) string { return identity }, IdentityLen: identityLen, History: history}
		return testing.AllocsPerRun(5, func() {
			if _, err := protocol.Prepare(protocol.Responses, body, protocol.Target{Model: "up", EncryptedReasoning: true}, w); err != nil {
				t.Fatal(err)
			}
		})
	}
	long := allocs(id, len(id))
	// A one-byte identifier replaces a one-byte original: the body grows only by what is added.
	if short := allocs("k", 0); long != short {
		t.Errorf("%d allocations with long identifiers, %d with short ones", int(long), int(short))
	}
	// Room to spare for the identifiers, which the added members must fit in as well.
	if roomy := allocs(id, 10*len(id)); long != roomy {
		t.Errorf("%d allocations with room for the identifiers, %d with room to spare", int(long), int(roomy))
	}
}

// TestPrepareBufferEscapes holds the room to the JSON escapes of the values put in: a model name and identifiers
// made of bytes that take six bytes each still fit in one allocation, as short ones do.
func TestPrepareBufferEscapes(t *testing.T) {
	nasty := strings.Repeat("\x01\"\\", 40)
	for _, c := range []struct {
		format protocol.Format
		body   string
	}{
		{protocol.Messages, `{"model":"m","metadata":{"user_id":"u"}}`},
		{protocol.Responses, `{"model":"m","prompt_cache_key":"k","user":"u","safety_identifier":"s"}`},
	} {
		allocs := func(model, identity string) float64 {
			w := protocol.Rewrites{Identity: func(string) string { return identity }, IdentityLen: len(identity)}
			return testing.AllocsPerRun(5, func() {
				if _, err := protocol.Prepare(c.format, []byte(c.body), protocol.Target{Model: model}, w); err != nil {
					t.Fatal(err)
				}
			})
		}
		if long, short := allocs(nasty, nasty), allocs("m", "u"); long != short {
			t.Errorf("format %d: %d allocations with escaped values, %d without", c.format, int(long), int(short))
		}
	}
}

// TestPrepareBufferDefaultIdentityLen holds the room for Identity's values to the default bound when IdentityLen is
// not set: values as long as the bound, all of bytes that need escaping, replacing originals that need none, fit in
// one allocation; a longer one is refused as the gateway's error.
func TestPrepareBufferDefaultIdentityLen(t *testing.T) {
	for _, c := range []struct {
		format protocol.Format
		body   string
	}{
		{protocol.Messages, `{"model":"m","metadata":{"user_id":"` + strings.Repeat("a", 128) + `"}}`},
		{protocol.Responses, `{"model":"m","prompt_cache_key":"` + strings.Repeat("a", 128) + `","user":"u","safety_identifier":"s"}`},
	} {
		prepareWith := func(n int) (float64, error) {
			id := strings.Repeat(`\`, n)
			w := protocol.Rewrites{Identity: func(string) string { return id }}
			var err error
			allocs := testing.AllocsPerRun(5, func() {
				_, err = protocol.Prepare(c.format, []byte(c.body), protocol.Target{Model: "m"}, w)
			})
			return allocs, err
		}
		long, err := prepareWith(128)
		if err != nil {
			t.Fatal(err)
		}
		short, err := prepareWith(1)
		if err != nil {
			t.Fatal(err)
		}
		if long != short {
			t.Errorf("format %d: %d allocations with 128 escaped bytes, %d with one", c.format, int(long), int(short))
		}
		if _, err := prepareWith(129); err == nil || errors.Is(err, protocol.ErrInvalid) {
			t.Errorf("format %d: got %v for 129 bytes, want the gateway's error", c.format, err)
		}
	}
}

// TestPrepareSamples holds the recorded requests, pretty-printed as published, to a change of the model alone, and
// of storage in Responses.
func TestPrepareSamples(t *testing.T) {
	for _, c := range []struct {
		format protocol.Format
		file   string
		model  string
		store  bool // Prepare adds "store":false, after the sample's last member
	}{
		{protocol.Messages, "messages/text/request.json", `"claude-opus-5-5"`, false},
		{protocol.Messages, "messages/thinking/request.json", `"claude-opus-5-5"`, false},
		{protocol.Messages, "messages/tool-use/request.json", `"claude-opus-5"`, false},
		{protocol.Responses, "responses/text/request.json", `"gpt-6-astra"`, true},
	} {
		t.Run(c.file, func(t *testing.T) {
			body := caseBody(t, c.file)
			got, err := prepare(c.format, body, false, false)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Replace(string(body), c.model, `"up"`, 1)
			if c.store {
				want = strings.Replace(want, `"stream": true`, `"stream": true,"store":false`, 1)
			}
			if diff := cmp.Diff(want, string(got)); diff != "" {
				t.Error(diff)
			}
		})
	}
}

var prepareRejects = []struct {
	name      string
	format    protocol.Format
	continues bool
	body      string
	want      string // the error's text
	invalid   bool   // the client's to fix, not the gateway's
}{
	{"chat", protocol.Chat, false, `{"model":"m"}`, "does not rewrite format 2 yet", false},
	{"continues without a history", protocol.Responses, false, `{"model":"m","previous_response_id":"r"}`, "disagree", false},
	{"history without continuing", protocol.Responses, true, `{"model":"m"}`, "disagree", false},
	{"end user not a string", protocol.Responses, false, `{"model":"m","user":1}`, "user is not a string", true},
	{"include not an array", protocol.Responses, false, `{"model":"m","include":"x"}`, "include is not an array", true},
	{"include holds a number", protocol.Responses, false, `{"model":"m","include":["x",1]}`, "include is not a string", true},
	{"metadata not an object", protocol.Messages, false, `{"model":"m","metadata":"x"}`, "metadata is not an object", true},
	{"user id not a string", protocol.Messages, false, `{"model":"m","metadata":{"user_id":1}}`, "user_id is not a string", true},
	{"input a number", protocol.Responses, true, `{"model":"m","previous_response_id":"r","input":1}`, "input is not", true},
}

func TestPrepareRejects(t *testing.T) {
	for _, c := range prepareRejects {
		t.Run(c.name, func(t *testing.T) {
			_, err := prepare(c.format, []byte(c.body), true, c.continues)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error saying %q", err, c.want)
			}
			if errors.Is(err, protocol.ErrInvalid) != c.invalid {
				t.Errorf("%v: ErrInvalid is %t", err, !c.invalid)
			}
		})
	}
}

// FuzzPrepare holds Prepare's output to the summary Inspect read from the input, which also holds it to jsonx's
// policy: admission's reservation and routing's capabilities stay true of what is sent, the model, the continued
// response and the history's bytes aside. Chat's seeds are read as Messages.
func FuzzPrepare(f *testing.F) {
	for _, c := range inspectCases {
		f.Add(uint8(c.format), caseBody(f, c.body))
	}
	for _, c := range prepareCases {
		f.Add(uint8(c.format), []byte(c.body))
	}
	for _, c := range prepareRejects {
		f.Add(uint8(c.format), []byte(c.body))
	}
	// Numbers no float64 holds, which Prepare passes on as written.
	f.Add(uint8(protocol.Messages), []byte(`{"model":"m","metadata":{"user_id":"u","n":1e1000}}`))
	f.Add(uint8(protocol.Responses), []byte(`{"model":"m","include":["x"],"n":1e1000}`))
	f.Fuzz(func(t *testing.T, format uint8, body []byte) {
		fm := protocol.Format(format % 2)
		r, err := protocol.Inspect(fm, body)
		if err != nil {
			return
		}
		out, err := prepare(fm, body, true, r.Previous != "")
		if want := rejects(t, fm, body); errors.Is(err, protocol.ErrInvalid) != want {
			t.Fatalf("%s: got %v, want ErrInvalid %t", body, err, want)
		}
		if errors.Is(err, protocol.ErrInvalid) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := protocol.Inspect(fm, out)
		if err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		r.Model, r.Previous, r.TextBytes = "up", "", r.TextBytes+int64(len(out)-len(body))
		if diff := cmp.Diff(r, got); diff != "" {
			t.Fatalf("%s:\n%s", out, diff)
		}
	})
}

// rejects reports whether Prepare must refuse a body Inspect accepted, decoding it to tell: a member only Prepare reads
// is neither null nor of the type README.md「同格式改写」 rewrites. Include is read as for an encrypting model. Values
// stay raw, as decoding them to Go values would refuse numbers no float64 holds, which Prepare passes on as written.
func rejects(t *testing.T, f protocol.Format, body []byte) bool {
	t.Helper()
	decode := func(v jsontext.Value, into any) {
		if err := jsonx.Unmarshal(v, into); err != nil {
			t.Fatal(err)
		}
	}
	// is reports whether v is absent, null or of kind k; a null decodes to a nil map or slice.
	is := func(v jsontext.Value, k jsontext.Kind) bool { return v == nil || v.Kind() == 'n' || v.Kind() == k }
	notString := func(v jsontext.Value) bool { return !is(v, '"') }
	var top map[string]jsontext.Value
	decode(body, &top)
	if f == protocol.Messages {
		var metadata map[string]jsontext.Value
		if !is(top["metadata"], '{') {
			return true
		} else if top["metadata"] != nil {
			decode(top["metadata"], &metadata)
		}
		return notString(metadata["user_id"])
	}
	var include []jsontext.Value
	if !is(top["include"], '[') {
		return true
	} else if top["include"] != nil {
		decode(top["include"], &include)
	}
	return slices.ContainsFunc(include, notString) ||
		slices.ContainsFunc([]jsontext.Value{top["user"], top["prompt_cache_key"], top["safety_identifier"]}, notString)
}

func BenchmarkPrepare(b *testing.B) {
	for _, f := range []protocol.Format{protocol.Messages, protocol.Responses} {
		for _, size := range sizes {
			body := body(f, size, "")
			b.Run(fmt.Sprintf("format=%d/size=%d", f, size), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					if _, err := prepare(f, body, true, false); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
