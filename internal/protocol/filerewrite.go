package protocol

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/jsontext" //nolint:depguard // the request's parts are copied as written, as in websearch.go
	"fmt"
	"slices"
	"strings"
)

// RewriteFiles replaces the inline files of a request with the text parse gives for each, and returns the body and the
// files it replaced. A file annotation in the same request supplies the text for a file whose hash it names: that is
// how a client skips re-parsing, and the fee that goes with it. A file the client referred to by id is not content and
// is left as it is. The request's body must already have passed Inspect.
func RewriteFiles(f Format, body []byte, parse func(File) (string, error)) ([]byte, []ParsedFile, error) {
	key, err := fileItemsKey(f)
	if err != nil {
		return nil, nil, err
	}
	root, err := requestMembers(body)
	if err != nil {
		return nil, nil, err
	}
	raw, ok := root[key]
	if !ok || raw.Kind() != '[' {
		return body, nil, nil // no items: a Responses prompt may be one string
	}
	items, err := requestArray(raw)
	if err != nil {
		return nil, nil, err
	}
	replayed := map[string]string{}
	for _, item := range items {
		readFileAnnotations(f, item, replayed)
	}
	w := &fileRewrite{f: f, parse: parse, replayed: replayed, texts: map[string]string{}}
	var changed bool
	for i, item := range items {
		rebuilt, ok, err := w.item(item)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		items[i], changed = rebuilt, true
	}
	if !changed {
		return body, nil, nil
	}
	root[key] = arrayValue(items) // as written: jsonValue would re-encode every item's numbers
	out, err := replaceRequestMember(body, key, root[key])
	if err != nil {
		return nil, nil, err
	}
	return out, w.files, nil
}

// fileRewrite is one request's files as they are replaced: each file is parsed once however often the history repeats it,
// and a text the request's own annotation supplied is used as it is.
type fileRewrite struct {
	f        Format
	parse    func(File) (string, error)
	replayed map[string]string // the annotations' texts, by hash
	texts    map[string]string // the texts this rewrite settled, by hash
	files    []ParsedFile
	written  int64 // bytes of text this rewrite has put in the body, every occurrence counted
}

// item rewrites one message or input item. ok is false when it carries no file.
func (w *fileRewrite) item(raw jsontext.Value) (jsontext.Value, bool, error) {
	item, parts, ok, err := itemParts(raw)
	if !ok || err != nil {
		return nil, false, err
	}
	rebuilt, replaced, err := w.parts(itemRole(item), parts)
	if !replaced || err != nil {
		return nil, false, err
	}
	// Only the content member is replaced: every other byte of the item, numbers included, is the client's own and
	// is copied as written. jsonValue would re-encode it, and the encoding cannot hold a number like 1e9999 that
	// jsontext accepts.
	content, err := replaceRequestMember(raw, "content", arrayValue(rebuilt))
	if err != nil {
		return nil, false, err
	}
	return jsontext.Value(content), true, nil
}

// arrayValue joins JSON values into an array, as they are written.
func arrayValue(elems []jsontext.Value) []byte {
	out := make([]byte, 0, 2+len(elems)*16)
	out = append(out, '[')
	for i, e := range elems {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, e...)
	}
	return append(out, ']')
}

// FileReferences counts the file parts that refer to a file by id rather than carrying its bytes: the references a
// rewrite leaves as they are, for routing to pin to one provider.
func FileReferences(f Format, body []byte) (int, error) {
	key, err := fileItemsKey(f)
	if err != nil {
		return 0, err
	}
	root, err := requestMembers(body)
	if err != nil {
		return 0, err
	}
	raw, ok := root[key]
	if !ok || raw.Kind() != '[' {
		return 0, nil
	}
	items, err := requestArray(raw)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, item := range items {
		refs, err := itemReferences(f, item)
		if err != nil {
			return 0, err
		}
		n += refs
	}
	return n, nil
}

// itemReferences counts the references among one item's content parts.
func itemReferences(f Format, item jsontext.Value) (int, error) {
	_, parts, ok, err := itemParts(item)
	if err != nil || !ok {
		return 0, err
	}
	n := 0
	for _, part := range parts {
		if fileReference(f, part) {
			n++
		}
	}
	return n, nil
}

