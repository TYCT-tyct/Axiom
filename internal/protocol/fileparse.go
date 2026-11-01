package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // the request's parts are read as written, as in websearch.go
	"errors"
	"fmt"
	"strings"

	"axiom/internal/platform/jsonx"
)

// The file-parser plugin's engines, as OpenRouter names them (OpenRouter《PDF Inputs》, read 2026-11-01).
const (
	FileEngineNative       = "native"
	FileEngineMistralOCR   = "mistral-ocr"
	FileEngineCloudflareAI = "cloudflare-ai"
)

// FileParser is the file-parser plugin as the request asked for it. Native sends the file to an endpoint that accepts
// documents; the OCR engines replace it with text parsed by the deployment's backend before the call is quoted.
type FileParser struct {
	Engine string
}

// Parsed reports whether the plugin replaces the request's files with parsed text. An engine the request did not name
// is Native: OpenRouter would fall back to its OCR engine for a model without native file input, but this gateway
// cannot know that before routing, and it does not start a paid parse the client did not ask for.
func (f *FileParser) Parsed() bool { return f != nil && f.Engine != FileEngineNative }

// NoFileParser says what is missing for the OCR engines. It is the sentence a node without a parser backend refuses
// the plugin with, unchanged from before a backend could be configured.
const NoFileParser = "external services are not configured"

// The bounds of one parse, checked before the backend is called.
const (
	MaxFileBytes       = 20 << 20 // one file's decoded bytes
	MaxFilesPerRequest = 8        // files one request parses
	MaxParsedBytes     = 2 << 20  // the most text a request's files may be replaced by, all of them together
)

// fileAnnotationCap bounds the file annotations a history may offer to skip a parse: one per file a request parses.
const fileAnnotationCap = MaxFilesPerRequest

// ErrFileTooLarge and ErrTooManyFiles are a request whose content is too big: well formed, but past what one call
// parses. The gateway answers them as payload_too_large, not as a malformed body.
var (
	ErrFileTooLarge = errors.New("protocol: a file is larger than the limit")
	ErrTooManyFiles = errors.New("protocol: the request carries more files than the limit")
)

// setEngine reads a pdf.engine value. pdf-text is the deprecated engine OpenRouter redirects to cloudflare-ai
// (OpenRouter《PDF Inputs》, read 2026-11-01).
func (f *FileParser) setEngine(name string) error {
	switch name {
	case FileEngineNative, FileEngineMistralOCR, FileEngineCloudflareAI:
		f.Engine = name
	case "pdf-text":
		f.Engine = FileEngineCloudflareAI
	default:
		return fmt.Errorf("pdf.engine %.64q is not supported: want native, mistral-ocr or cloudflare-ai", name)
	}
	return nil
}

// File is one inline file part of a request.
type File struct {
	Name string // the client's file name, when it gave one
	Data []byte // the decoded bytes
	Hash string // hex SHA-256 of Data: the hash a file annotation names the file by
}

// ParsedFile is a file RewriteFiles replaced, its text, and whether the deployment parsed it: a text the request's own
// file annotation supplied is neither parsed again nor charged.
type ParsedFile struct {
	File
	Text   string
	Parsed bool
}

// FileAnnotation is one file annotation of a Chat reply: the hash a later request names the file by, and the text the
// file was parsed into (OpenRouter《PDF Inputs》, read 2026-11-01). A client sends it back on a previous assistant
// message to skip parsing the same file again.
type FileAnnotation struct {
	Hash, Name, Text string
}

// readFileAnnotations records the parsed texts a Chat history carries on its assistant messages, by file hash. A
// malformed or oversized annotation is ignored rather than refused: the file is then parsed again, which is right.
func readFileAnnotations(f Format, raw jsontext.Value, into map[string]string) {
	if f != Chat || len(into) >= fileAnnotationCap {
		return
	}
	item, err := requestMembers(raw)
	if err != nil || itemRole(item) != "assistant" {
		return
	}
	entries, err := requestArray(item["annotations"])
	if err != nil {
		return
	}
	for _, entry := range entries {
		if len(into) >= fileAnnotationCap {
			return
		}
		hash, text, ok := fileAnnotation(entry)
		if !ok {
			continue
		}
		if _, seen := into[hash]; !seen {
			into[hash] = text
		}
	}
}

