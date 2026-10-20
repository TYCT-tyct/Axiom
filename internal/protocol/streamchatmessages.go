package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"fmt"
)

func (s *chatToNative) messagesDelta(i int) error {
	it := &s.items[i]
	if it.closed {
		if it.sent != len(it.text) {
			return fmt.Errorf("%w: content resumed after a Messages block closed", ErrUpstream)
		}
		return nil
	}
	for s.messageNext < i {
		j := s.messageNext
		prev := &s.items[j]
		if prev.kind == "tool_use" && !s.finished {
			return nil // parallel tools cannot overlap in Messages; keep their pieces until the first ends
		}
		if err := s.flushMessageBlock(j); err != nil {
			return err
		}
		if err := s.closeMessageBlock(j); err != nil {
			return err
		}
	}
	return s.flushMessageBlock(i)
}

func (s *chatToNative) flushMessageBlock(i int) error {
	it := &s.items[i]
	if !it.started {
		if err := s.emit("content_block_start", requestObject{"index": jsonValue(i), "content_block": jsonValue(messageBlock(it))}); err != nil {
			return err
		}
		it.started = true
	}
	if it.sent == len(it.text) {
		return nil
	}
	kind, member := "text_delta", "text"
	switch it.kind {
	case "thinking":
		kind, member = "thinking_delta", "thinking"
	case "tool_use":
		kind, member = "input_json_delta", "partial_json"
	}
	delta := requestObject{"type": jsonValue(kind), member: quotedContents(it.text[it.sent:])}
	if err := s.emit("content_block_delta", requestObject{"index": jsonValue(i), "delta": jsonValue(delta)}); err != nil {
		return err
	}
	it.sent = len(it.text)
	return nil
}

func messageBlock(it *chatOutput) requestObject {
	block := requestObject{"type": jsonValue(it.kind)}
	switch it.kind {
	case "text", "refusal":
		block["type"], block["text"] = jsonValue("text"), jsonValue("")
	case "thinking":
		block["thinking"] = jsonValue("")
	case "tool_use":
		block["id"], block["name"], block["input"] = jsonValue(toolUseID(it.id)), jsonValue(it.name), jsontext.Value("{}")
	}
	return block
}

func (s *chatToNative) closeMessageBlock(i int) error {
	if s.items[i].closed {
		return nil
	}
	if err := s.emit("content_block_stop", requestObject{"index": jsonValue(i)}); err != nil {
		return err
	}
	s.items[i].closed = true
	s.messageNext = i + 1
	return nil
}

func quotedContents(v []byte) jsontext.Value {
	out := make([]byte, 1, len(v)+2)
	out[0] = '"'
	return append(append(out, v...), '"')
}
