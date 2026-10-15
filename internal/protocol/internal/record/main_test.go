package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
	"axiom/internal/protocol/internal/streamcheck"
)

const key = "local-test-key"

// esc writes a JSON escape for each %xx in s, the byte xx as \u00xx, so a test can spell one string two ways.
func esc(s string) string { return strings.ReplaceAll(s, "%", `\u00`) }

// serve answers with status and stream, and checks that the request comes on the format's path with the key. hits
// is atomic: the race detector cannot see the order the network imposes between the handler and the test.
func serve(t *testing.T, format string, status int, stream string) (url string, hits *atomic.Int32) {
	t.Helper()
	hits = new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth := map[string]string{"messages": r.Header.Get("X-Api-Key")}[format]
		if format != "messages" {
			auth = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if want := formats[format].endpoint; r.Method != http.MethodPost || r.URL.Path != want || auth != key {
			t.Errorf("got %s %s with key %q, want POST %s with the key", r.Method, r.URL.Path, auth, want)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, stream) //nolint:errcheck // the recorder reports a short stream
	}))
	t.Cleanup(srv.Close)
	if format != "messages" {
		return srv.URL + "/v1", hits // OpenAI base URLs end in /v1
	}
	return srv.URL, hits
}

func env(url string) func(string) string {
	return func(name string) string {
		return map[string]string{"ANTHROPIC_BASE_URL": url, "OPENAI_BASE_URL": url, "ANTHROPIC_API_KEY": key, "OPENAI_API_KEY": key}[name]
	}
}

