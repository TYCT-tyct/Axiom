// Package fileparse runs the gateway's file-parser plugin through a self-hosted Docling serve instance: one document
// in, its text and, when asked, its page count out, within the size and time bounds the gateway charges for. The backend is configured by the
// deployment administrator and off by default; nothing here knows about a paid OCR service.
package fileparse

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"axiom/internal/platform/config"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
)

// The environment variables that configure the backend. They are named here, where they are read, so a startup error
// and .env.example spell them the same way.
const (
	urlVar = "AXIOM_FILE_PARSER_URL"
	keyVar = "AXIOM_FILE_PARSER_KEY"
)

const (
	// Timeout is how long one parse may take, the whole request included. A scanned report is slower than a search,
	// and the parse runs before the model call it serves, so it must not spend the call's whole budget.
	Timeout = 60 * time.Second
	// ResponseLimit bounds the backend's answer. The gateway charges it to the node's budget while the call runs.
	// A paid PDF answer also carries Docling's JSON document, whose page dictionary supplies the physical page count;
	// the markdown itself remains capped at MaxTextBytes.
	ResponseLimit = 8 << 20
	// MaxTextBytes is the most text one document may become. The text replaces the file in the body the call is
	// quoted at, so a parse that finds more is refused rather than silently cut.
	MaxTextBytes = 2 << 20
)

var (
	// ErrUpstream is a parser backend that did not answer with usable text: a status other than 200, a body over the
	// cap, a failed conversion, malformed JSON, or a transport error. The gateway answers it as an upstream failure.
	ErrUpstream = errors.New("fileparse: the backend failed")
	// ErrTooLarge is a backend answer, or the text in it, over the cap. The gateway answers it as payload_too_large.
	ErrTooLarge = errors.New("fileparse: the parsed text is larger than the limit")
	// ErrEmpty is an answer the backend returned with no text in it: the file is fine but parsing cannot stand in for
	// it. The gateway answers it as an upstream failure, because nothing about the request was wrong.
	ErrEmpty = errors.New("fileparse: the backend found no text")
	// ErrEmptyFile is a file with no bytes, which no backend can read. The gateway answers it as a malformed request.
	ErrEmptyFile = errors.New("fileparse: the file is empty")
)

// Config is the backend one node parses through. The zero value means file parsing is disabled.
type Config struct {
	URL string        // the Docling serve base URL, without a query; /v1/convert/source is appended
	Key secret.Secret // the X-Api-Key a Docling serve started with DOCLING_SERVE_API_KEY checks; empty sends none
}

// Configured reports whether a backend is set.
func (c Config) Configured() bool { return c.URL != "" }

// ReadConfig reads the backend from env. It is one boundary with the admin's saved settings, so both go through Parse.
func ReadConfig(env *config.Env) (Config, error) {
	return Parse(env.String(urlVar, ""), env.String(keyVar, ""))
}

// Parse validates a backend URL and key from any boundary: the environment or the admin's saved settings. With no URL
// the zero Config is returned and every request keeps the documented refusal; a key without a URL is an error rather
// than a credential silently unused.
func Parse(raw, key string) (Config, error) {
	raw, key = strings.TrimSpace(raw), strings.TrimSpace(key)
	if raw == "" {
		if key != "" {
			return Config{}, fmt.Errorf("%s is set but %s is not: the key has no backend to authenticate to", keyVar, urlVar)
		}
		return Config{}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return Config{}, fmt.Errorf("%s must be an http(s) base URL with a host, no credentials, query or fragment", urlVar)
	}
	return Config{URL: strings.TrimRight(raw, "/"), Key: secret.New(key)}, nil
}

// Document is one file to parse. Name carries the extension the backend picks its converter by.
type Document struct {
	Name string
	Data []byte
}

// Parser is what the gateway runs. The interface exists for the one adapter below and for tests; there is no second
// implementation to abstract over yet.
type Parser interface {
	Parse(ctx context.Context, doc Document, countPages bool) (text string, pages int, err error)
}

