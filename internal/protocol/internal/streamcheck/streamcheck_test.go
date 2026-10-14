package streamcheck_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/streamcheck"
)

// checks maps each format's directory under internal/protocol/testdata to its checker.
var checks = map[string]func(io.Reader) error{
	"messages":  streamcheck.Messages,
	"responses": streamcheck.Responses,
	"chat":      streamcheck.Chat,
}

// samples is internal/protocol/testdata, laid out as internal/protocol/README.md describes.
var samples = os.DirFS(filepath.Join("..", "..", "testdata"))

// TestSamples holds every sample to the convention and checks its upstream stream.
func TestSamples(t *testing.T) {
	for format, check := range checks {
		dirs, err := fs.Glob(samples, format+"/*")
		if err != nil || len(dirs) == 0 {
			t.Fatalf("no samples for %s: %v", format, err)
		}
		for _, dir := range dirs {
			t.Run(dir, func(t *testing.T) { checkSample(t, dir, check) })
		}
	}
}

func checkSample(t *testing.T, dir string, check func(io.Reader) error) {
	var meta struct {
		Source string `json:"source"`
		Date   string `json:"date"`
	}
	if err := jsonx.Unmarshal(read(t, dir+"/meta.json"), &meta); err != nil || meta.Source == "" {
		t.Fatalf("meta.json: %v, or no source", err)
	}
	if _, err := time.Parse(time.DateOnly, meta.Date); err != nil {
		t.Errorf("meta.json date: %v", err)
	}
	if err := jsonx.Validate(read(t, dir+"/request.json")); err != nil {
		t.Errorf("request.json: %v", err)
	}
	if err := check(bytes.NewReader(read(t, dir+"/upstream.sse"))); err != nil {
		t.Errorf("upstream.sse: %v", err)
	}
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(samples, name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// stream writes each event as one data line.
func stream(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String()
}

type checkCase struct {
	name, stream string
	want         string // a phrase of the error; "" when the stream is valid
}

func run(t *testing.T, check func(io.Reader) error, cases []checkCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := check(strings.NewReader(tc.stream))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("valid stream rejected: %v", err)
			case tc.want != "" && (!errors.Is(err, streamcheck.ErrInvalid) || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want ErrInvalid saying %q", err, tc.want)
			}
		})
	}
}

const (
	msgStart = `{"type":"message_start","message":{"content":[]}}`
	msgDelta = `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`
	msgStop  = `{"type":"message_stop"}`
)

func block(i int, kind string) string {
	return fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":%q}}`, i, kind)
}

func delta(i int, kind string) string {
	return fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":%q}}`, i, kind)
}

func stop(i int) string { return fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i) }

func TestMessages(t *testing.T) {
	text := []string{block(0, "text"), delta(0, "text_delta"), stop(0)}
	run(t, streamcheck.Messages, []checkCase{
		{"ping before the start, unknown types inside", stream(`{"type":"ping"}`, msgStart, `{"type":"later"}`, msgDelta, msgStop), ""},
		{"blocks without deltas", stream(msgStart, block(0, "fallback"), stop(0), block(1, "redacted_thinking"), stop(1), msgDelta, msgStop), ""},
		{"an error ends it", stream(msgStart, block(0, "text"), `{"type":"error","error":{"type":"overloaded_error"}}`), ""},
		{"a second start", stream(msgStart, msgStart), "a second message_start"},
		{"no start", stream(text...), "before message_start"},
		{"overlapping blocks", stream(msgStart, block(0, "text"), block(1, "text")), "starts while block 0 is open"},
		{"skipped index", stream(msgStart, block(1, "text")), "without index 0"},
		{"block without a type", stream(msgStart, `{"type":"content_block_start","index":0,"content_block":{}}`), "has no type"},
		{"delta after the stop", stream(append([]string{msgStart}, append(text, delta(0, "text_delta"))...)...), "not open"},
		{"delta to another block", stream(msgStart, block(0, "text"), delta(1, "text_delta")), "not open"},
		{"delta to an earlier block", stream(msgStart, block(0, "text"), stop(0), block(1, "text"), delta(0, "text_delta")), "not open"},
		{"wrong delta type", stream(msgStart, block(0, "text"), delta(0, "input_json_delta")), `"input_json_delta" in a text block`},
		{"message_delta inside a block", stream(msgStart, block(0, "text"), msgDelta), "while block 0 is open"},
		{"stop without message_delta", stream(msgStart, msgStop), "without a message_delta"},
		{"stop with a block open", stream(msgStart, msgDelta, block(0, "text"), msgStop), "before the last block stops"},
		{"an event after the stop", stream(msgStart, msgDelta, msgStop, `{"type":"ping"}`), "after the stream ended"},
		{"cut short", stream(append([]string{msgStart}, text...)...), "ends before message_stop"},
		{"[DONE]", stream(msgStart, "[DONE]"), "not an event object"},
	})
}

