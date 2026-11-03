package protocol

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"axiom/internal/platform/alloctest"
)

func testDataURL(data []byte) string {
	return "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(data)
}

func fileHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// parsedRecords counts the backend calls RewriteFiles made, and the names it was given.
type parsedRecords struct {
	calls int
	names []string
	text  string // what every file parses to; the name's when empty
}

func (p *parsedRecords) parse(f File) (string, error) {
	p.calls++
	p.names = append(p.names, f.Name)
	if p.text != "" {
		return p.text, nil
	}
	return "parsed " + f.Name, nil
}

func TestFileParserEngine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		members string
		engine  string
		bad     string
	}{
		{`{"id":"file-parser"}`, FileEngineNative, ""},
		{`{"id":"file-parser","pdf":{"engine":"native"}}`, FileEngineNative, ""},
		{`{"id":"file-parser","pdf":{"engine":"mistral-ocr"}}`, FileEngineMistralOCR, ""},
		{`{"id":"file-parser","pdf":{"engine":"cloudflare-ai"}}`, FileEngineCloudflareAI, ""},
		{`{"id":"file-parser","pdf":{"engine":"pdf-text"}}`, FileEngineCloudflareAI, ""},
		{`{"id":"file-parser","enabled":false,"pdf":{"engine":"mistral-ocr"}}`, "", ""},
		{`{"id":"file-parser","pdf":{"engine":"tika"}}`, "", "not supported"},
		{`{"id":"file-parser","pdf":{"pages":3}}`, "", "not supported"},
		{`{"id":"web","pdf":{"engine":"native"}}`, "", `plugin option "pdf" is not supported`},
		{`{"id":"file-parser","max_results":3}`, "", `plugin option "max_results" is not supported`},
	} {
		t.Run(tc.members, func(t *testing.T) { checkEngine(t, tc.members, tc.engine, tc.bad) })
	}
}

// checkEngine inspects one plugins member: engine is the one the request must end with, "" for none, and bad is what a
// refusal must say.
func checkEngine(t *testing.T, members, engine, bad string) {
	t.Helper()
	r, err := Inspect(Chat, []byte(`{"model":"a","plugins":[`+members+`]}`))
	if bad != "" {
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), bad) {
			t.Fatalf("got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if engine == "" {
		if r.File != nil {
			t.Fatalf("disabled plugin: got %+v", r.File)
		}
		return
	}
	if r.File == nil || r.File.Engine != engine {
		t.Fatalf("got %+v", r.File)
	}
}

func TestFileParserFormat(t *testing.T) {
	t.Parallel()
	for _, format := range []Format{Chat, Messages, Responses} {
		if _, err := Inspect(format, []byte(`{"model":"a","plugins":[{"id":"file-parser","pdf":{"engine":"native"}}]}`)); err != nil {
			t.Fatalf("format %d: %v", format, err)
		}
	}
}

func TestRewriteFiles(t *testing.T) {
	t.Parallel()
	data := []byte("%PDF-1.7 a scan")
	for _, tc := range rewriteCases(data) {
		t.Run(tc.name, func(t *testing.T) {
			rec := &parsedRecords{}
			body, files, err := RewriteFiles(tc.format, []byte(tc.body), rec.parse)
			if err != nil {
				t.Fatal(err)
			}
			checkRewrite(t, tc, rec, body, files)
		})
	}
}

// rewriteCase is one request, and what its rewrite must do: how many backend calls, how many files, the text the body
// must carry in their place ("" for none), and whether the text came from a parse rather than an annotation.
type rewriteCase struct {
	name   string
	format Format
	body   string
	calls  int
	found  int
	text   string
	parsed bool
}

