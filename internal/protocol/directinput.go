package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in direct.go: the input is read element by element
	"fmt"
)

// embeddingInput reads OpenAI's input: a string, an array of strings, a token array, an array of token arrays, or an
// array of objects whose content holds text and media parts (OpenAI《Create embeddings》, OpenRouter《Embeddings》,
// read 2026-10-30).
//
//nolint:exhaustive // the default rejects every other JSON kind with the client's error
func (d *directScanner) embeddingInput() error {
	switch d.dec.PeekKind() {
	case '"':
		v, err := d.str()
		if err == nil {
			d.input(int64(len(v)))
		}
		return err
	case '[':
		v, err := d.dec.ReadValue()
		if err != nil {
			return err
		}
		return d.embeddingArray(v)
	default:
		return fmt.Errorf("input is %s, want a string or an array", kindNameOf(d.dec.PeekKind()))
	}
}

// embeddingArray reads the several forms an embedding input array takes. Each string, token array or object is one
// input of its own: a batch is charged for all of them, while its context limit holds each one separately.
func (d *directScanner) embeddingArray(v jsontext.Value) error {
	var tokens int64
	err := uniformArray(v, "input", func(kind jsontext.Kind, e jsontext.Value) error {
		switch kind { //nolint:exhaustive // the default rejects every other JSON kind with the client's error
		case '0':
			tokens++
		case '"':
			d.input(unquotedLen(e))
		case '[':
			_, err := d.tokenInput(e)
			return err
		case '{':
			n, err := d.embeddingObject(e)
			if err != nil {
				return err
			}
			d.input(n)
		default:
			return fmt.Errorf("input has an element of the wrong type")
		}
		return nil
	})
	if err == nil && tokens > 0 {
		d.input(tokens * 4)
		d.r.TokenInputs += tokens
	}
	return err
}

// arrayShapes name an input array's element kinds, for the error that mixes them.
var arrayShapes = [...]string{'0': "tokens", '"': "strings", '[': "token arrays", '{': "objects"}

// uniformArray reads a non-empty array whose elements are all of the first one's kind, giving each to read as
// eachArray does; what names the member in errors. read refuses a first kind the member does not take.
func uniformArray(v jsontext.Value, what string, read func(jsontext.Kind, jsontext.Value) error) error {
	var first jsontext.Kind
	err := eachArray(v, func(kind jsontext.Kind, e jsontext.Value) error {
		if first == 0 {
			first = kind
		}
		if kind != first {
			return fmt.Errorf("%s mixes %s with other values", what, arrayShapes[first])
		}
		return read(kind, e)
	})
	if err == nil && first == 0 {
		return fmt.Errorf("%s is an empty array", what)
	}
	return err
}

// tokenInput records a token array as one input of n tokens and returns n.
func (d *directScanner) tokenInput(v jsontext.Value) (int64, error) {
	n, err := tokenCount(v)
	if err != nil {
		return 0, err
	}
	d.input(n * 4)
	d.r.TokenInputs += n
	return n, nil
}

// tokens counts a token array: its tokens are exact, so the reservation is the bytes a token takes at the estimate's
// most (billing.InputBytesPerToken is 4), which never undercounts them.
func tokenCount(v jsontext.Value) (int64, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	if _, err := dec.ReadToken(); err != nil {
		return 0, err
	}
	var n int64
	for dec.PeekKind() != ']' {
		if dec.PeekKind() != '0' {
			return 0, fmt.Errorf("input mixes tokens with other values")
		}
		if err := dec.SkipValue(); err != nil {
			return 0, err
		}
		n++
	}
	if _, err := dec.ReadToken(); err != nil {
		return 0, err
	}
	return n, nil
}

// embeddingObject reads one {content: [...]} input object and its parts, returning the text bytes it holds. Its
// content is read element by element: a batch of a million numbers must not become a million slice headers.
func (d *directScanner) embeddingObject(v jsontext.Value) (int64, error) {
	content, ok, err := member(v, "content")
	if err != nil {
		return 0, err
	}
	if !ok || content.Kind() != '[' {
		return 0, fmt.Errorf("input object has no content array")
	}
	var text int64
	err = eachArray(content, func(kind jsontext.Kind, part jsontext.Value) error {
		if kind != '{' {
			return fmt.Errorf("content part is not an object")
		}
		n, err := d.contentPart(part)
		text += n
		return err
	})
	return text, err
}

// contentPart reads one content part of an input object, returning its text bytes.
func (d *directScanner) contentPart(part jsontext.Value) (int64, error) {
	typ, _, err := member(part, "type")
	if err != nil {
		return 0, err
	}
	switch string(typ) {
	case `"text"`:
		value, ok, err := member(part, "text")
		if err != nil {
			return 0, err
		}
		if !ok || value.Kind() != '"' {
			return 0, fmt.Errorf("text part has no text string")
		}
		return unquotedLen(value), nil
	case `"image_url"`:
		d.r.Images++
		d.r.Needs |= Images
	case `"file"`:
		d.r.Documents++
		d.r.Needs |= Documents
	case `"input_audio"`, `"input_video"`:
		// Their size is not text and the catalog has no separate media bound: a document's 4096 tokens is larger
		// than an image's 1600, so counting them as files keeps the reservation above what they can take.
		d.r.Documents++
		d.r.Needs |= Documents
	default:
		return 0, fmt.Errorf("content part type %s is not supported", typ)
	}
	return 0, nil
}

