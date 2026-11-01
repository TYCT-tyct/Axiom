package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/fileparse"
	"axiom/internal/protocol"
)

// recordingParser is the node's parser in a test: it answers with one text and keeps every document it was given.
type recordingParser struct {
	docs []fileparse.Document
	text string
	err  error
}

func (p *recordingParser) Parse(_ context.Context, doc fileparse.Document) (string, error) {
	p.docs = append(p.docs, doc)
	return p.text, p.err
}

// fileBody is a Chat request that asks for the OCR engine and carries one inline PDF.
func fileBody(engine string) []byte {
	data := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 a scanned page"))
	return []byte(`{"model":"acme/alpha","plugins":[{"id":"file-parser","pdf":{"engine":"` + engine + `"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"Summarize."},` +
		`{"type":"file","file":{"filename":"scan.pdf","file_data":"data:application/pdf;base64,` + data + `"}}]}],` +
		`"stream":false}`)
}

func fileHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// parseHarness is the handler the parse step needs: the parser and the node's memory, and a call to hold them for.
func parseHarness(t *testing.T, parser fileparse.Parser) (*Handler, *Call) {
	t.Helper()
	deps := Deps{Parser: parser, Memory: protocol.NewMemory(64 << 20), Logger: slog.New(slog.DiscardHandler)}
	return &Handler{Deps: deps}, &Call{ID: uuid.NewV7()}
}

func TestParseFilesReplacesTheFile(t *testing.T) {
	parser := &recordingParser{text: "The parsed page."}
	h, c := parseHarness(t, parser)
	body := fileBody("mistral-ocr")
	req, err := protocol.Inspect(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	out, req, err := h.parseFiles(t.Context(), c, body, req)
	if err != nil {
		t.Fatal(err)
	}
	checkParse(t, parser, req, out, c)
	c.Done()
}

// checkParse holds the parse step to what one parsed file must leave behind: the backend's call, the count, the text in
// the body's file's place, and the annotation a reuse sends back.
func checkParse(t *testing.T, parser *recordingParser, req protocol.Request, out []byte, c *Call) {
	t.Helper()
	if len(parser.docs) != 1 || string(parser.docs[0].Data) != "%PDF-1.7 a scanned page" || parser.docs[0].Name != "scan.pdf" {
		t.Fatalf("the parser got %+v", parser.docs)
	}
	if req.FileParses != 1 || req.Documents != 0 {
		t.Fatalf("parses = %d, documents = %d, want 1 and 0", req.FileParses, req.Documents)
	}
	if !strings.Contains(string(out), "The parsed page.") || strings.Contains(string(out), "file_data") {
		t.Fatalf("the body does not carry the parsed text in place of the file: %s", out)
	}
	if len(c.files) != 1 || c.files[0].Hash != fileHash([]byte("%PDF-1.7 a scanned page")) || c.files[0].Text != "The parsed page." {
		t.Fatalf("annotations = %+v", c.files)
	}
}

func TestParseFilesSkipsAnAnnotatedFile(t *testing.T) {
	parser := &recordingParser{text: "should not be used"}
	h, c := parseHarness(t, parser)
	data := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 a scanned page"))
	body := []byte(`{"model":"acme/alpha","plugins":[{"id":"file-parser","pdf":{"engine":"mistral-ocr"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"Summarize."},` +
		`{"type":"file","file":{"filename":"scan.pdf","file_data":"data:application/pdf;base64,` + data + `"}}]},` +
		`{"role":"assistant","content":"ok","annotations":[{"type":"file","file":{"hash":"` +
		fileHash([]byte("%PDF-1.7 a scanned page")) + `","name":"scan.pdf","content":[{"type":"text","text":"The text from last time."}]}}]}],` +
		`"stream":false}`)
	req, err := protocol.Inspect(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	out, req, err := h.parseFiles(t.Context(), c, body, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(parser.docs) != 0 {
		t.Fatalf("the annotated file was parsed again: %+v", parser.docs)
	}
	if req.FileParses != 0 || !strings.Contains(string(out), "The text from last time.") {
		t.Fatalf("parses = %d, body = %s", req.FileParses, out)
	}
	if len(c.files) != 0 {
		t.Fatalf("a replayed file was annotated again: %+v", c.files)
	}
	c.Done()
}

// TestParseFilesNativeSendsTheFile: the native engine leaves the file to the endpoint, and a node without a parser
// serves it.
func TestParseFilesNativeSendsTheFile(t *testing.T) {
	body := fileBody("native")
	req, err := protocol.Inspect(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	h, c := parseHarness(t, nil)
	out, req, err := h.parseFiles(t.Context(), c, body, req)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) || req.FileParses != 0 || req.Documents != 1 {
		t.Fatalf("parses = %d, documents = %d", req.FileParses, req.Documents)
	}
	c.Done()
}

func TestParseFilesRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parser fileparse.Parser
		engine string
		status int
		want   string
	}{
		{name: "no backend", engine: "mistral-ocr", status: http.StatusBadRequest, want: protocol.NoFileParser},
		{
			name: "backend failed", parser: &recordingParser{err: fileparse.ErrUpstream}, engine: "cloudflare-ai",
			status: http.StatusBadGateway, want: "could not parse",
		},
		{
			name: "text too large", parser: &recordingParser{err: fileparse.ErrTooLarge}, engine: "cloudflare-ai",
			status: http.StatusRequestEntityTooLarge, want: "larger than",
		},
		{
			name: "empty text", parser: &recordingParser{err: fileparse.ErrEmpty}, engine: "cloudflare-ai",
			status: http.StatusBadRequest, want: "could not read",
		},
		{
			name: "a file that is not base64", parser: &recordingParser{text: "unused"}, engine: "cloudflare-ai",
			status: http.StatusBadRequest, want: "could not read",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := parseHarness(t, tc.parser)
			body := fileBody(tc.engine)
			if tc.name == "a file that is not base64" {
				body = []byte(`{"model":"acme/alpha","plugins":[{"id":"file-parser","pdf":{"engine":"cloudflare-ai"}}],` +
					`"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"!!"}}]}]}`)
			}
			req, err := protocol.Inspect(protocol.Chat, body)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = h.parseFiles(t.Context(), c, body, req)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Status != tc.status || !strings.Contains(refusalMessage(refusal), tc.want) {
				t.Fatalf("error = %v, want %d with %q", err, tc.status, tc.want)
			}
			c.Done()
		})
	}
}

