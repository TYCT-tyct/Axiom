package protocol

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
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
}

func (p *parsedRecords) parse(f File) (string, error) {
	p.calls++
	p.names = append(p.names, f.Name)
	return "parsed " + f.Name, nil
}

func TestFileParserEngine(t *testing.T) {
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
	for _, format := range []Format{Chat, Messages, Responses} {
		if _, err := Inspect(format, []byte(`{"model":"a","plugins":[{"id":"file-parser","pdf":{"engine":"native"}}]}`)); err != nil {
			t.Fatalf("format %d: %v", format, err)
		}
	}
}

func TestRewriteFiles(t *testing.T) {
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
