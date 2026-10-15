package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/protocol/internal/streamcheck"
)

const key = "local-test-key"

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

func file(t *testing.T, dir, name string) []byte {
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

// TestRecord records the committed samples from a local upstream: the files keep every byte but the IDs, IDs map to
// the same placeholder across both files, and the result passes the samples test. The requests hold what must stay:
// names and text that look like IDs, a short ID next to a long one, escaped quotes, and a stray quote in a comment.
func TestRecord(t *testing.T) {
	samples := filepath.Join("..", "..", "testdata")
	toolID, callID := "toolu_01T1x1fJ34qAmk2tNTrN7Up6", "call_"+strings.Repeat("A1", 12)
	opaque := `"gAAsk-` + strings.Repeat("a", 24) + `"` // "sk-" inside base64url, as in encrypted reasoning
	for _, tc := range []struct {
		format, sample, request, ids string // ids: original and placeholder pairs, for both files
		crlf                         bool
	}{
		{
			"messages", "tool-use", `{"stream":true,"tools":[{"name":"call_weather","input_schema":{"properties":{"msg_text":{}},` +
				`"required":["city_id","msg_text"]}},{"name":"call_get_weather_forecast"}],"messages":[{"role":"user","content":[` +
				`{"type":"text","text":"\" mention \"msg_abcdefghijklmnopqrstuvwx\""},{"type":"tool_result","tool_use_id":"` + toolID + `"}]}]}`,
			toolID + " toolu_1 msg_014p7gG3wDgGV9EUtLvnow3U msg_1", false,
		},
		{
			"responses", "text", `{"stream":true,"metadata":{"blob":` + opaque + `,"user_id":"user_msg_1"},"input":[{"type":"function_call_output","call_id":"call_1",` +
				`"output":""},{"type":"function_call_output","call_id":"` + callID + `","output":""}]}`,
			"call_1 call_1 " + callID + " call_2 resp_67c9fdcecf488190bdd9a0409de3a1ec07b8b0ad4e5eb654 resp_1 " +
				"msg_67c9fdcf37fc8190ba82116e33fb28c507b8b0ad4e5eb654 msg_1", false,
		},
		{"chat", "text", `{"stream":true,"messages":[]}`, "chatcmpl-123 chatcmpl-1", true},
	} {
		t.Run(tc.format, func(t *testing.T) {
			stream := `: a stray " in a comment` + "\n\n" + string(file(t, filepath.Join(samples, tc.format, tc.sample), "upstream.sse"))
			if tc.crlf {
				stream = strings.ReplaceAll(stream, "\n", "\r\n")
			}
			url, _ := serve(t, tc.format, http.StatusOK, stream)
			testdata := t.TempDir()
			if err := record(t.Context(), env(url), testdata, tc.format, "recorded", requestFile(t, tc.request)); err != nil {
				t.Fatal(err)
			}
			ids := strings.NewReplacer(strings.Fields(tc.ids)...)
			checkRecorded(t, filepath.Join(testdata, tc.format, "recorded"), tc.format, ids.Replace(tc.request), ids.Replace(stream))
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
	if !bytes.Contains(meta, []byte("IDs the upstream generated are replaced")) {
		t.Errorf("meta.json does not say the IDs are replaced:\n%s", meta)
	}
	if err := streamcheck.Sample(formats[format].check, file(t, dir, "request.json"), file(t, dir, "upstream.sse"), meta); err != nil {
		t.Error(err)
	}
}

// TestReplace runs a request and then a stream through one table, so the same ID gets the same placeholder however
// it is escaped and in whichever file it appears.
func TestReplace(t *testing.T) {
	ids := placeholders{map[string][]byte{}, map[string]int{}}
	for _, tc := range []struct{ name, data, want string }{
		{"an ID", `{"call_id":"call_1"}`, `{"call_id":"call_1"}`},
		{"the same ID and a new one, escaped", `{"call\u005fid":"call\u005f1","id":"\u0063all_x"}`, `{"call\u005fid":"call_1","id":"call_2"}`},
		{
			"a tool's input", `{"type":"tool_use","input":{"q":"}","id":"call_customer","n":{"x_id":"msg_x"}},"id":"toolu_abc"}`,
			`{"type":"tool_use","input":{"q":"}","id":"call_customer","n":{"x_id":"msg_x"}},"id":"toolu_1"}`,
		},
		{
			"metadata, and input that holds items", `{"metadata":{"user_id":"msg_user"},"input":[{"call_id":"call_y"}]}`,
			`{"metadata":{"user_id":"msg_user"},"input":[{"call_id":"call_3"}]}`,
		},
		{"a prefix inside the value", `{"user_id":"user_msg_1"}`, `{"user_id":"user_msg_1"}`},
		{"a string that names no member", `{"tags":["user_id","msg_x"]}`, `{"tags":["user_id","msg_x"]}`},
		{"an ID member that is not a string", `{"previous_response_id":null,"call_x":0}`, `{"previous_response_id":null,"call_x":0}`},
	} {
		if got := ids.replace([]byte(tc.data)); string(got) != tc.want {
			t.Errorf("%s: got\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}
}

// TestNothingWritten covers every refusal: what is refused before the call never reaches the upstream, and no
// failure leaves a sample directory behind.
func TestNothingWritten(t *testing.T) {
	const request = `{"stream":true}`
	event := func(data string) string { return "data: " + data + "\n\n" }
	start, end := event(`{"type":"message_start","message":{"content":[]}}`), event(`{"type":"message_delta"}`)+event(`{"type":"message_stop"}`)
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
		{"a stream that breaks its format", "messages", "s", request, start, 200, true, "upstream.sse"},
		{"the key echoed in the stream", "messages", "s", request, start + event(`{"type":"later","key":"`+key+`"}`) + end, 200, true, "upstream.sse holds a key"},
		{"the key escaped in the request", "messages", "s", `{"stream":true,"x":"\u006c` + key[1:] + `"}`, "", 200, false, "holds a key"},
		{"a key-like token escaped in the stream", "messages", "s", request, start + event(`{"type":"later","key":"\u0073k-`+strings.Repeat("a", 24)+`"}`) + end, 200, true, "upstream.sse holds a key"},
		// A CR ends the comment line as LF does, so its stray quote cannot pair with the quotes of the event after it.
		{"the key escaped after a comment ending in CR", "messages", "s", request, start + `: a stray " in a comment` + "\r" + event(`{"type":"later","key":"\u006c`+key[1:]+`"}`) + end, 200, true, "upstream.sse holds a key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, hits := serve(t, tc.format, tc.status, tc.stream)
			testdata := t.TempDir()
			err := record(t.Context(), env(url), testdata, tc.format, tc.scenario, requestFile(t, tc.request))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error saying %q", err, tc.want)
			}
			if strings.Contains(err.Error(), key) {
				t.Errorf("the error shows the key: %v", err)
			}
			if sent := hits.Load() > 0; sent != tc.sent {
				t.Errorf("request sent: %v, want %v", sent, tc.sent)
			}
			if entries, err := os.ReadDir(testdata); err != nil || len(entries) > 0 {
				t.Errorf("left %v behind (%v)", entries, err)
			}
		})
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
	if entries, err := os.ReadDir(testdata); err != nil || len(entries) > 0 {
		t.Errorf("left %v behind (%v)", entries, err)
	}
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
