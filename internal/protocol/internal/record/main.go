// Command record sends a request to a live upstream and saves the stream it answers with as a protocol sample, laid out
// as internal/protocol/README.md「录制样本」 describes. Nothing is written unless the whole sample passes the same
// check as the samples test; headers are never kept, the stream is rebuilt from its events, upstream-generated IDs
// are replaced by placeholders, and a request or response that holds the key, or anything that looks like one, is
// refused.
//
// It lives in the protocol module, not in tools/, because it reads streams with protocol's internal sse and
// streamcheck, which Go lets only packages under internal/protocol import.
//
// usage: go run ./internal/protocol/internal/record <testdata dir> <format> <scenario> <request file>
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext" //nolint:depguard // only to find each string's offsets, in values jsonx.Validate has accepted
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
	"axiom/internal/protocol/internal/streamcheck"
)

// maxStream bounds a recorded stream: samples are committed, and the largest one planned, a 16 MiB output whose
// Responses snapshots repeat it, stays well below this.
const maxStream = 64 << 20

// upstream is how one format is called. Base URLs follow each provider's SDK: ANTHROPIC_BASE_URL is the host,
// OPENAI_BASE_URL already ends in /v1, so the variables a developer has set for the SDKs work unchanged.
type upstream struct {
	path, endpoint  string // path is appended to the base URL; endpoint is the documented one, named in meta.json
	baseVar, keyVar string
	auth            func(h http.Header, key string)
	check           func(io.Reader) error
	events          bool           // the format names each event in an event line, which restates its type
	ids             *regexp.Regexp // the JSON Pointers, in the request or in an event, of the IDs the upstream generates
}

// The ID positions follow each format's documented objects, for the request and the stream at once since their
// pointers never meet: Messages' message, content blocks (tool_use and server tool IDs, tool_use_id of results) and
// error request_id; Responses' response, its output items and the item events (id, call_id, item_id,
// previous_response_id), and the request's input items; Chat's chunk, its tool calls, and the request's tool calls and
// tool_call_id. Only these positions change, so the caller's data, such as a tool argument named id, never does.
var formats = map[string]upstream{
	"messages": {
		path: "/v1/messages", endpoint: "/v1/messages", baseVar: "ANTHROPIC_BASE_URL", keyVar: "ANTHROPIC_API_KEY",
		auth: func(h http.Header, key string) {
			h.Set("X-Api-Key", key)
			h.Set("Anthropic-Version", "2023-06-01")
		},
		check: streamcheck.Messages, events: true,
		ids: positions("/message/id", "/content_block/id", "/content_block/tool_use_id", "/request_id",
			"/messages/*/content/*/id", "/messages/*/content/*/tool_use_id"),
	},
	"responses": {
		path: "/responses", endpoint: "/v1/responses", baseVar: "OPENAI_BASE_URL", keyVar: "OPENAI_API_KEY",
		auth: bearer, check: streamcheck.Responses, events: true,
		ids: positions("/response/id", "/response/previous_response_id", "/response/output/*/id", "/response/output/*/call_id",
			"/item/id", "/item/call_id", "/item_id", "/previous_response_id", "/input/*/id", "/input/*/call_id"),
	},
	"chat": {
		path: "/chat/completions", endpoint: "/v1/chat/completions", baseVar: "OPENAI_BASE_URL", keyVar: "OPENAI_API_KEY",
		auth: bearer, check: streamcheck.Chat,
		ids: positions("/id", "/choices/*/delta/tool_calls/*/id", "/messages/*/tool_calls/*/id", "/messages/*/tool_call_id"),
	},
}

func bearer(h http.Header, key string) { h.Set("Authorization", "Bearer "+key) }

// positions compiles JSON Pointers, where * stands for any array index, into one pattern.
func positions(pointers ...string) *regexp.Regexp {
	for i, p := range pointers {
		pointers[i] = strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, `\d+`)
	}
	return regexp.MustCompile(`^(?:` + strings.Join(pointers, "|") + `)$`)
}

