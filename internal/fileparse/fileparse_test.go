package fileparse_test

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"axiom/internal/fileparse"
	"axiom/internal/platform/config"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

func env(t *testing.T, values map[string]string) *config.Env {
	t.Helper()
	var environ []string
	for k, v := range values {
		environ = append(environ, k+"="+v)
	}
	return config.New(environ)
}

func TestReadConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		want    string
		wantErr string
	}{
		{name: "none", env: map[string]string{}, want: ""},
		{name: "url only", env: map[string]string{"AXIOM_FILE_PARSER_URL": "https://parse.example.com/docling/"}, want: "https://parse.example.com/docling"},
		{name: "key only", env: map[string]string{"AXIOM_FILE_PARSER_KEY": "secret"}, wantErr: "AXIOM_FILE_PARSER_URL is not"},
		{name: "not a url", env: map[string]string{"AXIOM_FILE_PARSER_URL": "parse.example.com"}, wantErr: "must be an http(s) base URL"},
		{
			//nolint:gosec // a deliberately invalid URL, not a credential
			name: "credentials", env: map[string]string{"AXIOM_FILE_PARSER_URL": "https://user:pass@parse.example.com"},
			wantErr: "must be an http(s) base URL",
		},
		{name: "query", env: map[string]string{"AXIOM_FILE_PARSER_URL": "https://parse.example.com/?a=b"}, wantErr: "must be an http(s) base URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fileparse.ReadConfig(env(t, tc.env))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ReadConfig() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadConfig() error = %v", err)
			}
			if got.URL != tc.want {
				t.Fatalf("ReadConfig().URL = %q, want %q", got.URL, tc.want)
			}
		})
	}
}

// TestOpenRequiresAConfiguredBackend: the node checks Configured first, so a deployment with no backend never opens an
// adapter and keeps the documented refusal.
func TestOpenRequiresAConfiguredBackend(t *testing.T) {
	client := egress.NewForTesting(egresstest.Loopback(t))
	if _, err := fileparse.Open(fileparse.Config{}, client); err == nil {
		t.Fatal("Open() without a backend URL succeeded")
	}
}