// Docling parses through a Docling serve instance's v1 conversion API, which is self-hostable and MIT licensed
// (Docling serve《Usage》, read 2026-11-01).
type Docling struct {
	base   *url.URL
	key    secret.Secret
	client *egress.Client
}

// Open validates the configuration and builds the adapter. The caller checks Configured first: a node with no backend
// has no parser, and the gateway keeps its documented refusal in front of the reservation. The client must be the
// node's egress client, so the backend is subject to the same address, redirect and timeout rules as every other
// outbound request.
func Open(c Config, client *egress.Client) (Parser, error) {
	return open(c, client)
}

func open(c Config, client *egress.Client) (*Docling, error) {
	if !c.Configured() {
		return nil, errors.New("fileparse: opened without a backend URL")
	}
	if client == nil {
		return nil, errors.New("fileparse: configured without an egress client")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("fileparse: %s is not an http(s) base URL", urlVar)
	}
	return &Docling{base: u, key: c.Key, client: client}, nil
}

// convertRequest is Docling serve's in-body conversion request: the file inline, markdown out, OCR on. Images are left
// out of the markdown: the text is what the model reads, and a page image would be charged as input tokens for nothing.
type convertRequest struct {
	Sources []convertSource `json:"sources"`
	Options convertOptions  `json:"options"`
}

type convertSource struct {
	Kind     string `json:"kind"`
	Base64   string `json:"base64_string"`
	Filename string `json:"filename"`
}

type convertOptions struct {
	ToFormats []string `json:"to_formats"`
	DoOCR     bool     `json:"do_ocr"`
	// PageBreakPlaceholder marks every page boundary in the markdown. It is only used for non-PDF inputs; a PDF's
	// physical pages come from json_content.pages.
	PageBreakPlaceholder string `json:"md_page_break_placeholder,omitempty"`
}

// convertDocument is the converted document: its markdown and, for a paid PDF, the page dictionary.
type convertDocument struct {
	Markdown    string `json:"md_content"`
	JSONContent *struct {
		Pages map[string]struct{} `json:"pages"`
	} `json:"json_content"`
}

// convertResponse is what the endpoint returns: the converted document and how far the conversion got.
type convertResponse struct {
	Status   string           `json:"status"`
	Document *convertDocument `json:"document"`
}

// Parse sends one document to the backend and returns its text and, when countPages is set, its page count. A PDF's
// count is the number of entries in Docling's page dictionary, so a page with no markdown still counts. A non-PDF
// input has no PDF page tree, so its count comes from a placeholder derived from the file's bytes; the placeholder
// cannot be confused with the document's own text without a deliberate hash collision. A backend that predates the
// placeholder still receives the old request when the billing switch is off. Errors never quote the file or the
// backend's body: the file is user content, and the body may repeat it.
func (d *Docling) Parse(ctx context.Context, doc Document, countPages bool) (string, int, error) {
	if len(doc.Data) == 0 {
		return "", 0, ErrEmptyFile
	}
	name := doc.Name
	if name == "" {
		name = "document"
	}
	pdf := countPages && looksPDF(doc.Data)
	options, placeholder := conversionOptions(doc.Data, countPages)
	body, err := jsonx.MarshalCanonical(convertRequest{
		Sources: []convertSource{{Kind: "file", Base64: base64.StdEncoding.EncodeToString(doc.Data), Filename: name}},
		Options: options,
	})
	if err != nil {
		return "", 0, fmt.Errorf("%w: the request could not be built", ErrUpstream)
	}
	raw, err := d.post(ctx, "/v1/convert/source", body)
	if err != nil {
		return "", 0, err
	}
	var out convertResponse
	if err := jsonx.Unmarshal(raw, &out); err != nil {
		return "", 0, fmt.Errorf("%w: the backend answered invalid JSON", ErrUpstream)
	}
	if out.Status == "failure" || out.Document == nil {
		return "", 0, fmt.Errorf("%w: the conversion ended %s", ErrUpstream, status(out.Status))
	}
	text, pages, err := documentText(out.Document, placeholder, countPages, pdf)
	if err != nil {
		return "", 0, err
	}
	if len(text) > MaxTextBytes {
		return "", 0, fmt.Errorf("%w: the text is %d bytes, more than %d", ErrTooLarge, len(text), MaxTextBytes)
	}
	if strings.TrimSpace(text) == "" {
		return "", 0, ErrEmpty
	}
	return text, pages, nil
}

