package protocol

import (
	"encoding/hex"
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
)

const hexDigits = "0123456789abcdef"

// blocks writes a message's content parts as content blocks, comma-separated; text alone when textOnly. Empty text
// writes nothing, as Anthropic refuses an empty text block.
func (m *messagesWriter) blocks(msg canonMessage, textOnly bool) error {
	start := len(m.o.buf)
	for _, p := range msg.Parts {
		if (p.Type == "text" || p.Type == "refusal") && (string(p.Text) == `""` || isNull(p.Text)) {
			continue
		}
		m.o.commaAfter(start)
		if err := m.block(p, textOnly); err != nil {
			return err
		}
		if m.o.err != nil {
			return m.o.err
		}
	}
	return nil
}

func (m *messagesWriter) block(p canonPart, textOnly bool) error {
	o := m.o
	switch {
	case p.Type == "text", p.Type == "refusal": // a refusal is text the assistant said: Anthropic's own come as text
		o.str(messagesTextHead).raw(p.Text)
	case textOnly:
		return &NotSupported{Reason: "a " + p.Type + " part where Messages takes text only"}
	case p.Type == "image_url" && p.ImageURL != nil:
		if err := imageBlock(o, p); err != nil {
			return err
		}
	case p.Type == "file" && p.File != nil && p.File.FileData != nil:
		if err := documentBlock(o, *p.File.FileData, p.File.Filename); err != nil {
			return err
		}
	default:
		return &NotSupported{Reason: "a " + p.Type + " part has no Messages counterpart"}
	}
	if !isNull(p.CacheControl) {
		o.str(`,"cache_control":`).raw(p.CacheControl)
	}
	o.byte('}')
	return nil
}

// imageBlock writes an image block but its closing brace: base64 for a data URL, a URL otherwise; Anthropic has no
// low detail.
func imageBlock(o *writer, p canonPart) error {
	if p.ImageURL.Detail == "low" {
		return &NotSupported{Reason: "image detail low has no Messages counterpart"}
	}
	o.str(messagesImageHead)
	if media, data, ok := dataURL(p.ImageURL.URL); ok {
		o.str(`{"type":"base64","media_type":`).quoted(media).str(`,"data":`).quoted(data)
	} else {
		o.str(`{"type":"url","url":`).quoted(p.ImageURL.URL)
	}
	o.byte('}')
	return nil
}

// documentBlock writes a PDF's data URL as a document block but its closing brace. Chat calls the file's name what
// Anthropic calls the document's title.
func documentBlock(o *writer, fileData string, filename jsontext.Value) error {
	media, data, ok := dataURL(fileData)
	if !ok || media != "application/pdf" {
		return &NotSupported{Reason: "a file that is not PDF data"}
	}
	o.str(messagesFileHead).quoted(data).byte('}')
	if !isNull(filename) {
		o.str(`,"title":`).raw(filename)
	}
	return nil
}

// toolUseID writes a tool call ID as Anthropic accepts it (toolUseID), in place.
func (w *writer) toolUseID(id string) *writer {
	if validToolUseID(id) {
		return w.quoted(id)
	}
	if w.room(len(`"axid_"`) + hex.EncodedLen(len(id))) {
		w.buf = append(w.buf, `"axid_`...)
		for i := range len(id) { // as hex.AppendEncode does, without copying id to a byte slice
			w.buf = append(w.buf, hexDigits[id[i]>>4], hexDigits[id[i]&0x0f])
		}
		w.buf = append(w.buf, '"')
	}
	return w
}