// TestParsesAreChargedAsSearches: each parse is one per-use unit of the search item, charged on the attempt that
// answered, like the web plugin's search.
func TestParsesAreChargedAsSearches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstream := protocol.Chat
		h := newHarness(t, t.Context(), options{
			client: protocol.Chat, upstream: &upstream, fileParses: 2,
			request: []byte(`{"model":"acme/alpha","messages":[{"role":"user","content":"hello"}],"stream":false}`),
			price:   `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
			script:  map[int][]scripted{1: {serve(beat{data: chatCited})}},
		})
		h.run()
		usage := h.settlements[0].outcome.Attempts[0].Segments[0].Usage
		if got := usage.Count(catalog.ItemWebSearch); got != 2 {
			t.Fatalf("settled parses = %d, want 2", got)
		}
	})
}

// TestParseFilesRefusesTooManyFiles: the cap is checked before the backend is asked to convert the ninth file.
func TestParseFilesRefusesTooManyFiles(t *testing.T) {
	parser := &recordingParser{text: "unused"}
	h, c := parseHarness(t, parser)
	var parts []string
	for i := range protocol.MaxFilesPerRequest + 1 {
		data := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 file " + strconv.Itoa(i)))
		parts = append(parts, `{"type":"file","file":{"filename":"scan.pdf","file_data":"data:application/pdf;base64,`+data+`"}}`)
	}
	body := []byte(`{"model":"acme/alpha","plugins":[{"id":"file-parser","pdf":{"engine":"mistral-ocr"}}],` +
		`"messages":[{"role":"user","content":[` + strings.Join(parts, ",") + `]}],"stream":false}`)
	req, err := protocol.Inspect(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = h.parseFiles(t.Context(), c, body, req)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("error = %v, want 413", err)
	}
	if len(parser.docs) != protocol.MaxFilesPerRequest {
		t.Fatalf("the parser converted %d files, want %d", len(parser.docs), protocol.MaxFilesPerRequest)
	}
	c.Done()
}

// TestFileParserUnconfiguredThroughTheHandler: the OCR engine keeps the documented 400 while no backend is configured,
// and nothing is reserved.
func TestFileParserUnconfiguredThroughTheHandler(t *testing.T) {
	r := newRig(t)
	a := r.newAccount(1e9)
	resp, body := r.do(http.MethodPost, "/api/v1/chat/completions", a.key, bytes.NewReader(fileBody("mistral-ocr")))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, protocol.NoFileParser) {
		t.Fatalf("%d %s, want a 400 saying %q", resp.StatusCode, body, protocol.NoFileParser)
	}
	if len(r.calls()) != 0 || r.count("SELECT count(*) FROM billing.reservations") != 0 {
		t.Fatal("the refusal was admitted")
	}
	r.idle(1 << 30)
}

// refusalMessage is what a client reads for the refusal: its own wording or the code's.
func refusalMessage(r *Refusal) string {
	if r.Message != "" {
		return r.Message
	}
	return messages[r.Code]
}

// replyFileAnnotations reads the file annotations of the reply's first choice.
