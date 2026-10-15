package protocol_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

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
	history   bool
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
			got, err := prepare(c.format, []byte(c.body), c.encrypted, c.history)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.want, string(got)); diff != "" {
				t.Error(diff)
			}
		})
	}
}

// TestPrepareSamples holds the recorded requests, pretty-printed as published, to a change of the model alone, and
// of storage in Responses.
func TestPrepareSamples(t *testing.T) {
	for _, c := range []struct {
		format protocol.Format
		file   string
		model  string
		add    [2]string
	}{
		{protocol.Messages, "messages/text/request.json", `"claude-opus-5-5"`, [2]string{}},
		{protocol.Messages, "messages/thinking/request.json", `"claude-opus-5-5"`, [2]string{}},
		{protocol.Messages, "messages/tool-use/request.json", `"claude-opus-5"`, [2]string{}},
		{protocol.Responses, "responses/text/request.json", `"gpt-6-astra"`, [2]string{`"stream": true`, `"stream": true,"store":false`}},
	} {
		t.Run(c.file, func(t *testing.T) {
			body := caseBody(t, c.file)
			got, err := prepare(c.format, body, false, false)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Replace(strings.Replace(string(body), c.model, `"up"`, 1), c.add[0], c.add[1], 1)
			if diff := cmp.Diff(want, string(got)); diff != "" {
				t.Error(diff)
			}
		})
	}
}

func TestPrepareRejects(t *testing.T) {
	for _, c := range []struct {
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
		{"metadata not an object", protocol.Messages, false, `{"model":"m","metadata":"x"}`, "metadata is not an object", true},
		{"input a number", protocol.Responses, true, `{"model":"m","previous_response_id":"r","input":1}`, "input is not", true},
	} {
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
	f.Fuzz(func(t *testing.T, format uint8, body []byte) {
		fm := protocol.Format(format % 2)
		r, err := protocol.Inspect(fm, body)
		if err != nil {
			return
		}
		out, err := prepare(fm, body, true, r.Previous != "")
		if errors.Is(err, protocol.ErrInvalid) {
			return // a member Inspect does not read has the wrong type
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