// documents reads rerank's documents: strings, or objects with text and/or image. Each document is one input; the
// context limit holds the query and one document, not the whole batch.
func (d *directScanner) documents() error {
	v, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	if v.Kind() != '[' {
		return fmt.Errorf("documents is %s, want an array", kindName(v))
	}
	seen := false
	err = eachArray(v, func(kind jsontext.Kind, e jsontext.Value) error {
		seen = true
		var n int64
		switch kind { //nolint:exhaustive // the default rejects every other JSON kind with the client's error
		case '"':
			n = unquotedLen(e)
			d.r.TextBytes += n
		case '{':
			var err error
			if n, err = d.documentObject(e); err != nil {
				return err
			}
		default:
			return fmt.Errorf("documents has an element of the wrong type")
		}
		d.maxDoc = max(d.maxDoc, n)
		return nil
	})
	if err == nil && !seen {
		return fmt.Errorf("documents is empty")
	}
	return err
}

// documentObject reads one {text, image} document, returning its text bytes.
func (d *directScanner) documentObject(e jsontext.Value) (int64, error) {
	text, hasText, err := member(e, "text")
	if err != nil {
		return 0, err
	}
	image, hasImage, err := member(e, "image")
	if err != nil {
		return 0, err
	}
	if !hasText && !hasImage {
		return 0, fmt.Errorf("a document has neither text nor image")
	}
	var n int64
	if hasText {
		if text.Kind() != '"' {
			return 0, fmt.Errorf("a document's text is not a string")
		}
		n = unquotedLen(text)
		d.r.TextBytes += n
	}
	if hasImage {
		if image.Kind() != '"' {
			return 0, fmt.Errorf("a document's image is not a string")
		}
		d.r.Images++
		d.r.Needs |= Images
	}
	return n, nil
}

// prompt reads legacy completions' prompt: a string, a token array, an array of strings, or an array of token arrays.
// A token array is one prompt, not one prompt per token; each prompt's length is kept for the context check.
//
//nolint:exhaustive // the default rejects every other JSON kind with the client's error
func (d *directScanner) prompt() error {
	switch d.dec.PeekKind() {
	case '"':
		v, err := d.str()
		if err == nil {
			d.input(int64(len(v)))
			d.promptBytes(int64(len(v)))
			d.prompts = 1
		}
		return err
	case '[':
		v, err := d.dec.ReadValue()
		if err != nil {
			return err
		}
		return d.promptArray(v)
	default:
		return fmt.Errorf("prompt is %s, want a string or an array", kindNameOf(d.dec.PeekKind()))
	}
}

// promptArray distinguishes a token prompt, which is one prompt, from a list of prompts, each of which generates.
func (d *directScanner) promptArray(v jsontext.Value) error {
	var tokens int64
	err := uniformArray(v, "prompt", func(kind jsontext.Kind, e jsontext.Value) error {
		switch kind { //nolint:exhaustive // the default rejects every other JSON kind with the client's error
		case '0':
			tokens++
		case '"':
			d.input(unquotedLen(e))
			d.promptBytes(unquotedLen(e))
			d.prompts++
		case '[':
			n, err := d.tokenInput(e)
			if err != nil {
				return err
			}
			d.promptBytes(n * 4)
			d.prompts++
		default:
			return fmt.Errorf("prompt has an element of the wrong type")
		}
		return nil
	})
	if err == nil && tokens > 0 {
		d.input(tokens * 4)
		d.r.TokenInputs += tokens
		d.promptBytes(tokens * 4)
		d.prompts = 1
	}
	return err
}

// eachArray reads an array's elements in order, without keeping them: a large input must not allocate a slice header
// per element.
func eachArray(v jsontext.Value, read func(jsontext.Kind, jsontext.Value) error) error {
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	for dec.PeekKind() != ']' {
		kind := dec.PeekKind()
		if kind == '0' {
			// A token array can hold a million numbers; skipping each one allocates nothing. A consumer that needs a
			// value (a string, an object or a nested token array) gets it.
			if err := read(kind, nil); err != nil {
				return err
			}
			if err := dec.SkipValue(); err != nil {
				return err
			}
			continue
		}
		e, err := dec.ReadValue()
		if err != nil {
			return err
		}
		if err := read(kind, e); err != nil {
			return err
		}
	}
	_, err := dec.ReadToken()
	return err
}

// member returns an object's named value, or false when it has none. The value aliases v: a caller uses it before
// the body it came from is released.
func member(v jsontext.Value, name string) (jsontext.Value, bool, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	if _, err := dec.ReadToken(); err != nil {
		return nil, false, err
	}
	for dec.PeekKind() != '}' {
		n, err := dec.ReadValue()
		if err != nil {
			return nil, false, err
		}
		key, err := jsontext.AppendUnquote(nil, n)
		if err != nil {
			return nil, false, err
		}
		value, err := dec.ReadValue()
		if err != nil {
			return nil, false, err
		}
		if string(key) == name {
			return value, true, nil
		}
	}
	return nil, false, nil
}

func kindName(v jsontext.Value) string { return kindNameOf(v.Kind()) }

//nolint:exhaustive // every unlisted kind means "not a value"
func kindNameOf(k jsontext.Kind) string {
	switch k {
	case '"':
		return "a string"
	case '0':
		return "a number"
	case '[':
		return "an array"
	case '{':
		return "an object"
	case 't', 'f':
		return "a boolean"
	case 'n':
		return "null"
	}
	return "not a value"
}
