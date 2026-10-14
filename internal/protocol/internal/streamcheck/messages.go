package streamcheck

import (
	"io"
	"slices"
)

// Messages checks an Anthropic Messages stream against the event flow in Anthropic's "Streaming messages" (read
// 2026-10-14): message_start; content blocks one after another, each a content_block_start, its deltas and a
// content_block_stop, with indices counting up from 0; one or more message_delta; message_stop. ping may come at
// any point, and an error event ends the stream. A block may have no delta: a fallback block never has one.
func Messages(src io.Reader) error { return check(src, &messages{}) }

type messages struct {
	started, ended bool
	blocks         int    // blocks started so far
	open           bool   // the last block started has not stopped
	block          string // the type of the last block started
	deltas         int    // message_delta events
}

type messagesEvent struct {
	Type         string `json:"type"`
	Index        *int64 `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
	} `json:"content_block"`
	Delta struct {
		Type string `json:"type"`
	} `json:"delta"`
}

// blockDeltas lists the delta types each documented block type takes. Other block types, such as server tool
// results or ones added later, are not checked.
var blockDeltas = map[string][]string{
	"text":            {"text_delta", "citations_delta"},
	"thinking":        {"thinking_delta", "signature_delta"},
	"tool_use":        {"input_json_delta"},
	"server_tool_use": {"input_json_delta"},
}

var messagesEvents = map[string]func(*messages, *messagesEvent) error{
	"message_start": func(m *messages, _ *messagesEvent) error {
		if m.started {
			return invalid("a second message_start")
		}
		m.started = true
		return nil
	},
	"content_block_start": (*messages).blockStart,
	"content_block_delta": (*messages).blockDelta,
	"content_block_stop": func(m *messages, e *messagesEvent) error {
		if err := m.inOpenBlock(e); err != nil {
			return err
		}
		m.open = false
		return nil
	},
	"message_delta": func(m *messages, _ *messagesEvent) error {
		if m.open {
			return invalid("message_delta while block %d is open", m.blocks-1)
		}
		m.deltas++
		return nil
	},
	"message_stop": func(m *messages, _ *messagesEvent) error {
		if m.open || m.deltas == 0 {
			return invalid("message_stop before the last block stops or without a message_delta")
		}
		m.ended = true
		return nil
	},
	"error": func(m *messages, _ *messagesEvent) error {
		m.ended = true
		return nil
	},
}

func (m *messages) event(data []byte) error {
	if m.ended {
		return invalid("an event after the stream ended")
	}
	var e messagesEvent
	if err := decode(data, &e); err != nil {
		return err
	}
	if !m.started && e.Type != "message_start" && e.Type != "ping" && e.Type != "error" {
		return invalid("%q before message_start", e.Type)
	}
	if handle, ok := messagesEvents[e.Type]; ok {
		return handle(m, &e)
	}
	return nil
}

func (m *messages) blockStart(e *messagesEvent) error {
	switch {
	case m.open:
		return invalid("block %d starts while block %d is open", m.blocks, m.blocks-1)
	case !next(e.Index, m.blocks):
		return invalid("content_block_start without index %d", m.blocks)
	case e.ContentBlock.Type == "":
		return invalid("block %d has no type", m.blocks)
	}
	m.blocks++
	m.open = true
	m.block = e.ContentBlock.Type
	return nil
}

func (m *messages) blockDelta(e *messagesEvent) error {
	if err := m.inOpenBlock(e); err != nil {
		return err
	}
	if allowed, ok := blockDeltas[m.block]; ok && !slices.Contains(allowed, e.Delta.Type) {
		return invalid("%q in a %s block", e.Delta.Type, m.block)
	}
	return nil
}

func (m *messages) inOpenBlock(e *messagesEvent) error {
	if i, ok := at(e.Index, m.blocks); !ok || i != m.blocks-1 || !m.open {
		return invalid("%s for a block that is not open", e.Type)
	}
	return nil
}

func (m *messages) end() error {
	if !m.ended {
		return invalid("the stream ends before message_stop")
	}
	return nil
}