func rewriteCases(data []byte) []rewriteCase {
	return []rewriteCase{
		{
			name: "chat file", format: Chat,
			body: `{"model":"a","messages":[{"role":"user","content":[{"type":"text","text":"read"},` +
				`{"type":"file","file":{"filename":"scan.pdf","file_data":"` + testDataURL(data) + `"}}]}]}`,
			calls: 1, found: 1, text: "parsed scan.pdf", parsed: true,
		},
		{
			name: "responses input_file", format: Responses,
			body: `{"model":"a","input":[{"role":"user","content":[{"type":"input_file","filename":"scan.pdf",` +
				`"file_data":"` + testDataURL(data) + `"}]}]}`,
			calls: 1, found: 1, text: "parsed scan.pdf", parsed: true,
		},
		{
			name: "messages document", format: Messages,
			body: `{"model":"a","messages":[{"role":"user","content":[{"type":"document","title":"scan.pdf",` +
				`"source":{"type":"base64","media_type":"application/pdf","data":"` +
				base64.StdEncoding.EncodeToString(data) + `"}}]}]}`,
			calls: 1, found: 1, text: "parsed scan.pdf", parsed: true,
		},
		{
			name: "bare base64", format: Chat,
			body: `{"model":"a","messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"` +
				base64.StdEncoding.EncodeToString(data) + `"}}]}]}`,
			calls: 1, found: 1, text: "parsed document", parsed: true,
		},
		{
			name: "an annotation skips the parse", format: Chat,
			body: `{"model":"a","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"scan.pdf",` +
				`"file_data":"` + testDataURL(data) + `"}}]},{"role":"assistant","content":"ok","annotations":[` +
				`{"type":"file","file":{"hash":"` + fileHash(data) + `","name":"scan.pdf",` +
				`"content":[{"type":"text","text":"cached text"}]}}]}]}`,
			calls: 0, found: 1, text: "cached text",
		},
		{
			name: "the same file is parsed once", format: Chat,
			body: `{"model":"a","messages":[{"role":"user","content":[` +
				`{"type":"file","file":{"file_data":"` + testDataURL(data) + `"}},` +
				`{"type":"file","file":{"file_data":"` + testDataURL(data) + `"}}]}]}`,
			calls: 1, found: 1, text: "parsed document.pdf", parsed: true,
		},
		{
			name: "a file id is not content", format: Chat,
			body: `{"model":"a","messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file_1"}}]}]}`,
		},
	}
}

// checkRewrite holds a rewrite to its case: the calls, the files, the text standing in their place, and that the body
// still passes Inspect with no document left in it.
func checkRewrite(t *testing.T, tc rewriteCase, rec *parsedRecords, body []byte, files []ParsedFile) {
	t.Helper()
	if rec.calls != tc.calls || len(files) != tc.found {
		t.Fatalf("got %d calls, %d files, want %d and %d", rec.calls, len(files), tc.calls, tc.found)
	}
	if tc.found > 0 {
		if files[0].Hash != fileHash([]byte("%PDF-1.7 a scan")) || files[0].Parsed != tc.parsed {
			t.Fatalf("got %+v", files[0])
		}
		if !strings.Contains(string(body), tc.text) {
			t.Fatalf("the body does not carry %q: %s", tc.text, body)
		}
		req, err := Inspect(tc.format, body)
		if err != nil {
			t.Fatal(err)
		}
		if req.Documents != 0 {
			t.Fatalf("a file remains in the body: %s", body)
		}
	}
}