// fileAnnotation reads one file annotation's hash and text.
func fileAnnotation(raw jsontext.Value) (hash, text string, ok bool) {
	entry, err := requestMembers(raw)
	if err != nil || optionalString(entry["type"]) != "file" {
		return "", "", false
	}
	file, err := requestMembers(entry["file"])
	if err != nil {
		return "", "", false
	}
	hash = optionalString(file["hash"])
	if hash == "" || len(hash) > 128 {
		return "", "", false
	}
	text, ok = annotationText(file["content"])
	if !ok {
		return "", "", false
	}
	return hash, text, true
}

// annotationText joins the text parts of an annotation's content, within the cap A parse may be replaced by. ok is
// false for anything but text, which has no text to stand in the file's place.
func annotationText(raw jsontext.Value) (string, bool) {
	parts, err := requestArray(raw)
	if err != nil {
		return "", false
	}
	var b strings.Builder
	for _, part := range parts {
		p, err := requestMembers(part)
		if err != nil {
			return "", false
		}
		if optionalString(p["type"]) != "text" {
			return "", false
		}
		s, err := requestString(p["text"])
		if err != nil || b.Len()+len(s) > MaxParsedBytes {
			return "", false
		}
		b.WriteString(s)
	}
	return b.String(), b.Len() > 0
}

// FileAnnotationEntries encodes the file annotations of a Chat reply, as OpenRouter returns them (OpenRouter《PDF
// Inputs》, read 2026-11-01): a file's hash, its name when the client gave one, and its parsed text.
func FileAnnotationEntries(files []FileAnnotation) []byte {
	var out []byte
	for _, f := range files {
		file := map[string]any{"hash": f.Hash, "content": []any{map[string]any{"type": "text", "text": f.Text}}}
		if f.Name != "" {
			file["name"] = f.Name
		}
		entry, err := jsonx.MarshalCanonical(map[string]any{"type": "file", "file": file})
		if err != nil {
			continue
		}
		if len(out) > 0 {
			out = append(out, ',')
		}
		out = append(out, entry...)
	}
	return out
}

// files is the ending's annotations: a failure that carried no ending has none.
func (f *Final) files() []FileAnnotation {
	if f == nil {
		return nil
	}
	return f.Files
}

// annotateFiles adds the file annotations the gateway's parser supplied to every choice's message, so a client can
// send the parsed text back instead of the file (OpenRouter《PDF Inputs》, read 2026-11-01). A reply whose budget
// cannot hold them goes without: the parsed text is in the prompt either way.
func (a *chatAggregate) annotateFiles(files []FileAnnotation) {
	if len(files) == 0 || a.sealed {
		return
	}
	// The entries are encoded before any choice holds them, and the encoding is larger than the text it carries: reserve
	// its bound before FileAnnotationEntries allocates it.
	bound := annotationBound(files)
	if a.take(bound, 2) != nil {
		return
	}
	entries := FileAnnotationEntries(files)
	if len(entries) == 0 || len(entries) > bound {
		return // an encoding that grew past its bound is not appended: better no annotation than an uncharged copy
	}
	a.appendAnnotations(entries)
}

// appendAnnotations writes the entries into every choice, charging each copy twice over, as the other annotation paths
// are: the piece the choice keeps and the body its render is written into. A choice that cannot be charged leaves every
// choice without the annotations, so a reply never carries them on some choices and not others.
func (a *chatAggregate) appendAnnotations(entries []byte) {
	prior := make([]int, len(a.choices))
	for i := range a.choices {
		c := &a.choices[i]
		prior[i] = len(c.annotations)
		n := len(entries)
		if prior[i] > 0 {
			n++
		}
		var err error
		if c.annotations, err = grow(&a.aggBase, c.annotations, n, 2); err != nil {
			for j := range a.choices {
				a.choices[j].annotations = a.choices[j].annotations[:prior[j]]
			}
			return
		}
		if len(c.annotations) > 0 {
			c.annotations = append(c.annotations, ',')
		}
		c.annotations = append(c.annotations, entries...)
	}
}

// annotationBound is the encoded length of the file annotations: every string quoted, and each entry's own syntax.
func annotationBound(files []FileAnnotation) int {
	bound := 0
	for _, f := range files {
		bound += quotedLength(f.Text) + quotedLength(f.Hash) + quotedLength(f.Name) + 128
	}
	return bound
}