// seq numbers events from 0, the way OpenAI does.
func seq(events ...string) []string {
	for i, e := range events {
		events[i] = fmt.Sprintf(`{"sequence_number":%d,`, i) + e[1:]
	}
	return events
}

const (
	created   = `{"type":"response.created","response":{"output":[]}}`
	msgAdded  = `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message"}}`
	partAdded = `{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text"}}`
	textDelta = `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hi"}`
	partDone  = `{"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0}`
	msgDone   = `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message"}}`
	completed = `{"type":"response.completed","response":{"output":[{}]}}`
)

func TestResponses(t *testing.T) {
	message := []string{created, msgAdded, partAdded, textDelta, partDone, msgDone}
	with := func(events ...string) string {
		return stream(seq(append(append([]string{}, message...), events...)...)...)
	}
	call := `{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call"}}`
	args := `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{"}`
	reasoning := []string{
		created, `{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning"}}`,
		`{"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text"}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"x"}`,
		`{"type":"response.reasoning_summary_part.done","item_id":"rs_1","output_index":0,"summary_index":0}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning"}}`, completed,
	}
	run(t, streamcheck.Responses, []checkCase{
		{"message", with(completed), ""},
		{"reasoning summary", stream(seq(reasoning...)...), ""},
		{"unknown types and gaps in sequence_number", stream(seq(created)[0], `{"sequence_number":5,"type":"later"}`, `{"sequence_number":9,"type":"response.failed"}`), ""},
		{"failed with an open item", stream(seq(created, msgAdded, `{"type":"response.failed"}`)...), ""},
		{"an error ends it", stream(seq(`{"type":"error","code":"server_error"}`)...), ""},
		{"no sequence_number", stream(created), "sequence_number missing"},
		{"sequence_number repeats", stream(seq(created, msgAdded)[0], seq(created)[0]), "not above 0"},
		{"no created", stream(seq(msgAdded)...), "before response.created"},
		{"a second created", stream(seq(created, created)...), "a second response.created"},
		{"skipped output_index", stream(seq(created, strings.Replace(msgAdded, `"output_index":0`, `"output_index":1`, 1))...), "without output_index 0"},
		{"item without id", stream(seq(created, strings.Replace(msgAdded, `"msg_1"`, `""`, 1))...), "an item id"},
		{"item without type", stream(seq(created, strings.Replace(msgAdded, `,"type":"message"`, "", 1))...), "or a type"},
		{"skipped content_index", stream(seq(created, msgAdded, strings.Replace(partAdded, `"content_index":0`, `"content_index":1`, 1))...), "next part index 0"},
		{"delta after the part is done", stream(seq(created, msgAdded, partAdded, partDone, textDelta)...), "part that is not open"},
		{"delta after the item is done", stream(seq(append(message, textDelta)...)...), "output item that is not open"},
		{"another item_id", stream(seq(created, msgAdded, partAdded, strings.Replace(textDelta, "msg_1", "msg_2", 1))...), "output item that is not open"},
		{"text delta to a refusal part", stream(seq(created, msgAdded, strings.Replace(partAdded, "output_text", "refusal", 1), textDelta)...), "for a refusal part"},
		{"text delta without content_index", stream(seq(created, msgAdded, strings.Replace(textDelta, `"content_index":0,`, "", 1))...), "without content_index"},
		{"arguments for a message", stream(seq(created, msgAdded, args)...), "for a message item"},
		{"arguments for a call", stream(seq(created, call, args, strings.Replace(call, "added", "done", 1), completed)...), ""},
		{"item done with an open part", stream(seq(created, msgAdded, partAdded, msgDone)...), "while a part of item 0 is open"},
		{"completed with an open item", stream(seq(created, msgAdded, completed)...), "while item 0 is open"},
		{"completed without its items", with(strings.Replace(completed, "{}", "", 1)), "lists 0 output items, the stream added 1"},
		{"an event after completed", with(completed, `{"type":"later"}`), "after the stream ended"},
		{"cut short", with(), "before the response completes"},
	})
}