// documentText validates and counts the backend's markdown: a PDF uses the page dictionary, a non-PDF input uses the
// markdown's page placeholder.
func documentText(doc *convertDocument, placeholder string, countPages, pdf bool) (string, int, error) {
	text := doc.Markdown
	if !utf8.ValidString(text) {
		return "", 0, fmt.Errorf("%w: the text is not UTF-8", ErrUpstream)
	}
	if !pdf {
		text, pages := pageText(text, placeholder, countPages)
		return text, pages, nil
	}
	if doc.JSONContent == nil {
		return "", 0, fmt.Errorf("%w: the backend did not report the PDF's pages", ErrUpstream)
	}
	pages, err := physicalPages(doc.JSONContent.Pages)
	if err != nil {
		return "", 0, err
	}
	return text, pages, nil
}

// conversionOptions asks for markdown and, when pages are billed, either Docling's JSON pages for a PDF or a page
// boundary placeholder unique to this file for a non-PDF input.
func conversionOptions(data []byte, countPages bool) (convertOptions, string) {
	options := convertOptions{ToFormats: []string{"md"}, DoOCR: true}
	if !countPages {
		return options, ""
	}
	if looksPDF(data) {
		options.ToFormats = []string{"md", "json"}
		return options, ""
	}
	sum := sha256.Sum256(data)
	placeholder := "\n\n<!--axiom-page-" + hex.EncodeToString(sum[:8]) + "-->\n\n"
	options.PageBreakPlaceholder = placeholder
	return options, placeholder
}

// pageText counts the page boundaries and removes their placeholders; without counting it leaves the text alone.
func pageText(text, placeholder string, countPages bool) (string, int) {
	if !countPages {
		return text, 1
	}
	pages := 1 + strings.Count(text, placeholder)
	return strings.ReplaceAll(text, placeholder, "\n\n"), pages
}

// Health checks that the backend answers its health endpoint, which is all a configuration test can check without a
// document to convert.
func (d *Docling) Health(ctx context.Context) error {
	_, err := d.get(ctx, "/health")
	return err
}

// status names a conversion status, or "no status" for an answer without one.
func status(s string) string {
	if s == "" {
		return "no status"
	}
	return s
}

// post sends body to path and returns its answer, bounded.
func (d *Docling) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	req, err := d.request(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return d.do(req)
}

// get asks for path, for the health check.
func (d *Docling) get(ctx context.Context, path string) ([]byte, error) {
	req, err := d.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return d.do(req)
}

func (d *Docling) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	u := *d.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("%w: the request could not be built", ErrUpstream)
	}
	req.Header.Set("Accept", "application/json")
	if key := d.key.Reveal(); key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	return req, nil
}

// do runs the request and reads the answer, holding the body to the cap.
func (d *Docling) do(req *http.Request) ([]byte, error) {
	resp, err := d.client.Do(req)
	if err != nil {
		// The transport's error quotes the URL: report only the class, so a log line keeps no backend address.
		return nil, fmt.Errorf("%w: %s", ErrUpstream, transportFailure(err))
	}
	defer resp.Body.Close() //nolint:errcheck // an error closing a response whose answer is already decided changes nothing
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: the backend answered %d", ErrUpstream, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ResponseLimit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read the response: %w", ErrUpstream, err)
	}
	if len(body) > ResponseLimit {
		return nil, fmt.Errorf("%w: the backend answered more than %d bytes", ErrTooLarge, ResponseLimit)
	}
	return body, nil
}

// transportFailure names what went wrong without the request's URL.
func transportFailure(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the backend did not answer in time"
	case errors.Is(err, context.Canceled):
		return "the parse was canceled"
	default:
		return "the backend could not be reached"
	}
}