var (
	scenarioName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// keyLike matches the keys of both providers, sk-ant-… and sk-…, where a token starts: inside a run of base64url,
	// such as an encrypted reasoning item, "sk-" is chance, not a key.
	keyLike = regexp.MustCompile(`(^|[^A-Za-z0-9_-])sk-[A-Za-z0-9_-]{20,}`)
	// idPrefix is the part of an ID that names its kind, such as msg_ or chatcmpl-, which its placeholder keeps.
	idPrefix = regexp.MustCompile(`^[A-Za-z]+[_-]`)
)

var errKey = errors.New("it holds a key, so nothing is written")

// client never follows a redirect: Go drops only Authorization and cookies when one leaves the host, so X-Api-Key
// would reach wherever the upstream points. A redirect fails as a status that is not 200.
var client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// idleTimeout bounds each wait for the upstream: the connection, the headers and every read of the stream. It waits
// for the next bytes, not the whole stream, and is long because a reasoning model may send nothing while it thinks.
// It is a variable so that tests can shorten it.
var idleTimeout = 10 * time.Minute

var errIdle = errors.New("the upstream sent nothing for too long")

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: record <testdata dir> <format> <scenario> <request file>")
		os.Exit(2)
	}
	getenv, err := dotenv(".env")
	if err == nil {
		err = record(context.Background(), getenv, os.Args[1], os.Args[2], os.Args[3], os.Args[4])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "record:", err)
		os.Exit(1)
	}
}

// record writes testdata/<format>/<scenario>/ from one call, or nothing.
func record(ctx context.Context, getenv func(string) string, testdata, format, scenario, requestFile string) (err error) {
	up, ok := formats[format]
	if !ok {
		return fmt.Errorf("unknown format %q: want messages, responses or chat", format)
	}
	if !scenarioName.MatchString(scenario) {
		return fmt.Errorf("scenario %q: want lowercase letters and digits joined by hyphens", scenario)
	}
	root, err := os.OpenRoot(testdata)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	dir := path.Join(format, scenario)
	if _, err := root.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w: a sample is never overwritten", dir, cmp.Or(err, fs.ErrExist))
	}
	base, key := getenv(up.baseVar), getenv(up.keyVar)
	if base == "" || key == "" {
		return fmt.Errorf("set %s and %s in the environment or in .env", up.baseVar, up.keyVar)
	}
	request, err := os.ReadFile(requestFile) //nolint:gosec // the developer names the file to send
	if err != nil {
		return err
	}
	if err := checkRequest(request, key); err != nil {
		return err
	}
	stream, err := send(ctx, up, base, key, request)
	if err != nil {
		return err
	}
	files, err := sample(up, key, request, stream)
	if err != nil {
		return err
	}
	return write(root, dir, files)
}

func checkRequest(request []byte, key string) error {
	var r struct {
		Stream bool `json:"stream"`
	}
	if err := jsonx.Unmarshal(request, &r); err != nil {
		return fmt.Errorf("request: %w", err)
	}
	if !r.Stream {
		return errors.New(`request: a sample is a stream, so the request needs "stream": true`)
	}
	if leaks(request, key) {
		return errors.New("request: it holds a key, so it is not sent")
	}
	return nil
}

// leaks reports whether data, which need not be JSON, holds a key in its bytes or, when it is JSON, in any string
// once decoded: an escaped character hides a key from the bytes but not from whoever decodes them.
func leaks(data []byte, key string) bool {
	if holdsKey(string(data), key) {
		return true
	}
	strs, err := jsonStrings(data)
	if err != nil {
		return false // not JSON, so nothing decodes it: the bytes were checked
	}
	return slices.ContainsFunc(strs, func(s jsonString) bool { return holdsKey(s.text, key) })
}

func holdsKey(text, key string) bool {
	return strings.Contains(text, key) || keyLike.MatchString(text)
}

