package gateway

import (
	"bytes"
	"cmp"
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
	docs       []fileparse.Document
	text       string
	pages      int
	countPages bool
	err        error
}

func (p *recordingParser) Parse(_ context.Context, doc fileparse.Document, countPages bool) (string, int, error) {
	p.docs = append(p.docs, doc)
	p.countPages = countPages
	return p.text, cmp.Or(p.pages, 1), p.err
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
	// Eight PDF parses each hold the 8 MiB response cap at once, so the harness needs room for all of them.
	deps := Deps{Parser: parser, Memory: protocol.NewMemory(128 << 20), Logger: slog.New(slog.DiscardHandler)}
	return &Handler{Deps: deps}, &Call{ID: uuid.NewV7()}
}

// checkParse holds the parse step to what one parsed file must leave behind: the backend's call, the count, the text in
// the body's file's place, and the annotation a reuse sends back.
func checkParse(t *testing.T, parser *recordingParser, req protocol.Request, out []byte, c *Call) {
	t.Helper()
	if len(parser.docs) != 1 || string(parser.docs[0].Data) != "%PDF-1.7 a scanned page" || parser.docs[0].Name != "scan.pdf" {
		t.Fatalf("the parser got %+v", parser.docs)
	}
	if !parser.countPages {
		t.Fatal("the parser was not asked for a page count")
	}
	if req.FileParses != 1 || req.FilePages != 1 || !req.FileBilling || req.Documents != 0 {
		t.Fatalf("parses = %d, pages = %d, billing = %t, documents = %d, want 1, 1, true and 0",
			req.FileParses, req.FilePages, req.FileBilling, req.Documents)
	}
	checkParsedBody(t, out, c)
}

// checkParsedBody holds the replaced body and its annotation to the parsed text.
func checkParsedBody(t *testing.T, out []byte, c *Call) {
	t.Helper()
	if !strings.Contains(string(out), "The parsed page.") || strings.Contains(string(out), "file_data") {
		t.Fatalf("the body does not carry the parsed text in place of the file: %s", out)
	}
	if len(c.files) != 1 || c.files[0].Hash != fileHash([]byte("%PDF-1.7 a scanned page")) || c.files[0].Text != "The parsed page." {
		t.Fatalf("annotations = %+v", c.files)
	}
}

func TestParseFilesSkipsAnAnnotatedFile(t *testing.T) {
	t.Parallel()
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
	out, _, err := h.parseFiles(t.Context(), c, body, req, true)
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
	t.Parallel()
	body := fileBody("native")
	req, err := protocol.Inspect(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	h, c := parseHarness(t, nil)
	out, _, err := h.parseFiles(t.Context(), c, body, req, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) || req.FileParses != 0 || req.Documents != 1 {
		t.Fatalf("parses = %d, documents = %d", req.FileParses, req.Documents)
	}
	c.Done()
}

func TestParseFilesRefusals(t *testing.T) {
	t.Parallel()
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
			status: http.StatusBadGateway, want: "could not parse",
		},
		{
			name: "an empty file", parser: &recordingParser{err: fileparse.ErrEmptyFile}, engine: "cloudflare-ai",
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
			_, _, err = h.parseFiles(t.Context(), c, body, req, true)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Status != tc.status || !strings.Contains(refusalMessage(refusal), tc.want) {
				t.Fatalf("error = %v, want %d with %q", err, tc.status, tc.want)
			}
			c.Done()
		})
	}
}

// TestParsesAreChargedAsFileParses: each page is one per-use unit of its own item when the switch is on, charged on the
// attempt that answered, like the web plugin's search, and not folded into the search item.
func TestParsesAreChargedAsFileParses(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		upstream := protocol.Chat
		h := newHarness(t, t.Context(), options{
			client: protocol.Chat, upstream: &upstream, fileParses: 2, filePages: 2, fileBilling: true,
			request: []byte(`{"model":"acme/alpha","messages":[{"role":"user","content":"hello"}],"stream":false}`),
			price:   `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01","file_parse":"0.02"}}`,
			script:  map[int][]scripted{1: {serve(beat{data: chatCited})}},
		})
		h.run()
		usage := h.settlements[0].outcome.Attempts[0].Segments[0].Usage
		if got := usage.Count(catalog.ItemFileParse); got != 2 {
			t.Fatalf("settled parses = %d, want 2", got)
		}
		if got := usage.Count(catalog.ItemWebSearch); got != 0 {
			t.Fatalf("settled parses added %d searches, want none", got)
		}
	})
}