func TestParseSendsTheDocumentAndReadsTheText(t *testing.T) {
	document := []byte("%PDF-1.7 a scanned page")
	server := httptest.NewTLSServer(conversion(t, document, true))
	t.Cleanup(server.Close)
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	parser, err := fileparse.Open(fileparse.Config{URL: server.URL + "/docling", Key: secret.New("parse-key")}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	text, pages, err := parser.Parse(ctx, fileparse.Document{Name: "scan.pdf", Data: document}, true)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if text != "# Report\n\nThe text.\n\nMore text.\n\nEnd." || pages != 3 {
		t.Fatalf("Parse() = %q, %d pages", text, pages)
	}
}

// TestParseWithoutPageCounting: the old billing path sends the request shape older Docling versions already accept and
// reports one page; the gateway does not use the count while the switch is off.
func TestParseWithoutPageCounting(t *testing.T) {
	document := []byte("%PDF-1.7 a scanned page")
	server := httptest.NewTLSServer(conversion(t, document, false))
	t.Cleanup(server.Close)
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	parser, err := fileparse.Open(fileparse.Config{URL: server.URL + "/docling", Key: secret.New("parse-key")}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	text, pages, err := parser.Parse(ctx, fileparse.Document{Name: "scan.pdf", Data: document}, false)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if text != "# Report\n\nThe text." || pages != 1 {
		t.Fatalf("Parse() = %q, %d pages", text, pages)
	}
}

// conversion checks the request Docling serve is sent, and answers with a markdown document.
func conversion(t *testing.T, document []byte, countPages bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		placeholder := checkConversion(t, r, document, countPages)
		markdown := "# Report\n\nThe text."
		if countPages {
			markdown += placeholder + "More text." + placeholder + "End."
		}
		body, err := jsonx.MarshalCanonical(map[string]any{
			"status": "success", "document": map[string]string{"md_content": markdown}, "processing_time": 1.5,
		})
		if err != nil {
			t.Errorf("the test body is not JSON: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(body); err != nil {
			t.Errorf("the backend could not write its body: %v", err)
		}
	}
}

// checkConversion holds one conversion request to the shape Docling serve is called with.
func checkConversion(t *testing.T, r *http.Request, document []byte, countPages bool) string {
	if r.URL.Path != "/docling/v1/convert/source" {
		t.Errorf("path = %q, want /docling/v1/convert/source", r.URL.Path)
	}
	if got := r.Header.Get("X-Api-Key"); got != "parse-key" {
		t.Errorf("X-Api-Key = %q, want the configured key", got)
	}
	var sent convertRequest
	if err := jsonx.Unmarshal(readAll(t, r.Body), &sent); err != nil {
		t.Errorf("the request is not the conversion request: %v", err)
	}
	if len(sent.Sources) != 1 || sent.Sources[0].Kind != "file" || sent.Sources[0].Filename != "scan.pdf" {
		t.Errorf("sources = %+v", sent.Sources)
	}
	if got, err := base64.StdEncoding.DecodeString(sent.Sources[0].Base64); err != nil || string(got) != string(document) {
		t.Errorf("the backend did not receive the document")
	}
	checkConversionOptions(t, sent.Options, countPages)
	return sent.Options.PageBreakPlaceholder
}

// checkConversionOptions holds the markdown and page-boundary options to the ones the parser needs.
func checkConversionOptions(t *testing.T, o conversionOptions, countPages bool) {
	t.Helper()
	if len(o.ToFormats) != 1 || o.ToFormats[0] != "md" || !o.DoOCR {
		t.Errorf("options = %+v", o)
	}
	if countPages != (o.PageBreakPlaceholder != "") {
		t.Errorf("page boundary request = %q, want counted = %t", o.PageBreakPlaceholder, countPages)
	}
}

// convertRequest is the part of Docling serve's conversion request these tests read.
type convertRequest struct {
	Sources []struct {
		Kind     string `json:"kind"`
		Base64   string `json:"base64_string"`
		Filename string `json:"filename"`
	} `json:"sources"`
	Options conversionOptions `json:"options"`
}

type conversionOptions struct {
	ToFormats            []string `json:"to_formats"`
	DoOCR                bool     `json:"do_ocr"`
	PageBreakPlaceholder string   `json:"md_page_break_placeholder"`
}

func TestParseBackendFailures(t *testing.T) {
	document := []byte("%PDF-1.7 client-content")
	for _, tc := range []struct {
		name   string
		status int
		body   string
		empty  bool
		want   error
	}{
		{name: "status", status: http.StatusInternalServerError, body: `{"status":"failure"}`, want: fileparse.ErrUpstream},
		{name: "not json", status: http.StatusOK, body: `not json`, want: fileparse.ErrUpstream},
		{name: "failed conversion", status: http.StatusOK, body: `{"status":"failure","errors":[{"error_message":"client-content"}]}`, want: fileparse.ErrUpstream},
		{name: "no document", status: http.StatusOK, body: `{"status":"skipped"}`, want: fileparse.ErrUpstream},
		{name: "not utf-8", status: http.StatusOK, body: `{"status":"success","document":{"md_content":"\ud800"}}`, want: fileparse.ErrUpstream},
		{name: "empty", status: http.StatusOK, body: `{"status":"success","document":{"md_content":"  \n"}}`, want: fileparse.ErrEmpty},
		{name: "an empty file", empty: true, status: http.StatusOK, want: fileparse.ErrEmptyFile},
		{name: "too large", status: http.StatusOK, body: `{"status":"success","document":{"md_content":"` + strings.Repeat("x", fileparse.MaxTextBytes+1) + `"}}`, want: fileparse.ErrTooLarge},
		{name: "body too large", status: http.StatusOK, body: strings.Repeat("x", fileparse.ResponseLimit+1), want: fileparse.ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := failingBackend(t, tc.status, tc.body)
			client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
			parser, err := fileparse.Open(fileparse.Config{URL: server.URL}, client)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			data := document
			if tc.empty {
				data = nil
			}
			_, _, err = parser.Parse(ctx, fileparse.Document{Name: "scan.pdf", Data: data}, true)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Parse() error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "client-content") {
				t.Fatalf("the error quotes the file or the backend body: %v", err)
			}
		})
	}
}

// TestParseTimeout: a backend that does not answer within the context's deadline is an upstream failure, not a hang.
func TestParseTimeout(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(`{"status":"success","document":{"md_content":"late"}}`)); err != nil {
				return // the client is gone
			}
		}
	}))
	t.Cleanup(server.Close)
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	parser, err := fileparse.Open(fileparse.Config{URL: server.URL}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, _, err = parser.Parse(ctx, fileparse.Document{Name: "scan.pdf", Data: []byte("%PDF")}, true)
	if !errors.Is(err, fileparse.ErrUpstream) || !strings.Contains(err.Error(), "in time") {
		t.Fatalf("Parse() error = %v, want a timeout as ErrUpstream", err)
	}
}

func TestHealth(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docling/health" {
			t.Errorf("path = %q, want /docling/health", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	if got := fileparse.TestConfig(t.Context(), fileparse.Config{URL: server.URL + "/docling"}, client, quietLogger()); got != "accepted" {
		t.Fatalf("TestConfig() = %q, want accepted", got)
	}
}

// failingBackend answers every conversion with status and body.
func failingBackend(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("the backend could not write its body: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func quietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