// send posts the request exactly as the file holds it and returns the stream's bytes as they arrived.
func send(ctx context.Context, up upstream, base, key string, request []byte) ([]byte, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	idle := time.AfterFunc(idleTimeout, func() { cancel(fmt.Errorf("%w (%v)", errIdle, idleTimeout)) })
	defer idle.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+up.path, bytes.NewReader(request))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	up.auth(req.Header, key)
	resp, err := client.Do(req) //nolint:gosec // the developer chooses the upstream
	if err != nil {
		return nil, err
	}
	stream, err := io.ReadAll(io.LimitReader(idleReader{resp.Body, idle}, maxStream+1))
	switch err = errors.Join(err, resp.Body.Close()); {
	case err != nil:
		return nil, fmt.Errorf("reading the stream: %w", err)
	case resp.StatusCode != http.StatusOK && leaks(stream, key):
		return nil, fmt.Errorf("upstream answered %s with a body that holds a key, so the body is not shown", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("upstream answered %s: %.1000s", resp.Status, stream)
	case len(stream) > maxStream:
		return nil, fmt.Errorf("the stream is over %d bytes, too large to commit", maxStream)
	}
	return stream, nil
}

// idleReader restarts the idle timer whenever bytes arrive.
type idleReader struct {
	r    io.Reader
	idle *time.Timer
}

func (r idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.idle.Reset(idleTimeout)
	}
	return n, err
}

// sample returns the three files, after the same check the samples test makes: the request and the stream with IDs
// replaced, the stream rebuilt from its events, and meta.json.
func sample(up upstream, key string, request, stream []byte) (map[string][]byte, error) {
	s := scrubber{key, map[string]string{}, map[string]int{}}
	request, _, err := s.json(request, up.ids)
	if err != nil {
		return nil, fmt.Errorf("request.json: %w", err)
	}
	if stream, err = s.stream(up, stream); err != nil {
		return nil, fmt.Errorf("upstream.sse: %w", err)
	}
	// Every value is ASCII without quotes or backslashes, where %q quotes as JSON does.
	meta := fmt.Appendf(nil, "{\n  \"source\": %q,\n  \"date\": %q,\n  \"note\": %q\n}\n",
		"recorded from a live upstream: POST "+up.endpoint, time.Now().Format(time.DateOnly), recordedNote)
	files := map[string][]byte{"request.json": request, "upstream.sse": stream, "meta.json": meta}
	for name, data := range files {
		if bytes.Contains(data, []byte(key)) {
			return nil, fmt.Errorf("%s: %w", name, errKey)
		}
	}
	if err := streamcheck.Sample(up.check, request, stream, meta); err != nil {
		return nil, err
	}
	return files, nil
}

const recordedNote = "Rebuilt from the upstream's events: comments and the id and retry fields are left out, an event line, " +
	"where the format has one, restates the event's type, and IDs the upstream generated are replaced in both files " +
	"by their prefix and a number in order of appearance."

// scrubber replaces the IDs of one recording through one table, so an ID gets the same placeholder in the request and
// in the stream however it is escaped, and refuses JSON that holds a key once decoded. Every ID is replaced, including
// one that already looks like a placeholder, and a counter per prefix numbers them, so distinct IDs never share one.
type scrubber struct {
	key   string
	ids   map[string]string // decoded ID to placeholder, quotes included
	count map[string]int
}

// stream returns the stream's events, each written the same way, with IDs replaced: an event line where the format
// has one, a data line per line of data, and a blank line. Comments, the id and retry fields, a byte order mark and
// the line endings the upstream chose are not kept: they are SSE framing, which no reader of a sample looks at.
func (s scrubber) stream(up upstream, stream []byte) ([]byte, error) {
	events := sse.NewReader(bytes.NewReader(stream), maxStream)
	var out []byte
	for {
		data, err := events.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if out, err = s.event(out, up, data); err != nil {
			return nil, err
		}
	}
}

