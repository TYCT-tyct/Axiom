// Command record sends a request to a live upstream and saves the stream it answers with as a protocol sample, laid out
// as internal/protocol/README.md「录制样本」 describes. Nothing is written unless the whole sample passes the same
// check as the samples test; headers are never kept, upstream-generated IDs are replaced by placeholders, and a
// request or response that holds the key, or anything that looks like one, is refused.
//
// It lives in the protocol module, not in tools/, because it checks streams with protocol's internal streamcheck,
// which Go lets only packages under internal/protocol import.
//
// usage: go run ./internal/protocol/internal/record <testdata dir> <format> <scenario> <request file>
package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"axiom/internal/platform/jsonx"
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
}

var formats = map[string]upstream{
	"messages": {"/v1/messages", "/v1/messages", "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", func(h http.Header, key string) {
		h.Set("X-Api-Key", key)
		h.Set("Anthropic-Version", "2023-06-01")
	}, streamcheck.Messages},
	"responses": {"/responses", "/v1/responses", "OPENAI_BASE_URL", "OPENAI_API_KEY", bearer, streamcheck.Responses},
	"chat":      {"/chat/completions", "/v1/chat/completions", "OPENAI_BASE_URL", "OPENAI_API_KEY", bearer, streamcheck.Chat},
}

func bearer(h http.Header, key string) { h.Set("Authorization", "Bearer "+key) }

var (
	scenarioName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// keyLike matches the keys of both providers, sk-ant-… and sk-…, where a token starts: inside a run of base64url,
	// such as an encrypted reasoning item, "sk-" is chance, not a key.
	keyLike = regexp.MustCompile(`(^|[^A-Za-z0-9_-])sk-[A-Za-z0-9_-]{20,}`)
	// upstreamID matches the start of an ID an upstream generates, by the prefixes the three formats' references use.
	upstreamID = regexp.MustCompile(`^(chatcmpl-|msg_|toolu_|srvtoolu_|req_|resp_|rs_|fc_|call_|ws_)`)
)

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
	if hasKey(request, key) {
		return errors.New("request: it holds a key, so it is not sent")
	}
	return nil
}

// hasKey looks at the raw bytes and at each JSON string decoded, since an escape such as \u0073k- hides a key from
// the bytes but not from whoever decodes the file.
func hasKey(data []byte, key string) bool {
	if holdsKey(data, key) {
		return true
	}
	for _, s := range jsonStrings(data) {
		var text string
		// A quote on a line that is not JSON, such as an SSE comment, may delimit no JSON string; the raw scan covers it.
		if jsonx.Unmarshal(data[s.start:s.end], &text) == nil && holdsKey([]byte(text), key) {
			return true
		}
	}
	return false
}

func holdsKey(data []byte, key string) bool {
	return bytes.Contains(data, []byte(key)) || keyLike.Match(data)
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
	case resp.StatusCode != http.StatusOK && hasKey(stream, key):
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

// sample returns the three files with IDs replaced, after the same check the samples test makes.
func sample(up upstream, key string, request, stream []byte) (map[string][]byte, error) {
	ids := placeholders{map[string][]byte{}, map[string]int{}}
	request, stream = ids.replace(request), ids.replace(stream)
	note := ""
	if len(ids.ids) > 0 {
		note = ",\n  \"note\": \"IDs the upstream generated are replaced in both files by their prefix and a number in order of appearance.\""
	}
	// Every value is ASCII without quotes or backslashes, where %q quotes as JSON does.
	meta := fmt.Appendf(nil, "{\n  \"source\": %q,\n  \"date\": %q%s\n}\n",
		"recorded from a live upstream: POST "+up.endpoint, time.Now().Format(time.DateOnly), note)
	files := map[string][]byte{"request.json": request, "upstream.sse": stream, "meta.json": meta}
	for name, data := range files {
		if hasKey(data, key) {
			return nil, fmt.Errorf("%s holds a key, so nothing is written", name)
		}
	}
	if err := streamcheck.Sample(up.check, request, stream, meta); err != nil {
		return nil, err
	}
	return files, nil
}

// placeholders maps each upstream ID to its placeholder. Every ID is replaced, including one that already looks like
// a placeholder, and a counter per prefix numbers them, so distinct IDs never share a placeholder.
type placeholders struct {
	ids   map[string][]byte
	count map[string]int
}

// replace replaces each ID in data: a string with an upstream ID's prefix that is the value of a member named id or
// ending in _id, the names under which the three formats carry IDs. Requiring the name leaves alone a tool or a schema
// key that merely starts like an ID, and text that quotes one.
func (p placeholders) replace(data []byte) []byte {
	var out []byte
	last := 0
	strs := jsonStrings(data)
	for i, s := range strs {
		id := data[s.start+1 : s.end-1]
		if i == 0 || !isIDMember(data, strs[i-1], s) || !upstreamID.Match(id) {
			continue
		}
		placeholder, ok := p.ids[string(id)]
		if !ok {
			prefix := string(upstreamID.FindSubmatch(id)[1])
			p.count[prefix]++
			placeholder = fmt.Appendf(nil, `"%s%d"`, prefix, p.count[prefix])
			p.ids[string(id)] = placeholder
		}
		out = append(append(out, data[last:s.start]...), placeholder...)
		last = s.end
	}
	return append(out, data[last:]...)
}

// isIDMember reports whether value is the value of the member name, and name is id or ends in _id.
func isIDMember(data []byte, name, value span) bool {
	n := string(data[name.start+1 : name.end-1])
	return string(bytes.TrimSpace(data[name.end:value.start])) == ":" && (n == "id" || strings.HasSuffix(n, "_id"))
}

// span is where a JSON string lies in a file, its quotes included.
type span struct{ start, end int }

// jsonStrings returns the JSON strings in data in order, honoring escapes. A JSON string cannot hold a line break, so
// a line break ends whatever a stray quote outside JSON, as in an SSE comment, opened.
func jsonStrings(data []byte) []span {
	var spans []span
	start := -1
	for i := 0; i < len(data); i++ {
		switch c := data[i]; {
		case c == '\n':
			start = -1
		case start < 0:
			if c == '"' {
				start = i
			}
		case c == '\\':
			i++
		case c == '"':
			spans = append(spans, span{start, i + 1})
			start = -1
		}
	}
	return spans
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
