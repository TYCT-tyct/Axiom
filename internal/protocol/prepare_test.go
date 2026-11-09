package protocol_test

import (
	"encoding/json/jsontext" //nolint:depguard // the oracle keeps members raw, which jsonx's Go values cannot; jsonx still decodes
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

func derive(s string) string { return "h(" + s + ")" }

// rewrites are the gateway's values the protocol tests prepare with: an identity that tags what it derives, and a device.
func rewrites() protocol.Rewrites { return protocol.Rewrites{Identity: derive, Device: "d"} }

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
		`{ "model":"up","metadata":{"user_id":"{\"device_id\":\"d\",\"account_uuid\":\"\",\"session_id\":\"h(ué)\"}","x":1},` +
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

// inspected is the summary Prepare is given, read outside the code whose allocations a test counts.
func inspected(t *testing.T, f protocol.Format, body []byte) protocol.Request {
	t.Helper()
	r, err := protocol.Inspect(f, body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// claim is the spec's answer for an endpoint that does, or does not, take what a test asks of it.
func claim(yes bool) catalog.Claim {
	if yes {
		return catalog.ClaimYes
	}
	return catalog.ClaimNo
}

func prepare(f protocol.Format, body []byte, encrypted, continues bool) ([]byte, error) {
	w := rewrites()
	if continues {
		w.History = history
	}
	r, err := protocol.Inspect(f, body)
	if err != nil {
		return nil, err
	}
	u, err := protocol.Prepare(&r, body, protocol.Target{Format: f, Model: "up", EncryptedReasoning: claim(encrypted)}, w)
	return u.Body, err
}

// TestPrepare compares whole bodies, so a byte changed outside the rewrites fails it (README.md「同格式改写」, I1, I2).
func TestPrepare(t *testing.T) {
	t.Parallel()
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
var routingParameters = []string{"provider", "models", "preset", "fallbacks", "route", "transforms", "plugins", "usage", "session_id", "axiom"}

// TestPrepareDropsRoutingParameters holds every routing parameter to being dropped (README.md「同格式改写」).
func TestPrepareDropsRoutingParameters(t *testing.T) {
	t.Parallel()
	values := map[string]string{"session_id": `"s"`, "preset": `"ignored"`, "fallbacks": `[]`, "models": `["alt"]`, "plugins": `[]`, "transforms": `[]`}
	for _, f := range []protocol.Format{protocol.Messages, protocol.Responses, protocol.Chat} {
		want := `{"model":"up"}`
		if f == protocol.Responses {
			want = `{"model":"up","store":false}`
		}
		for _, name := range routingParameters {
			value, known := values[name]
			if !known {
				value = "{}"
			}
			got, err := prepare(f, []byte(`{"model":"m","`+name+`":`+value+`}`), false, false)
			if err != nil || string(got) != want {
				t.Errorf("format %d, %s: got %s (%v), want %s", f, name, got, err, want)
			}
		}
	}
}

// TestPrepareSamples holds the recorded requests, pretty-printed as published, to a change of the model alone, and
// of storage in Responses.
func TestPrepareSamples(t *testing.T) {
	t.Parallel()
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
		{protocol.Chat, "chat/text/request.json", `"gpt-6-astra"`, false},
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
	t.Parallel()
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
// response and the history's bytes aside.
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
		fm := protocol.Format(format % 3)
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
		r.Preset, r.SessionID = "", ""
		// The end user goes upstream as the tenant's derivation of it, not as the client wrote it.
		r.EndUser = got.EndUser
		r.Model, r.Models, r.Provider, r.Previous, r.TextBytes = "up", nil, nil, "", r.TextBytes+int64(len(out)-len(body)) // models and provider are routing's, deleted
		r = withSmallerCap(r, got)
		if diff := cmp.Diff(r, got, cmpopts.IgnoreUnexported(protocol.Request{})); diff != "" {
			t.Fatalf("%s:\n%s", out, diff)
		}
	})
}

// withSmallerCap is r with the output cap of got when that is the smaller of the two caps a Chat request may have: they are
// sent as one, which is the smaller.
func withSmallerCap(r, got protocol.Request) protocol.Request {
	if r.MaxOutput > 0 && got.MaxOutput > 0 && got.MaxOutput <= r.MaxOutput {
		r.MaxOutput = got.MaxOutput
	}
	return r
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
	if f == protocol.Chat {
		return slices.ContainsFunc([]jsontext.Value{top["user"], top["prompt_cache_key"], top["safety_identifier"]}, notString)
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
			prepare := prepareInspected()
			b.Run(fmt.Sprintf("format=%d/size=%d", f, size), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					if err := prepare(f, body); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// TestPrepareUpstream: the usage context comes from the request: a 1-hour cache breakpoint, and the service tier only
// when the request names one alone; a format not converted to the endpoint's is the endpoint's shortcoming, not the
// client's error.
func TestPrepareUpstream(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, body string
		to         protocol.Format
		want       protocol.UsageContext
		supported  bool
	}{
		{"defaults", `{"model":"m","messages":[]}`, protocol.Messages, protocol.UsageContext{}, true},
		{
			"1-hour breakpoint", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,
			protocol.Messages,
			protocol.UsageContext{CacheTTL1h: true},
			true,
		},
		{
			"one tier named", `{"model":"m","service_tier":"standard_only","speed":"fast"}`, protocol.Messages,
			protocol.UsageContext{Tier: catalog.ServiceTierFast},
			true,
		},
		{"auto names two", `{"model":"m","service_tier":"auto"}`, protocol.Messages, protocol.UsageContext{}, true},
		{"to another format", `{"model":"m"}`, protocol.Responses, protocol.UsageContext{}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := protocol.Inspect(protocol.Messages, []byte(c.body))
			if err != nil {
				t.Fatal(err)
			}
			u, err := protocol.Prepare(&r, []byte(c.body), protocol.Target{Format: c.to, Model: "up"}, protocol.Rewrites{Identity: derive})
			var unsupported *protocol.NotSupported
			if u.Usage != c.want || (err == nil) != c.supported || !c.supported && !errors.As(err, &unsupported) {
				t.Errorf("usage %+v, error %v", u.Usage, err)
			}
		})
	}
}

// TestPrepareOutputCap: a Responses request without an output cap, or with null for it, is sent with Target.MaxOutput,
// the cap it was quoted at, whose member the buffer has room for; one that sets a cap keeps it, and a target without
// a cap to send leaves the request as it is (README.md「同格式改写」, I1).
func TestPrepareOutputCap(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, body string
		max        int64
		want       string
	}{
		{"none", `{"model":"m","input":"hi"}`, 7000, `{"model":"up","input":"hi","store":false,"max_output_tokens":7000}`},
		{"the largest", `{"model":"m","input":"hi"}`, math.MaxInt64, `{"model":"up","input":"hi","store":false,"max_output_tokens":9223372036854775807}`},
		{"null", `{"model":"m","max_output_tokens":null,"input":"hi"}`, 7000, `{"model":"up","max_output_tokens":7000,"input":"hi","store":false}`},
		{"the largest for null", `{"model":"m","max_output_tokens":null}`, math.MaxInt64, `{"model":"up","max_output_tokens":9223372036854775807,"store":false}`},
		{"set", `{"model":"m","max_output_tokens":50,"input":"hi"}`, 7000, `{"model":"up","max_output_tokens":50,"input":"hi","store":false}`},
		{"nothing to send", `{"model":"m","input":"hi"}`, 0, `{"model":"up","input":"hi","store":false}`},
	} {
		r := inspected(t, protocol.Responses, []byte(c.body))
		target, w := protocol.Target{Format: protocol.Responses, Model: "up", MaxOutput: c.max}, protocol.Rewrites{Identity: derive}
		u, err := protocol.Prepare(&r, []byte(c.body), target, w)
		if err != nil || string(u.Body) != c.want {
			t.Errorf("%s: got %s (%v), want %s", c.name, u.Body, err, c.want)
		}
	}
}