// event appends to out the event whose data is data, with IDs replaced.
func (s scrubber) event(out []byte, up upstream, data []byte) ([]byte, error) {
	var typ string
	if string(data) != sse.Done {
		var err error
		if data, typ, err = s.json(data, up.ids); err != nil {
			return nil, err
		}
	}
	if !up.events {
		typ = ""
	}
	return sse.AppendEvent(out, typ, data), nil
}

// json returns the JSON value data with the string at each ID position replaced by its placeholder, every other byte
// kept, and the value's top-level type member; it fails if any string, a member name or a value, holds a key.
func (s scrubber) json(data []byte, ids *regexp.Regexp) (out []byte, typ string, err error) {
	strs, err := jsonStrings(data)
	if err != nil {
		return nil, "", err
	}
	last := 0
	for _, str := range strs {
		switch {
		case holdsKey(str.text, s.key):
			return nil, "", errKey
		case str.name || str.text == "":
		case str.pointer == "/type":
			typ = str.text
		case ids.MatchString(str.pointer):
			out = append(append(out, data[last:str.start]...), s.placeholder(str.text)...)
			last = str.end
		}
	}
	return append(out, data[last:]...), typ, nil
}

func (s scrubber) placeholder(id string) string {
	p, ok := s.ids[id]
	if !ok {
		prefix := cmp.Or(idPrefix.FindString(id), "id_")
		s.count[prefix]++
		p = fmt.Sprintf(`"%s%d"`, prefix, s.count[prefix])
		s.ids[id] = p
	}
	return p
}

// jsonString is a string in a JSON value, a member name or a value: its JSON Pointer, where it lies, quotes
// included, and its decoded text.
type jsonString struct {
	pointer    string
	name       bool
	start, end int
	text       string
}

// jsonStrings returns the strings in the JSON value data in order, once jsonx.Validate accepts data.
func jsonStrings(data []byte) ([]jsonString, error) {
	if err := jsonx.Validate(data); err != nil {
		return nil, err
	}
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	var strs []jsonString
	for {
		if dec.PeekKind() != '"' {
			if _, err := dec.ReadToken(); errors.Is(err, io.EOF) {
				return strs, nil
			} else if err != nil {
				return nil, err
			}
			continue
		}
		kind, n := dec.StackIndex(dec.StackDepth()) // in an object, an even count of tokens read means a name is next
		raw, err := dec.ReadValue()
		if err != nil {
			return nil, err
		}
		text, err := jsontext.AppendUnquote(nil, raw)
		if err != nil {
			return nil, err
		}
		end := int(dec.InputOffset())
		strs = append(strs, jsonString{string(dec.StackPointer()), kind == '{' && n%2 == 0, end - len(raw), end, string(text)})
	}
}

// write creates dir under root, failing if it exists, with the files; if a write fails, it removes dir again.
func write(root *os.Root, dir string, files map[string][]byte) error {
	if err := root.MkdirAll(path.Dir(dir), 0o750); err != nil {
		return err
	}
	if err := root.Mkdir(dir, 0o750); err != nil {
		return err
	}
	for name, data := range files {
		if err := root.WriteFile(path.Join(dir, name), data, 0o600); err != nil {
			return errors.Join(err, root.RemoveAll(dir))
		}
	}
	return nil
}

// dotenv returns a lookup that reads the environment first and then file, made of NAME=value lines where blank lines
// and lines starting with # are skipped and one pair of quotes around a value is removed. A missing file is an empty
// one.
func dotenv(file string) (func(string) string, error) {
	data, err := os.ReadFile(file) //nolint:gosec // a fixed name in the working directory
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	values := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") { // TrimSpace below also drops the CR of a CRLF line
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s line %d: want NAME=value", file, i+1)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		values[strings.TrimSpace(name)] = value
	}
	return func(name string) string {
		if value := os.Getenv(name); value != "" {
			return value
		}
		return values[name]
	}, nil
}