func TestRewriteFilesRefusals(t *testing.T) {
	t.Parallel()
	data := []byte("%PDF")
	for _, tc := range []struct {
		name string
		body string
		want error
	}{
		{
			name: "both data and id",
			body: `{"model":"a","messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"f","file_data":"` +
				testDataURL(data) + `"}}]}]}`,
			want: ErrInvalid,
		},
		{
			name: "not base64",
			body: `{"model":"a","messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"!!"}}]}]}`,
			want: ErrInvalid,
		},
		{
			name: "too large",
			body: `{"model":"a","messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"` +
				base64.StdEncoding.EncodeToString(make([]byte, MaxFileBytes+1)) + `"}}]}]}`,
			want: ErrFileTooLarge,
		},
		{
			name: "a file in assistant content",
			body: `{"model":"a","messages":[{"role":"assistant","content":[{"type":"file","file":{"file_data":"` +
				testDataURL(data) + `"}}]}]}`,
			want: ErrInvalid,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &parsedRecords{}
			if _, _, err := RewriteFiles(Chat, []byte(tc.body), rec.parse); !errors.Is(err, tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestFileAnnotationEntries(t *testing.T) {
	t.Parallel()
	got := string(FileAnnotationEntries([]FileAnnotation{{Hash: "h", Name: "a.pdf", Text: "text"}}))
	want := `{"file":{"content":[{"text":"text","type":"text"}],"hash":"h","name":"a.pdf"},"type":"file"}`
	if got != want {
		t.Fatalf("got %s", got)
	}
	unnamed := string(FileAnnotationEntries([]FileAnnotation{{Hash: "h", Text: "text"}}))
	if strings.Contains(unnamed, `"name"`) {
		t.Fatalf("an unnamed file carries a name: %s", unnamed)
	}
}

// TestRewriteFilesKeepsTheBytesItDoesNotReplace: a member the rewrite knows nothing about is copied as the client wrote
// it, including a number the encoder cannot hold. Rewriting one content array must not re-encode the message.
func TestRewriteFilesKeepsTheBytesItDoesNotReplace(t *testing.T) {
	t.Parallel()
	data := []byte("%PDF-1.7 a scan")
	body := `{"model":"a","messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"` +
		testDataURL(data) + `"}}],"seed":1e9999,"note":"kept"}]}`
	if _, err := Inspect(Chat, []byte(body)); err != nil {
		t.Fatalf("Inspect refused the request: %v", err)
	}
	out, files, err := RewriteFiles(Chat, []byte(body), (&parsedRecords{}).parse)
	if err != nil {
		t.Fatalf("RewriteFiles() error = %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	for _, want := range []string{`"seed":1e9999`, `"note":"kept"`, `"role":"user"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("the rewrite lost %s:\n%s", want, out)
		}
	}
}

// TestRewriteFilesCountsEveryCopyOfAText: a history that repeats one file writes the settled text beside every copy of
// it, so the copies are held to the request's total, not the parse's one.
func TestRewriteFilesCountsEveryCopyOfAText(t *testing.T) {
	t.Parallel()
	rec := &parsedRecords{text: strings.Repeat("x", MaxParsedBytes/2)}
	file := `{"type":"file","file":{"file_data":"` + testDataURL([]byte("%PDF-1.7 a scan")) + `"}}`
	body := `{"model":"a","messages":[{"role":"user","content":[` + file + `,` + file + `,` + file + `]}]}`
	if _, _, err := RewriteFiles(Chat, []byte(body), rec.parse); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("RewriteFiles() error = %v, want %v", err, ErrFileTooLarge)
	}
	if rec.calls != 1 {
		t.Fatalf("the file was parsed %d times, want once", rec.calls)
	}
}

// TestFileReferences: only a part that names a supplier's file without carrying one is a reference a rewrite may leave;
// anything else that still counts as a document is a file it could not parse.
func TestFileReferences(t *testing.T) {
	t.Parallel()
	ref := `{"type":"file","file":{"file_id":"file-1"}}`
	inline := `{"type":"file","file":{"file_data":"` + testDataURL([]byte("%PDF-1.7 a scan")) + `"}}`
	for _, tc := range []struct {
		name, body string
		format     Format
		want       int
	}{
		{
			name: "chat reference", format: Chat, want: 1,
			body: `{"model":"a","messages":[{"role":"user","content":[` + ref + `]}]}`,
		},
		{name: "chat inline", format: Chat, body: `{"model":"a","messages":[{"role":"user","content":[` + inline + `]}]}`},
		{
			name: "chat both", format: Chat, want: 1,
			body: `{"model":"a","messages":[{"role":"user","content":[` + ref + `,` + inline + `]}]}`,
		},
		{
			name: "responses reference", format: Responses, want: 1,
			body: `{"model":"a","input":[{"role":"user","content":[{"type":"input_file","file_id":"file-1"}]}]}`,
		},
		{
			name: "messages reference", format: Messages, want: 1,
			body: `{"model":"a","messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file-1"}}]}]}`,
		},
		{
			name: "messages tool result", format: Messages,
			body: `{"model":"a","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t",` +
				`"content":[{"type":"document","source":{"type":"file","file_id":"file-1"}}]}]}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FileReferences(tc.format, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("FileReferences() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestQuotedLength: the budget a text is charged by is what the encoder actually writes. A change in the escaping
// would grow the buffer past what was charged before the text reached the reply, so the two are compared here.
func TestQuotedLength(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"", "plain markdown", `a "quoted" word`, `back\slash`, "tab\tnewline\ncarriage\r", "\x00\x01\x1f",
		"unicode: 世界, emoji 🙂", "line separator", "\xff\xfe invalid", strings.Repeat(`"\\`, 200),
	} {
		if got, want := quotedLength(s), len(appendQuoted(nil, s)); got != want {
			t.Errorf("quotedLength(%q) = %d, appendQuoted writes %d", s, got, want)
		}
	}
}

// TestAnnotationRefusedForBudgetAllocatesNothing: a reply whose budget cannot hold the annotations goes without them,
// and it decides that before encoding a megabyte of text into an entry it would throw away.
func TestAnnotationRefusedForBudgetAllocatesNothing(t *testing.T) {
	t.Parallel()
	files := []FileAnnotation{{Hash: strings.Repeat("a", 64), Name: "scan.pdf", Text: strings.Repeat("x", 1<<20)}}
	a := &chatAggregate{}
	a.init(func(int) error { return ErrMemory })
	got := alloctest.Measure(t, alloctest.Function{Name: "annotate", F: func() { a.annotateFiles(files) }})
	if bytes := got["annotate"].Bytes; bytes > 8<<10 {
		t.Fatalf("a refused annotation allocated %v bytes, want the bound's scan only", bytes)
	}
}

// TestAnnotationBudgetCoversEveryChoicesBody: a reply's file annotations are held once as the entries and again in every
// choice, and each choice's render copies them once more into the body. A budget that covers the entries and the pieces
// but not those copies is short of what the reply allocates, so it takes no annotations at all.
func TestAnnotationBudgetCoversEveryChoicesBody(t *testing.T) {
	t.Parallel()
	const choices = 8
	text := strings.Repeat("x", 1<<20)
	files := []FileAnnotation{{Hash: strings.Repeat("a", 64), Name: "scan.pdf", Text: text}}
	bound := int64(quotedLength(text) + quotedLength(files[0].Hash) + quotedLength(files[0].Name) + 128)
	// Entries twice over and every choice's piece once: what the reply holds without the body's copies.
	withoutBody := 2*bound + choices*bound
	a := annotateWithin(files, choices, withoutBody+1024)
	for i := range a.choices {
		if len(a.choices[i].annotations) > 0 {
			t.Fatalf("choice %d carries annotations a budget of %d cannot hold", i, withoutBody+1024)
		}
	}
	a = annotateWithin(files, choices, 1<<30)
	for i := range a.choices {
		if !strings.Contains(string(a.choices[i].annotations), "scan.pdf") {
			t.Fatalf("choice %d has no annotations: %q", i, a.choices[i].annotations)
		}
	}
}

// TestAnnotationRollbackKeepsUpstreamAnnotations: a budget that runs out at the second choice takes the file annotations
// off the first again, and leaves every choice's own upstream annotations as they were, the choices not reached included.
func TestAnnotationRollbackKeepsUpstreamAnnotations(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("x", 1<<20)
	files := []FileAnnotation{{Hash: strings.Repeat("a", 64), Name: "scan.pdf", Text: text}}
	bound := int64(quotedLength(text) + quotedLength(files[0].Hash) + quotedLength(files[0].Name) + 128)
	citation := `{"type":"url_citation"}`
	choices := make([]chatChoice, 3)
	choices[2].annotations = []byte(citation)
	// The entries twice over and the first choice's copy twice over fit; the second choice's copy does not.
	a := annotateWithinChoices(files, choices, 5*bound)
	for i, want := range []string{"", "", citation} {
		if got := string(a.choices[i].annotations); got != want {
			t.Errorf("choice %d annotations = %.40q, want %q", i, got, want)
		}
	}
}

// annotateWithin runs the file annotations on a reply of choices choices under a budget of budget bytes.
func annotateWithin(files []FileAnnotation, choices int, budget int64) *chatAggregate {
	return annotateWithinChoices(files, make([]chatChoice, choices), budget)
}

// annotateWithinChoices runs the file annotations on a reply holding choices under a budget of budget bytes.
func annotateWithinChoices(files []FileAnnotation, choices []chatChoice, budget int64) *chatAggregate {
	a := &chatAggregate{choices: choices}
	left := budget
	a.init(func(n int) error {
		if int64(n) > left {
			return ErrMemory
		}
		left -= int64(n)
		return nil
	})
	a.annotateFiles(files)
	return a
}