// fileReference reports whether a part is a file the client referred to by id, which a rewrite leaves untouched.
func fileReference(f Format, raw jsontext.Value) bool {
	part, err := requestMembers(raw)
	if err != nil {
		return false // a part that is not an object is no reference
	}
	switch f {
	case Chat:
		file, err := requestMembers(part["file"])
		return err == nil && optionalString(part["type"]) == "file" && nestedReference(file)
	case Responses:
		return optionalString(part["type"]) == "input_file" && nestedReference(part)
	case Messages:
		source, err := requestMembers(part["source"])
		return err == nil && optionalString(part["type"]) == "document" &&
			optionalString(source["type"]) == "file" && !isNull(source["file_id"])
	case Embeddings, Rerank, Completions:
		return false // no content parts, so no references
	}
	return false
}

// nestedReference reports whether an object carries a file id and no data: a reference, not content.
func nestedReference(obj requestObject) bool {
	return isNull(obj["file_data"]) && !isNull(obj["file_id"])
}

// itemParts reads an item's content parts. ok is false for an item that carries none.
func itemParts(raw jsontext.Value) (requestObject, []jsontext.Value, bool, error) {
	item, err := requestMembers(raw)
	if err != nil {
		return nil, nil, false, nil //nolint:nilerr // a plain string item carries no parts
	}
	content, ok := item["content"]
	if !ok || content.Kind() != '[' {
		return item, nil, false, nil
	}
	parts, err := requestArray(content)
	if err != nil {
		return item, nil, false, err
	}
	return item, parts, true, nil
}

// parts replaces the files of one item, and reports whether it replaced any.
func (w *fileRewrite) parts(role string, parts []jsontext.Value) ([]jsontext.Value, bool, error) {
	rebuilt, cloned := parts, false
	for i, part := range parts {
		file, ok, err := filePart(w.f, part)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			continue
		}
		if !parsableRole(role) {
			return nil, false, fmt.Errorf("%w: a file outside user content cannot be parsed", ErrInvalid)
		}
		text, err := w.text(file)
		if err != nil {
			return nil, false, err
		}
		// A text that a repeated file reuses is written beside every one of its files, so each occurrence is counted
		// against the same budget: a history that repeats a parsed file cannot multiply it into a body no budget holds.
		w.written += int64(len(text))
		if w.written > MaxParsedBytes {
			return nil, false, fmt.Errorf("%w: the parsed text passes %d bytes", ErrFileTooLarge, MaxParsedBytes)
		}
		if !cloned {
			rebuilt, cloned = slices.Clone(parts), true
		}
		rebuilt[i] = parsedTextPart(w.f, text)
	}
	return rebuilt, cloned, nil
}

// optionalString reads a member that may be absent: a value of another kind is "" rather than an error, because the
// parts a rewrite reads are the ones the format conversion already accepted.
func optionalString(raw jsontext.Value) string {
	v, err := requestString(raw)
	if err != nil {
		return ""
	}
	return v
}

// text is the text that stands in file's place: the annotation's, the one an earlier part settled, or a fresh parse.
func (w *fileRewrite) text(file File) (string, error) {
	if text, known := w.texts[file.Hash]; known {
		return text, nil
	}
	if text, replayed := w.replayed[file.Hash]; replayed {
		w.texts[file.Hash] = text
		w.files = append(w.files, ParsedFile{File: file, Text: text})
		return text, nil
	}
	text, err := w.parse(file)
	if err != nil {
		return "", err
	}
	w.texts[file.Hash] = text
	w.files = append(w.files, ParsedFile{File: file, Text: text, Parsed: true})
	return text, nil
}

// parsableRole reports whether a file in an item of this role is content the request sends, rather than the model's own
// answer or a tool's result.
func parsableRole(role string) bool {
	return role == "" || role == "user" || role == "system" || role == "developer"
}

// fileItemsKey is the request member whose entries carry content parts.
func fileItemsKey(f Format) (string, error) {
	switch f {
	case Chat, Messages:
		return "messages", nil
	case Responses:
		return "input", nil
	case Completions, Embeddings, Rerank:
		return "", fmt.Errorf("%w: the file-parser plugin is not supported for this format", ErrInvalid)
	}
	return "", fmt.Errorf("%w: unknown format", ErrInvalid)
}

// itemRole is an item's role, or "" when it has none.
func itemRole(item requestObject) string {
	role, err := requestString(item["role"])
	if err != nil {
		return ""
	}
	return role
}