const (
	chunk = `{"id":"c1","object":"chat.completion.chunk","choices":[%s]}`
	usage = `{"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"total_tokens":3}}`
	// failure is OpenRouter's mid-stream error chunk ("Errors", read 2026-10-14).
	failure = `{"id":"c1","object":"chat.completion.chunk","error":{"code":502,"message":"upstream failed"},"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}`
)

func choice(format string, args ...any) string {
	return fmt.Sprintf(chunk, fmt.Sprintf(format, args...))
}

func TestChat(t *testing.T) {
	text := choice(`{"index":0,"delta":{"content":"Hi"},"finish_reason":null}`)
	finish := choice(`{"index":0,"delta":{},"finish_reason":"stop"}`)
	callStart := func(i int) string {
		return choice(`{"index":0,"delta":{"tool_calls":[{"index":%d,"id":"call_%d","type":"function","function":{"name":"f","arguments":""}}]}}`, i, i)
	}
	callArgs := func(i int) string {
		return choice(`{"index":0,"delta":{"tool_calls":[{"index":%d,"function":{"arguments":"{}"}}]}}`, i)
	}
	run(t, streamcheck.Chat, []checkCase{
		{"interleaved tool calls and usage", stream(callStart(0), callStart(1), callArgs(0), callArgs(1), finish, usage, "[DONE]"), ""},
		{"two choices", stream(text, choice(`{"index":1,"delta":{},"finish_reason":"length"}`), finish, "[DONE]"), ""},
		{"an error chunk, then [DONE]", stream(text, failure, "[DONE]"), ""},
		{"an error chunk ends it", stream(text, failure), ""},
		{"another object", stream(strings.Replace(text, ".chunk", "", 1)), `object "chat.completion"`},
		{"another id", stream(text, strings.Replace(finish, "c1", "c2", 1)), `id "c2"`},
		{"no id", stream(strings.Replace(text, `"c1"`, `""`, 1)), `id ""`},
		{"choice without index", stream(choice(`{"delta":{}}`)), "without an index"},
		{"delta after finish", stream(finish, text), "continues after its finish_reason"},
		{"call without its name", stream(strings.Replace(callStart(0), `"name":"f",`, "", 1)), "starts call 0 with its id and name"},
		{"call without its id", stream(strings.Replace(callStart(0), `"id":"call_0",`, "", 1)), "starts call 0 with its id and name"},
		{"skipped call index", stream(callStart(1)), "starts call 0"},
		{"continuing a call not started", stream(callStart(0), callArgs(1)), "starts call 1"},
		{"usage with choices", stream(finish, strings.Replace(usage, "[]", `[{"index":0,"delta":{}}]`, 1)), "usage chunk with choices"},
		{"usage before finish", stream(text, usage), "before every choice finished"},
		{"chunk after usage", stream(finish, usage, text), "after the error or usage chunk"},
		{"chunk after an error", stream(`{"error":{}}`, text), "after the error or usage chunk"},
		{"[DONE] before finish", stream(text, "[DONE]"), "[DONE] before every choice finished"},
		{"an event after [DONE]", stream(finish, "[DONE]", "[DONE]"), "after [DONE]"},
		{"cut short", stream(finish), "ends before [DONE]"},
	})
}

// FuzzCheck feeds every checker arbitrary streams, seeded with the samples: a hostile index or event order must
// produce an error, never a panic or an allocation sized by the input's numbers.
func FuzzCheck(f *testing.F) {
	paths, err := fs.Glob(samples, "*/*/upstream.sse")
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range paths {
		data, err := fs.ReadFile(samples, path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(stream(msgStart, block(1<<62, "text"), delta(-1, "x"))))
	f.Fuzz(func(_ *testing.T, data []byte) {
		for _, check := range checks {
			_ = check(strings.NewReader(string(data))) //nolint:errcheck // only panics matter here
		}
	})
}