// TestParseFilesRefusesTooManyFiles: the cap is checked before the backend is asked to convert the ninth file.
func TestParseFilesRefusesTooManyFiles(t *testing.T) {
	t.Parallel()
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
	_, _, err = h.parseFiles(t.Context(), c, body, req, true)
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
	t.Parallel()
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

// parseSpec and parsePrice are a model that may search and see files, priced for every item the OpenAI formats may
// report: a parse is charged as a flat file_parse, and the sale schedule has to price it for the reservation to hold.
const (
	parseSpec = `{"items":["input","output","cache_write","image_in","web_search"],"tiers":[],` +
		`"context_length":128000,"max_output_tokens":1000,"streams":true,"abilities":["tools","web_search","documents"]}`
	parsePrice = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002",` +
		`"cache_write":"0.000001","image_in":"0.000001","web_search":"0.01","file_parse":"0.01"},` +
		`"service_tiers":{"priority":{"rates":{"input":"0.000001","output":"0.000002",` +
		`"cache_write":"0.000001","image_in":"0.000001","web_search":"0.01","file_parse":"0.01"}}}}`
)

func TestFileParserThroughTheHandler(t *testing.T) {
	t.Parallel()
	testFileParserThroughTheHandler(t, "mistral-ocr", true, 1e9, checkParseReply)
}

func TestCloudflareParsingIsFree(t *testing.T) {
	t.Parallel()
	testFileParserThroughTheHandler(t, "cloudflare-ai", true, 1e9, checkParseReplyFree)
}

func TestFileParseKeepsSearchBillingUntilTheSwitchIsOn(t *testing.T) {
	t.Parallel()
	testFileParserThroughTheHandler(t, "mistral-ocr", false, 1e12, checkParseReplyOld)
}

// Keep the documented test entry points while sharing their HTTP setup and forwarding assertions.
func testFileParserThroughTheHandler(t *testing.T, engine string, fileBilling bool, balance int64, checkReply func(*testing.T, string)) {
	t.Helper()
	parser := &recordingParser{text: "The parsed page."}
	r := newRig(t, func(o *rigOptions) {
		o.spec, o.parser, o.price, o.fileParse = parseSpec, parser, parsePrice, fileBilling
		if !fileBilling {
			o.price = strings.ReplaceAll(parsePrice, `,"file_parse":"0.01"`, "")
		}
	})
	up := withCosts(r, r.billing, fakeCosts{cost: 1000, known: true})
	replies, _ := sample(t, protocol.Chat)
	for _, e := range up.eps {
		e.script = []scripted{wholeAnswer(strings.Join(replies, ""))}
	}
	a := r.newAccount(balance)
	resp, out := r.do(http.MethodPost, "/v1/chat/completions", a.key, bytes.NewReader(fileBody(engine)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, out)
	}
	checkReply(t, out)
	if len(parser.docs) != 1 {
		t.Fatalf("the parser was given %d documents, want one", len(parser.docs))
	}
	c := r.admits[0]
	if engine == "mistral-ocr" && fileBilling {
		checkParse(t, parser, c.Request, c.Body, c)
		checkParseBilling(t, r, c)
	} else if parser.countPages {
		t.Fatal("a path without page billing asked for a page count")
	}
	checkParsedForward(t, up)
	r.idle(1 << 30)
}

// checkParseReplyFree: the file annotation is present, but nothing names a server-tool charge or a search count.
func checkParseReplyFree(t *testing.T, out string) {
	t.Helper()
	hash := fileHash([]byte("%PDF-1.7 a scanned page"))
	for _, want := range []string{`"annotations":[{`, `"type":"file"`, `"hash":"` + hash + `"`, `"text":"The parsed page."`} {
		if !strings.Contains(out, want) {
			t.Fatalf("the reply has no %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"server_tool_cost"`) || strings.Contains(out, `"web_search_requests"`) {
		t.Fatalf("a free parse was charged as a tool:\n%s", out)
	}
}

// checkParseReplyOld: while the switch is off the parse keeps the old unit, so the reply reports one web search and the
// server-tool cost still carries it.
func checkParseReplyOld(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{`"server_tool_cost":0.01`, `"web_search_requests":1`} {
		if !strings.Contains(out, want) {
			t.Fatalf("the old parse unit is missing %s:\n%s", want, out)
		}
	}
}

// checkParseReply: the reply carries the annotation of the parsed file — its hash of the bytes the client sent, its
// name and the text — and the usage names the parse's one per-use charge. A parse is not a search, so the search
// counter stays absent; only the server-tool cost carries it.
func checkParseReply(t *testing.T, out string) {
	t.Helper()
	hash := fileHash([]byte("%PDF-1.7 a scanned page"))
	for _, want := range []string{
		`"annotations":[{`, `"type":"file"`, `"hash":"` + hash + `"`, `"text":"The parsed page."`,
		`"server_tool_cost":0.01`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the reply has no %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"web_search_requests"`) {
		t.Fatalf("the reply counts a parse as a search:\n%s", out)
	}
}

// checkParsedForward: the upstream got the text where the file was, and none of the file's bytes.
func checkParsedForward(t *testing.T, up *fakeUpstreams) {
	t.Helper()
	var forwarded string
	for _, e := range up.eps {
		if len(e.calls) > 0 {
			forwarded = string(e.calls[0].Body)
		}
	}
	if forwarded == "" {
		t.Fatal("the call never reached the upstream")
	}
	if !strings.Contains(forwarded, `"text":"The parsed page."`) || strings.Contains(forwarded, "JVBERi0") {
		t.Fatalf("the upstream got %s, want the parsed text in the file's place", forwarded)
	}
}

// checkParseBilling: the parse is one reservation and one settlement, the call's own, and what it settles includes the
// file_parse price.
func checkParseBilling(t *testing.T, r *rig, c *Call) {
	t.Helper()
	if n := r.count("SELECT count(*) FROM billing.reservations WHERE request_id = $1", c.ID); n != 1 {
		t.Fatalf("the parse wrote %d reservations, want the call's one", n)
	}
	if n := r.count("SELECT count(*) FROM billing.transactions WHERE request_id = $1 AND kind = 'settle'", c.ID); n != 1 {
		t.Fatalf("the call settled %d times, want once", n)
	}
	if n := r.count("SELECT coalesce(sum(e.amount), 0) FROM billing.entries e JOIN billing.transactions t ON t.id = e.transaction_id "+
		"WHERE t.request_id = $1 AND t.kind = 'settle' AND e.book = 'user_balance'", c.ID); n < 10_000_000 {
		t.Fatalf("the call settled %d, less than the parse's own 0.01 credit", n)
	}
}

// TestParseFilesRefusesAFileItCannotParse: only a reference by id may stay in the body; a document anywhere else — a
// tool result, a URL source — is a file the plugin cannot parse, and the request is refused rather than forwarded with
// its document intact.
func TestParseFilesRefusesAFileItCannotParse(t *testing.T) {
	t.Parallel()
	h, c := parseHarness(t, &recordingParser{text: "unused"})
	for name, body := range map[string]string{
		"tool result": `{"model":"acme/alpha","plugins":[{"id":"file-parser","pdf":{"engine":"cloudflare-ai"}}],` +
			`"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[` +
			`{"type":"document","source":{"type":"file","file_id":"file-1"}}]}]}]}`,
		"url document": `{"model":"acme/alpha","plugins":[{"id":"file-parser","pdf":{"engine":"cloudflare-ai"}}],` +
			`"messages":[{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://example.com/scan.pdf"}}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			req, err := protocol.Inspect(protocol.Messages, []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = h.parseFiles(t.Context(), c, []byte(body), req, true)
			ref, ok := errors.AsType[*Refusal](err)
			if !ok || ref.Status != http.StatusBadRequest {
				t.Fatalf("parseFiles() error = %v, want a 400", err)
			}
		})
	}
}

// TestParseFilesLeavesAReference: a file the client named by id is routing's to pin, not content to parse; the inline
// file beside it is parsed, and the request is not refused for the reference.
func TestParseFilesLeavesAReference(t *testing.T) {
	t.Parallel()
	parser := &recordingParser{text: "The parsed page."}
	h, c := parseHarness(t, parser)
	body := []byte(`{"model":"acme/alpha","plugins":[{"id":"file-parser","pdf":{"engine":"cloudflare-ai"}}],` +
		`"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-1"}},` +
		`{"type":"file","file":{"file_data":"` + base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 a scan")) + `"}}]}]}`)
	req, err := protocol.Inspect(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := h.parseFiles(t.Context(), c, body, req, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(parser.docs) != 1 || !strings.Contains(string(out), `"file_id":"file-1"`) {
		t.Fatalf("the reference was not left in place: %s", out)
	}
	c.Done()
}

// TestOnlineSurvivesParsing: the model suffix is not a body member, so the re-inspection after a parse cannot see it.
// The web plugin the suffix asked for is kept, and its reserved input with it.
func TestOnlineSurvivesParsing(t *testing.T) {
	t.Parallel()
	parser := &recordingParser{text: "The parsed page."}
	r := newRig(t, func(o *rigOptions) {
		o.spec, o.parser = parseSpec, parser
		o.price = parsePrice
		o.search = &recordingSearcher{}
	})
	a := r.newAccount(1e9)
	body := strings.Replace(string(fileBody("cloudflare-ai")), `"model":"acme/alpha"`, `"model":"acme/alpha:online"`, 1)
	resp, out := r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, out)
	}
	admitted := r.admits[0]
	if admitted.Request.Web == nil || !admitted.Request.Web.Online {
		t.Fatalf("admitted %+v, want the suffix's web plugin kept", admitted.Request.Web)
	}
	if admitted.Request.SearchBytes != protocol.WebSearchMaxBytes || admitted.Request.FileParses != 1 {
		t.Fatalf("search bytes = %d, parses = %d", admitted.Request.SearchBytes, admitted.Request.FileParses)
	}
	r.idle(1 << 30)
}