// filePart reads one content part that carries an inline file. ok is false for every other part.
func filePart(f Format, raw jsontext.Value) (File, bool, error) {
	part, err := requestMembers(raw)
	if err != nil {
		return File{}, false, nil //nolint:nilerr // a part that is not an object carries no file
	}
	kind := optionalString(part["type"])
	switch {
	case f == Chat && kind == "file":
		return nestedFile(part["file"])
	case f == Responses && kind == "input_file":
		return inlineFile(part["file_data"], part["filename"], part["file_id"])
	case f == Messages && kind == "document":
		return messagesFile(part)
	}
	return File{}, false, nil
}

// nestedFile reads Chat's file object.
func nestedFile(raw jsontext.Value) (File, bool, error) {
	if isNull(raw) {
		return File{}, false, nil
	}
	file, err := requestMembers(raw)
	if err != nil {
		return File{}, false, nil //nolint:nilerr // a file that is not an object has no inline data
	}
	return inlineFile(file["file_data"], file["filename"], file["file_id"])
}

// inlineFile reads a part's file_data, a data URL or bare base64, and its name. A part with an id and no data is the
// supplier's own file and stays as it is; one carrying both is refused, as the conversion refuses it.
func inlineFile(data, name, id jsontext.Value) (File, bool, error) {
	if isNull(data) {
		return File{}, false, nil
	}
	if !isNull(id) {
		return File{}, false, fmt.Errorf("%w: a file carrying both data and an id cannot be parsed", ErrInvalid)
	}
	raw, err := requestString(data)
	if err != nil {
		return File{}, false, err
	}
	label := optionalString(name)
	bytes, media, err := decodeFileData(raw)
	if err != nil {
		return File{}, false, err
	}
	return newFile(label, media, bytes), true, nil
}

// messagesFile reads a Messages document's base64 source and its title.
func messagesFile(part requestObject) (File, bool, error) {
	source, err := requestMembers(part["source"])
	if err != nil {
		return File{}, false, nil //nolint:nilerr // a URL or a text source carries nothing inline to parse
	}
	if optionalString(source["type"]) != "base64" {
		return File{}, false, nil
	}
	raw, err := requestString(source["data"])
	if err != nil {
		return File{}, false, err
	}
	bytes, media, err := decodeFileData(raw)
	if err != nil {
		return File{}, false, err
	}
	if declared, err := requestString(source["media_type"]); err == nil && media == "" {
		media = declared
	}
	name := optionalString(part["title"])
	return newFile(name, media, bytes), true, nil
}

// newFile hashes the bytes a file annotation names it by, and gives the file a name the parser picks its converter by:
// the client's own, with the media type's extension when it lacks one, or the media type's bare name.
func newFile(name, media string, data []byte) File {
	if name == "" {
		name = "document" + fileExt(media)
	} else if ext := fileExt(media); ext != "" && !strings.HasSuffix(strings.ToLower(name), ext) {
		name += ext
	}
	name = strings.NewReplacer("/", "_", "\\", "_").Replace(name)
	if len(name) > 120 {
		name = name[:120]
	}
	sum := sha256.Sum256(data)
	return File{Name: name, Data: data, Hash: hex.EncodeToString(sum[:])}
}

// fileExt is the extension a media type names. The parser's converters are picked by extension, and a document part
// need not carry a title.
func fileExt(media string) string {
	switch media {
	case "application/pdf":
		return ".pdf"
	case "text/html":
		return ".html"
	case "text/markdown", "text/x-markdown":
		return ".md"
	case "text/plain":
		return ".txt"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	}
	return ""
}

// decodeFileData reads a data URL's base64 payload or bare base64, and the media type a data URL declares.
func decodeFileData(raw string) ([]byte, string, error) {
	raw, media := strings.TrimSpace(raw), ""
	if rest, ok := strings.CutPrefix(raw, "data:"); ok {
		meta, payload, found := strings.Cut(rest, ",")
		if !found || !strings.HasSuffix(meta, ";base64") {
			return nil, "", fmt.Errorf("%w: a file's data URL is not base64", ErrInvalid)
		}
		raw, media = payload, strings.TrimSuffix(meta, ";base64")
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil {
		return nil, "", fmt.Errorf("%w: a file is not base64", ErrInvalid)
	}
	if len(data) > MaxFileBytes {
		return nil, "", fmt.Errorf("%w: a file is %d bytes, more than %d", ErrFileTooLarge, len(data), MaxFileBytes)
	}
	return data, media, nil
}

// parsedTextPart is the part that stands in a file's place.
func parsedTextPart(f Format, text string) jsontext.Value {
	kind := "text"
	if f == Responses {
		kind = contentNames[Responses]["text"]
	}
	return jsonValue(map[string]any{"type": kind, "text": text})
}