func file(t testing.TB, dir, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS(dir), name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// requestFile writes the request where the recorder reads it.
func requestFile(t *testing.T, request string) string {
	name := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(name, []byte(request), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

// hostile is a block of comments that broke the byte scanner the recorder once had: a stray quote, a line that
// looks like the start of a tool's input, and a line ending in a backslash.
const hostile = ": a stray \" in a comment\n: \"input\": {\n: ends in a backslash \\\n\n"

var comments = regexp.MustCompile(`(?m)^:.*\n\n`)

// TestRecord records the committed samples from a local upstream that sends them after the hostile comments, with
// LF, CR or CRLF line endings: the stream written is the sample's events, written the same way whatever the line
// endings, with IDs replaced at their positions and mapped alike in both files, and passes the samples test. The
// requests hold what must stay: tool arguments, names and text that look like IDs, a short ID next to a long one.
func TestRecord(t *testing.T) {
	samples := filepath.Join("..", "..", "testdata")
	toolID, callID := "toolu_01T1x1fJ34qAmk2tNTrN7Up6", "call_"+strings.Repeat("A1", 12)
	escapedToolID := esc("%74") + toolID[1:]            // the stream's spelling is raw
	opaque := `"gAAsk-` + strings.Repeat("a", 24) + `"` // "sk-" inside base64url, as in encrypted reasoning
	for _, tc := range []struct {
		format, sample, request, ids string // ids: original and placeholder pairs, for both files
		eol                          string
	}{
		{
			"messages", "tool-use", `{"stream":true,"tools":[{"name":"call_weather","input_schema":{"properties":{"msg_text":{}},` +
				`"required":["city_id","msg_text"]}},{"name":"call_get_weather_forecast"}],"messages":[{"role":"assistant","content":[` +
				`{"type":"tool_use","id":"toolu_earlier","name":"call_weather","input":{}},` +
				`{"type":"tool_use","id":"` + toolID + `","name":"call_weather","input":{"id":"call_customer","user_id":"msg_x"}}]},` +
				`{"role":"user","content":[{"type":"text","text":"\" mention \"msg_abcdefghijklmnopqrstuvwx\""},` +
				`{"type":"tool_result","tool_use_id":"` + escapedToolID + `"}]}]}`,
			"toolu_earlier toolu_1 " + toolID + " toolu_2 " + escapedToolID + " toolu_2 msg_014p7gG3wDgGV9EUtLvnow3U msg_1", "\r",
		},
		{
			"responses", "text", `{"stream":true,"metadata":{"blob":` + opaque + `,"user_id":"user_msg_1"},"input":[{"type":"function_call_output","call_id":"call_1",` +
				`"output":""},{"type":"function_call_output","call_id":"` + callID + `","output":""}]}`,
			"call_1 call_1 " + callID + " call_2 resp_67c9fdcecf488190bdd9a0409de3a1ec07b8b0ad4e5eb654 resp_1 " +
				"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654 msg_1", "\n",
		},
		{
			"chat", "text", `{"stream":true,"messages":[{"role":"assistant","tool_calls":[{"id":"call_abc","type":"function",` +
				`"function":{"name":"call_lookup","arguments":"{\"id\":\"call_abc\"}"}}]},{"role":"tool","tool_call_id":"call_abc","content":"call_abc"}]}`,
			"chatcmpl-123 chatcmpl-1", "\r\n",
		},
	} {
		t.Run(tc.format, func(t *testing.T) {
			sent := string(file(t, filepath.Join(samples, tc.format, tc.sample), "upstream.sse"))
			url, _ := serve(t, tc.format, http.StatusOK, strings.ReplaceAll(hostile+sent, "\n", tc.eol))
			testdata := t.TempDir()
			if err := record(t.Context(), env(url), testdata, tc.format, "recorded", requestFile(t, tc.request)); err != nil {
				t.Fatal(err)
			}
			ids := strings.NewReplacer(strings.Fields(tc.ids)...)
			request := ids.Replace(tc.request)
			if tc.format == "chat" { // only the ID positions change, not the arguments or the content that repeat the ID
				request = strings.Replace(strings.Replace(request, `"id":"call_abc"`, `"id":"call_1"`, 1), `"tool_call_id":"call_abc"`, `"tool_call_id":"call_1"`, 1)
			}
			checkRecorded(t, filepath.Join(testdata, tc.format, "recorded"), tc.format, request, ids.Replace(comments.ReplaceAllString(sent, "")))
		})
	}
}

// checkRecorded checks that dir holds exactly the three files, with the request and stream wanted, and that they pass
// the samples test.
func checkRecorded(t *testing.T, dir, format, request, stream string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 3 {
		t.Fatalf("want exactly the three sample files, got %v (%v)", entries, err)
	}
	for name, want := range map[string]string{"request.json": request, "upstream.sse": stream} {
		if got := file(t, dir, name); string(got) != want {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, want)
		}
	}
	meta := file(t, dir, "meta.json")
	if !bytes.Contains(meta, []byte("Rebuilt from the upstream's events")) {
		t.Errorf("meta.json does not say the stream is rebuilt:\n%s", meta)
	}
	if err := streamcheck.Sample(formats[format].check, file(t, dir, "request.json"), file(t, dir, "upstream.sse"), meta); err != nil {
		t.Error(err)
	}
}

// TestJSON runs values from a request and a stream through one scrubber, as a recording does: only the format's ID
// positions change, the same ID gets the same placeholder however it is escaped, and distinct IDs never share one.
func TestJSON(t *testing.T) {
	s := scrubber{key, map[string]string{}, map[string]int{}}
	for _, tc := range []struct{ name, format, data, want, typ string }{
		{"an ID", "chat", `{"messages":[{"tool_call_id":"call_1"}]}`, `{"messages":[{"tool_call_id":"call_1"}]}`, ""},
		{
			"the same ID escaped, and a new one with an escaped prefix", "chat",
			`{"messages":[{"tool` + esc("%5f") + `call_id":"call` + esc("%5f") + `1"},{"tool_call_id":"` + esc("%63") + `all_x"}]}`,
			`{"messages":[{"tool` + esc("%5f") + `call_id":"call_1"},{"tool_call_id":"call_2"}]}`, "",
		},
		{
			"a tool's input in a request", "messages",
			`{"messages":[{"content":[{"type":"tool_use","id":"toolu_abc","input":{"q":"}","id":"call_customer","n":{"tool_use_id":"msg_x"}}}]}]}`,
			`{"messages":[{"content":[{"type":"tool_use","id":"toolu_1","input":{"q":"}","id":"call_customer","n":{"tool_use_id":"msg_x"}}}]}]}`, "",
		},
		{
			"the same tool use in the stream", "messages",
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_abc","input":{}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","input":{}}}`, "content_block_start",
		},
		{
			"metadata, and input that holds items", "responses",
			`{"metadata":{"user_id":"msg_user","item_id":"msg_m"},"input":[{"type":"function_call_output","call_id":"call_y","id":"fc_y"}]}`,
			`{"metadata":{"user_id":"msg_user","item_id":"msg_m"},"input":[{"type":"function_call_output","call_id":"call_3","id":"fc_1"}]}`, "",
		},
		{"an ID-like name or text", "chat", `{"call_id":"call_z","tags":["id","call_z"],"user_id":"user_msg_1"}`, `{"call_id":"call_z","tags":["id","call_z"],"user_id":"user_msg_1"}`, ""},
		{"a placeholder-like ID and the one after it", "chat", `{"id":"chatcmpl-1"}`, `{"id":"chatcmpl-1"}`, ""},
		{"", "chat", `{"id":"chatcmpl-x"}`, `{"id":"chatcmpl-2"}`, ""},
		{"an ID without a known prefix", "chat", `{"id":"gen-17"}`, `{"id":"gen-1"}`, ""},
		{"an ID without any prefix", "chat", `{"id":"017"}`, `{"id":"id_1"}`, ""},
		{"an empty ID, and one that is not a string", "responses", `{"item_id":"","previous_response_id":null}`, `{"item_id":"","previous_response_id":null}`, ""},
	} {
		got, typ, err := s.json([]byte(tc.data), formats[tc.format].ids)
		if err != nil || string(got) != tc.want || typ != tc.typ {
			t.Errorf("%s: got %s, type %q (%v)\nwant %s, type %q", tc.name, got, typ, err, tc.want, tc.typ)
		}
	}
}

// TestStream checks how a stream is rebuilt: its events alone, each written the same way, whatever the framing the
// upstream chose.
func TestStream(t *testing.T) {
	for _, tc := range []struct{ name, format, stream, want string }{
		{
			"framing", "messages", "\xef\xbb\xbf: comment\rid: 7\rretry: 10\revent: other\rdata: {\"type\":\"ping\"}\r\r",
			"event: ping\ndata: {\"type\":\"ping\"}\n\n",
		},
		{"data on two lines", "responses", "data: {\"type\":\ndata: \"x\"}\n\n", "event: x\ndata: {\"type\":\ndata: \"x\"}\n\n"},
		{"a type that would break a line", "messages", `data: {"type":"a\ndata: {}"}` + "\n\n", `data: {"type":"a\ndata: {}"}` + "\n\n"},
		{"no event lines, and the end", "chat", "event: x\ndata: {\"type\":\"x\"}\n\ndata: [DONE]\n\n", "data: {\"type\":\"x\"}\n\ndata: [DONE]\n\n"},
	} {
		s := scrubber{key, map[string]string{}, map[string]int{}}
		if got, err := s.stream(formats[tc.format], []byte(tc.stream)); err != nil || string(got) != tc.want {
			t.Errorf("%s: got %q (%v), want %q", tc.name, got, err, tc.want)
		}
	}
}

// FuzzStream checks, for any stream, that a rebuilt stream never holds the key and reads back as the same events,
// each the same JSON value but for the strings at the format's ID positions, which map one to one onto placeholders.
// It builds JSON Pointers itself, from the decoded value, rather than trusting the decoder the recorder uses.
func FuzzStream(f *testing.F) {
	for i, stream := range streamSeeds(f) {
		f.Add(uint8(i), stream)
	}
	f.Fuzz(func(t *testing.T, format uint8, stream string) {
		up := formats[[]string{"messages", "responses", "chat"}[format%3]]
		s := scrubber{key, map[string]string{}, map[string]int{}}
		out, err := s.stream(up, []byte(stream))
		if err != nil {
			return
		}
		if bytes.Contains(out, []byte(key)) {
			t.Fatalf("the key is written: %q", out)
		}
		in, got := events(t, []byte(stream)), events(t, out)
		if len(in) != len(got) {
			t.Fatalf("%d events read back, want %d:\n%q", len(got), len(in), out)
		}
		placeholders := map[string]string{}
		for i := range in {
			sameEvent(t, up.ids, placeholders, in[i], got[i])
		}
	})
}

// streamSeeds returns the committed samples after the hostile comments, the reviews' counterexamples, and streams
// whose framing the rebuild must get right.
func streamSeeds(t testing.TB) []string {
	samples := filepath.Join("..", "..", "testdata")
	escaped := `{"type":"error","error":{"message":"` + esc("%6c") + key[1:] + `"}}`
	return []string{
		hostile + string(file(t, filepath.Join(samples, "messages", "tool-use"), "upstream.sse")),
		hostile + string(file(t, filepath.Join(samples, "responses", "text"), "upstream.sse")),
		hostile + string(file(t, filepath.Join(samples, "chat", "text"), "upstream.sse")),
		": stray \"\r\rdata: " + escaped + "\r\r",
		": stray \"\\\rdata: " + escaped + "\r\r",
		": stray \"\\\ndata: " + escaped + "\n\n",
		": \"input\": {\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\"}}\n\n",
		"data: {\"type\":\"a\\ndata: {}\"}\n\n",
		"data: {\"type\":\ndata: \"x\",\"item_id\":\"msg_" + esc("%31") + "\"}\n\ndata: [DONE]\n\n",
	}
}

// events reads every event's data from stream.
func events(t *testing.T, stream []byte) [][]byte {
	t.Helper()
	r := sse.NewReader(bytes.NewReader(stream), maxStream)
	var all [][]byte
	for {
		data, err := r.Next()
		if errors.Is(err, io.EOF) {
			return all
		}
		if err != nil {
			t.Fatalf("the stream does not read back: %v", err)
		}
		all = append(all, bytes.Clone(data))
	}
}

// sameEvent checks that the data of a rebuilt event, got, is want's but for the IDs.
func sameEvent(t *testing.T, ids *regexp.Regexp, placeholders map[string]string, want, got []byte) {
	t.Helper()
	if string(want) == sse.Done || string(got) == sse.Done {
		if string(got) != string(want) {
			t.Fatalf("got %q, want %q", got, want)
		}
		return
	}
	var a, b any
	if jsonx.Unmarshal(want, &a) != nil {
		return // a number beyond float64, which this comparison cannot hold; the recorder never touches numbers
	}
	if err := jsonx.Unmarshal(got, &b); err != nil {
		t.Fatal(err)
	}
	same(t, ids, placeholders, "", a, b)
}

var pointerEscape = strings.NewReplacer("~", "~0", "/", "~1")

// same checks that got is want but for nonempty strings at the ID positions, which must map one to one through
// placeholders, and that no string in got holds the key.
func same(t *testing.T, ids *regexp.Regexp, placeholders map[string]string, pointer string, want, got any) {
	t.Helper()
	switch want := want.(type) {
	case map[string]any:
		sameObject(t, ids, placeholders, pointer, want, got)
	case []any:
		got, ok := got.([]any)
		if !ok || len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", pointer, got, want)
		}
		for i := range want {
			same(t, ids, placeholders, fmt.Sprintf("%s/%d", pointer, i), want[i], got[i])
		}
	case string:
		sameString(t, ids, placeholders, pointer, want, got)
	default:
		if got != want {
			t.Fatalf("%s: got %v, want %v", pointer, got, want)
		}
	}
}

func sameObject(t *testing.T, ids *regexp.Regexp, placeholders map[string]string, pointer string, want map[string]any, got any) {
	t.Helper()
	object, ok := got.(map[string]any)
	if !ok || len(object) != len(want) {
		t.Fatalf("%s: got %v, want %v", pointer, got, want)
	}
	for name, value := range want {
		if holdsKey(name, key) {
			t.Fatalf("%s: a name holds the key", pointer)
		}
		same(t, ids, placeholders, pointer+"/"+pointerEscape.Replace(name), value, object[name])
	}
}

func sameString(t *testing.T, ids *regexp.Regexp, placeholders map[string]string, pointer, want string, got any) {
	t.Helper()
	text, ok := got.(string)
	switch {
	case !ok || holdsKey(text, key):
		t.Fatalf("%s: got %v, want a string without the key", pointer, got)
	case want == "" || !ids.MatchString(pointer):
		if text != want {
			t.Fatalf("%s: got %q, want %q unchanged", pointer, text, want)
		}
	default:
		if p, seen := placeholders[want]; seen && p != text || !seen && slices.Contains(slices.Collect(maps.Values(placeholders)), text) {
			t.Fatalf("%s: %q became %q, which is not one to one", pointer, want, text)
		}
		placeholders[want] = text
	}
}

// TestNothingWritten covers every refusal: what is refused before the call never reaches the upstream, and no
// failure leaves a sample directory behind.
func TestNothingWritten(t *testing.T) {
	const request = `{"stream":true}`
	event := func(data string) string { return "data: " + data + "\n\n" }
	start, end := event(`{"type":"message_start","message":{"content":[]}}`), event(`{"type":"message_delta"}`)+event(`{"type":"message_stop"}`)
	escapedKey := esc("%6c") + key[1:]
	later := `{"type":"later","key":"` + escapedKey + `"}`
	for _, tc := range []struct {
		name, format, scenario, request, stream string
		status                                  int
		sent                                    bool
		want                                    string
	}{
		{"unknown format", "gemini", "s", request, "", 200, false, "unknown format"},
		{"scenario outside the format's directory", "messages", "../s", request, "", 200, false, "scenario"},
		{"the key in the request", "messages", "s", `{"stream":true,"x":"` + key + `"}`, "", 200, false, "holds a key"},
		{"a key-like token in the request", "messages", "s", `{"stream":true,"x":"sk-` + strings.Repeat("a", 24) + `"}`, "", 200, false, "holds a key"},
		{"not a stream", "messages", "s", `{"stream":false}`, "", 200, false, `"stream": true`},
		{"a request with a duplicate key", "messages", "s", `{"stream":true,"stream":true}`, "", 200, false, "request"},
		{"an error status", "messages", "s", request, `{"error":{}}`, 500, true, "upstream answered 500"},
		{"an error status echoing the key", "messages", "s", request, `{"error":"bad key ` + key + `"}`, 401, true, "not shown"},
		{"an error status echoing the key escaped", "messages", "s", request, `{"error":"bad key ` + escapedKey + `"}`, 401, true, "not shown"},
		{"a stream that breaks its format", "messages", "s", request, start, 200, true, "upstream.sse"},
		{"a stream that is not SSE of JSON", "messages", "s", request, "data: {\n\n", 200, true, "upstream.sse"},
		{"the key echoed in the stream", "messages", "s", request, start + event(`{"type":"later","key":"`+key+`"}`) + end, 200, true, "upstream.sse: it holds a key"},
		{"the key escaped in the request", "messages", "s", `{"stream":true,"x":"` + escapedKey + `"}`, "", 200, false, "holds a key"},
		{"the key escaped in a member name of the request", "messages", "s", `{"stream":true,"` + escapedKey + `":1}`, "", 200, false, "holds a key"},
		{"the key escaped in a member name of the stream", "messages", "s", request, start + event(`{"type":"later","`+escapedKey+`":1}`) + end, 200, true, "upstream.sse: it holds a key"},
		{"a key-like token escaped in the stream", "messages", "s", request, start + event(`{"type":"later","key":"`+esc("%73")+`k-`+strings.Repeat("a", 24)+`"}`) + end, 200, true, "upstream.sse: it holds a key"},
		// The reviews' counterexamples: a comment that once threw a byte scanner's strings out of step.
		{"the key escaped after a comment ending in CR", "messages", "s", request, start + ": a stray \" in a comment\r" + event(later) + end, 200, true, "upstream.sse: it holds a key"},
		{"the key escaped after a comment ending in a backslash and CR", "messages", "s", request, start + ": stray \"\\\r" + event(later) + end, 200, true, "upstream.sse: it holds a key"},
		{"the key escaped after a comment ending in a backslash and LF", "messages", "s", request, start + ": stray \"\\\n" + event(later) + end, 200, true, "upstream.sse: it holds a key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, hits := serve(t, tc.format, tc.status, tc.stream)
			testdata := t.TempDir()
			err := record(t.Context(), env(url), testdata, tc.format, tc.scenario, requestFile(t, tc.request))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error saying %q", err, tc.want)
			}
			if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), escapedKey) {
				t.Errorf("the error shows the key: %v", err)
			}
			if sent := hits.Load() > 0; sent != tc.sent {
				t.Errorf("request sent: %v, want %v", sent, tc.sent)
			}
			checkEmpty(t, testdata)
		})
	}
}

// checkEmpty checks that a refused recording left nothing in testdata.
func checkEmpty(t *testing.T, testdata string) {
	t.Helper()
	if entries, err := os.ReadDir(testdata); err != nil || len(entries) > 0 {
		t.Errorf("left %v behind (%v)", entries, err)
	}
}

// TestRefusedEarly covers the refusals that need more than a request: no call is made.
func TestRefusedEarly(t *testing.T) {
	const request = `{"stream":true}`
	t.Run("an existing sample", func(t *testing.T) {
		url, hits := serve(t, "chat", 200, "")
		testdata := t.TempDir()
		if err := os.MkdirAll(filepath.Join(testdata, "chat", "s"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := record(t.Context(), env(url), testdata, "chat", "s", requestFile(t, request)); err == nil || hits.Load() > 0 {
			t.Fatalf("got %v after %d calls, want a refusal before any call", err, hits.Load())
		}
	})
	t.Run("no key", func(t *testing.T) {
		err := record(t.Context(), func(string) string { return "" }, t.TempDir(), "chat", "s", requestFile(t, request))
		if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
			t.Fatalf("got %v, want an error naming the variable", err)
		}
	})
}

// TestNoRedirect checks that a redirect fails the call instead of taking the key to another host.
func TestNoRedirect(t *testing.T) {
	other, hits := serve(t, "messages", http.StatusOK, "")
	redirect := httptest.NewServer(http.RedirectHandler(other+"/v1/messages", http.StatusTemporaryRedirect))
	t.Cleanup(redirect.Close)
	testdata := t.TempDir()
	err := record(t.Context(), env(redirect.URL), testdata, "messages", "s", requestFile(t, `{"stream":true}`))
	if err == nil || !strings.Contains(err.Error(), "307") || hits.Load() > 0 {
		t.Fatalf("got %v after %d calls to the other host, want a 307 failure and none", err, hits.Load())
	}
	checkEmpty(t, testdata)
}

// TestIdleTimeout checks that an upstream that stops sending fails the call, before the headers or in the middle of
// the stream, and that a slow stream whose gaps are each shorter than the timeout is recorded.
func TestIdleTimeout(t *testing.T) {
	defer func(d time.Duration) { idleTimeout = d }(idleTimeout)
	idleTimeout = 500 * time.Millisecond
	lines := strings.SplitAfter(string(file(t, filepath.Join("..", "..", "testdata", "chat", "text"), "upstream.sse")), "\n")
	for _, tc := range []struct {
		name  string
		parts []string
		stall bool
	}{
		{"no headers", nil, true},
		{"a stall mid-stream", lines[:1], true},
		{"a slow stream", lines, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testdata, request, done := t.TempDir(), requestFile(t, `{"stream":true}`), make(chan error, 1)
			url := trickle(t, tc.parts, tc.stall)
			go func() { done <- record(t.Context(), env(url), testdata, "chat", "s", request) }()
			select {
			case err := <-done:
				if tc.stall != errors.Is(err, errIdle) || !tc.stall && err != nil {
					t.Fatalf("got %v, want a timeout: %v", err, tc.stall)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the recorder is still waiting")
			}
		})
	}
}

// trickle serves parts 100 ms apart as a Chat upstream and, if stall, then waits until the test ends.
func trickle(t *testing.T, parts []string, stall bool) (url string) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for i, part := range parts {
			if i > 0 {
				time.Sleep(100 * time.Millisecond)
			}
			_, _ = io.WriteString(w, part) //nolint:errcheck // the recorder reports a short stream
			w.(http.Flusher).Flush()
		}
		if stall {
			<-release
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs first, so Close does not wait on the handler
	return srv.URL + "/v1"
}

func TestDotenv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	write := func(content string) {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("# comment\r\n\r\nRECORD_TEST_A = \"quoted\"\r\nRECORD_TEST_B='single'\nRECORD_TEST_C=file\n")
	t.Setenv("RECORD_TEST_C", "environment")
	getenv, err := dotenv(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"RECORD_TEST_A": "quoted", "RECORD_TEST_B": "single", "RECORD_TEST_C": "environment", "RECORD_TEST_D": ""} {
		if got := getenv(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	write("RECORD_TEST_A\n")
	if _, err := dotenv(path); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("got %v, want the line that has no =", err)
	}
	if _, err := dotenv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("a missing file: %v", err)
	}
}
