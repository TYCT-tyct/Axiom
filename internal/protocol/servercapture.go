package protocol

import (
	"bytes"
	"encoding/json" //nolint:depguard // a whole event is decoded into a small typed value; the relay already read it
	"fmt"
)

// serverCapture records the assistant turn and the calls of the gateway's web search tool as the client-format events
// carry them. It reads the events the relay is about to write, after conversion: one capture serves every pair of
// formats, and a call's arguments accumulate across chunks whatever shape the client's format gives them.
type serverCapture struct {
	format    Format
	calls     []ServerToolCall
	index     map[int64]int // the format's call index to the entry in calls
	blocks    []capturedBlock
	blockAt   map[int64]int
	text      []byte
	reasoning []byte
	args      int
	client    bool
}

// ServerTurn is the assistant turn one loop hop produced, in the client's format, together with every call it made.
// The gateway appends the server calls' results after this turn, so text, thinking and signatures the model emitted
// stay in the conversation. Client is true when a call is one the client runs; a loop hop may not mix those with a
// server call.
type ServerTurn struct {
	Assistant []byte
	Calls     []ServerToolCall
	Client    bool
}

type capturedBlock struct {
	kind, text, thinking, signature, id, name string
	input                                     []byte
	raw                                       json.RawMessage
	client                                    bool
}

func newServerCapture(f Format) *serverCapture {
	return &serverCapture{format: f, index: make(map[int64]int), blockAt: make(map[int64]int)}
}

// maxServerCallArguments bounds one call's arguments. A search query is short; a model that writes more has produced
// something this gateway will not copy or run.
const maxServerCallArguments = 64 << 10

type serverEvent struct {
	Type    string `json:"type"`
	Index   int64  `json:"index"`
	Choices []struct {
		Index int64 `json:"index"`
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int64  `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	ContentBlock struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Text      string          `json:"text"`
	} `json:"content_block"`
	Delta       json.RawMessage `json:"delta"`
	OutputIndex int64           `json:"output_index"`
	ItemRaw     json.RawMessage `json:"item"`
	Arguments   string          `json:"arguments"`
}

// event reads one client-format event. An event it cannot decode is left to the relay, which will refuse it if it
// matters; only a matching call that grows past its bound fails here.
//
//nolint:cyclop,funlen,gocognit // one branch per format's event shape
func (c *serverCapture) event(data []byte) error {
	var e serverEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return nil //nolint:nilerr // an event this capture cannot read is left to the relay
	}
	//nolint:exhaustive // only the three chat formats carry tool calls
	switch c.format {
	case Chat:
		for _, choice := range e.Choices {
			if choice.Index == 0 {
				c.text = append(c.text, choice.Delta.Content...)
				if choice.Delta.ReasoningContent != "" {
					c.reasoning = append(c.reasoning, choice.Delta.ReasoningContent...)
				} else {
					c.reasoning = append(c.reasoning, choice.Delta.Reasoning...)
				}
			}
			for _, call := range choice.Delta.ToolCalls {
				key := choice.Index<<32 | call.Index
				at, ok := c.index[key]
				switch {
				case call.Function.Name != "":
					if !ok {
						at = len(c.calls)
						c.index[key] = at
						c.calls = append(c.calls, ServerToolCall{Name: call.Function.Name, Client: call.Function.Name != ServerToolName})
					}
					c.calls[at].Name = call.Function.Name
					c.calls[at].Client = call.Function.Name != ServerToolName
					c.client = c.client || c.calls[at].Client
					if call.ID != "" {
						c.calls[at].ID = call.ID
					}
				case !ok:
					continue
				}
				if call.Function.Arguments != "" {
					if err := c.append(at, call.Function.Arguments); err != nil {
						return err
					}
				}
			}
		}
	case Messages:
		switch e.Type {
		case "content_block_start":
			at := len(c.blocks)
			c.blockAt[e.Index] = at
			b := capturedBlock{
				kind: e.ContentBlock.Type, text: e.ContentBlock.Text, thinking: e.ContentBlock.Thinking,
				signature: e.ContentBlock.Signature, id: e.ContentBlock.ID, name: e.ContentBlock.Name,
			}
			if b.kind == "tool_use" {
				if len(e.ContentBlock.Input) > 0 && string(e.ContentBlock.Input) != "{}" {
					b.input = bytes.Clone(e.ContentBlock.Input)
				}
				b.client = b.name != ServerToolName
				c.client = c.client || b.client
				if !b.client {
					c.index[e.Index] = len(c.calls)
					c.calls = append(c.calls, ServerToolCall{ID: b.id, Name: b.name})
					if len(b.input) > 0 {
						c.calls[len(c.calls)-1].Arguments = string(b.input)
						c.args += len(b.input)
					}
				}
			}
			c.blocks = append(c.blocks, b)
			if b.kind == "tool_use" && len(b.input) > 0 {
				return c.bound()
			}
		case "content_block_delta":
			at, ok := c.blockAt[e.Index]
			if !ok {
				return nil
			}
			var d struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
			}
			if err := json.Unmarshal(e.Delta, &d); err != nil {
				return nil //nolint:nilerr // a delta this capture cannot read is left to the relay
			}
			switch d.Type {
			case "text_delta":
				c.blocks[at].text += d.Text
			case "thinking_delta":
				c.blocks[at].thinking += d.Thinking
			case "signature_delta":
				c.blocks[at].signature += d.Signature
			case "input_json_delta":
				if c.blocks[at].kind != "tool_use" {
					return nil
				}
				c.blocks[at].input = append(c.blocks[at].input, d.PartialJSON...)
				if call, ok := c.index[e.Index]; ok {
					return c.append(call, d.PartialJSON)
				}
			}
		}
	case Responses:
		switch e.Type {
		case "response.output_item.added":
			var item struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(e.ItemRaw, &item); err != nil {
				return nil //nolint:nilerr // an item this capture cannot read is left to the relay
			}
			at := len(c.blocks)
			c.blockAt[e.OutputIndex] = at
			c.blocks = append(c.blocks, capturedBlock{kind: item.Type, raw: bytes.Clone(e.ItemRaw)})
			if item.Type != "function_call" {
				return nil
			}
			c.client = c.client || item.Name != ServerToolName
			if item.Name != ServerToolName {
				return nil
			}
			c.index[e.OutputIndex] = len(c.calls)
			c.calls = append(c.calls, ServerToolCall{ID: item.CallID, Name: item.Name, Arguments: item.Arguments})
			c.args += len(item.Arguments)
			return c.bound()
		case "response.function_call_arguments.delta":
			at, ok := c.index[e.OutputIndex]
			if !ok {
				return nil
			}
			var fragment string
			if err := json.Unmarshal(e.Delta, &fragment); err != nil {
				return nil //nolint:nilerr // a delta this capture cannot read is left to the relay
			}
			return c.append(at, fragment)
		case "response.function_call_arguments.done", "response.output_item.done":
			if e.Type == "response.output_item.done" {
				if at, ok := c.blockAt[e.OutputIndex]; ok {
					c.blocks[at].raw = bytes.Clone(e.ItemRaw)
				}
			}
			at, ok := c.index[e.OutputIndex]
			if !ok {
				return nil
			}
			args := e.Arguments
			if e.Type == "response.output_item.done" {
				var item struct {
					Arguments string `json:"arguments"`
				}
				if err := json.Unmarshal(e.ItemRaw, &item); err != nil {
					return nil //nolint:nilerr // an item this capture cannot read is left to the relay
				}
				args = item.Arguments
			}
			if args == "" {
				return nil
			}
			c.args += len(args) - len(c.calls[at].Arguments)
			c.calls[at].Arguments = args
			return c.bound()
		}
	}
	return nil
}

// append adds an argument fragment to one call.
func (c *serverCapture) append(at int, fragment string) error {
	if fragment == "" {
		return nil
	}
	c.args += len(fragment)
	c.calls[at].Arguments += fragment
	return c.bound()
}

func (c *serverCapture) bound() error {
	if c.args > maxServerCallArguments {
		return fmt.Errorf("%w: a web search call's arguments pass %d bytes", ErrUpstream, maxServerCallArguments)
	}
	return nil
}

// turn builds the assistant turn the client format expects, so the next hop can replay it unchanged. Chat and Messages
// get one assistant message; Responses gets the array of output items. A block this capture did not understand keeps
// its raw start object rather than being dropped.
//
//nolint:gocognit // one branch per format and block kind
func (c *serverCapture) turn() ServerTurn {
	t := ServerTurn{Client: c.client, Calls: append([]ServerToolCall(nil), c.calls...)}
	//nolint:exhaustive // only the three chat formats carry tools
	switch c.format {
	case Chat:
		t.Assistant = append(t.Assistant, `{"role":"assistant","content":`...)
		if len(c.text) == 0 {
			t.Assistant = append(t.Assistant, "null"...)
		} else {
			quoted := jsonString(string(c.text))
			t.Assistant = append(t.Assistant, quoted...)
		}
		if len(c.reasoning) > 0 {
			quoted := jsonString(string(c.reasoning))
			t.Assistant = append(t.Assistant, `,"reasoning_content":`...)
			t.Assistant = append(t.Assistant, quoted...)
		}
		server := c.serverCalls()
		if len(server) > 0 {
			t.Assistant = append(t.Assistant, `,"tool_calls":`...)
			t.Assistant = append(t.Assistant, toolCallsJSON(server)...)
		}
		t.Assistant = append(t.Assistant, '}')
	case Messages:
		t.Assistant = append(t.Assistant, `{"role":"assistant","content":[`...)
		first := true
		for _, b := range c.blocks {
			if !first {
				t.Assistant = append(t.Assistant, ',')
			}
			first = false
			t.Assistant = append(t.Assistant, blockJSON(b)...)
		}
		t.Assistant = append(t.Assistant, `]}`...)
	case Responses:
		t.Assistant = append(t.Assistant, '[')
		for i, b := range c.blocks {
			if i > 0 {
				t.Assistant = append(t.Assistant, ',')
			}
			if len(b.raw) > 0 {
				t.Assistant = append(t.Assistant, b.raw...)
			} else {
				t.Assistant = append(t.Assistant, `{"type":"`+b.kind+`"}`...)
			}
		}
		t.Assistant = append(t.Assistant, ']')
	}
	return t
}

// jsonString quotes a Go string for a JSON body. A string always encodes; the empty fallback is unreachable and keeps
// the helper free of an error path at every call site.
func jsonString(s string) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		return []byte(`""`)
	}
	return b
}

func (c *serverCapture) serverCalls() []ServerToolCall {
	out := make([]ServerToolCall, 0, len(c.calls))
	for _, call := range c.calls {
		if !call.Client {
			out = append(out, call)
		}
	}
	return out
}

func toolCallsJSON(calls []ServerToolCall) []byte {
	out := []byte{'['}
	for i, call := range calls {
		if i > 0 {
			out = append(out, ',')
		}
		id := jsonString(call.ID)
		args := jsonString(call.Arguments)
		out = append(out, `{"id":`...)
		out = append(out, id...)
		out = append(out, `,"type":"function","function":{"name":`...)
		nameValue := call.Name
		if nameValue == "" {
			nameValue = ServerToolName
		}
		name := jsonString(nameValue)
		out = append(out, name...)
		out = append(out, `,"arguments":`...)
		out = append(out, args...)
		out = append(out, `}}`...)
	}
	return append(out, ']')
}

func blockJSON(b capturedBlock) []byte {
	switch b.kind {
	case "text":
		text := jsonString(b.text)
		return append(append([]byte(`{"type":"text","text":`), text...), '}')
	case "thinking":
		thinking := jsonString(b.thinking)
		out := append([]byte(`{"type":"thinking","thinking":`), thinking...)
		if b.signature != "" {
			sig := jsonString(b.signature)
			out = append(out, `,"signature":`...)
			out = append(out, sig...)
		}
		return append(out, '}')
	case "tool_use":
		input := b.input
		if len(input) == 0 {
			input = []byte("{}")
		}
		id := jsonString(b.id)
		name := jsonString(b.name)
		out := append([]byte(`{"type":"tool_use","id":`), id...)
		out = append(out, `,"name":`...)
		out = append(out, name...)
		out = append(out, `,"input":`...)
		out = append(out, input...)
		return append(out, '}')
	default:
		if len(b.raw) > 0 {
			return b.raw
		}
		return []byte(`{}`)
	}
}

// capture reads the client-format events a step carries, which a conversion may frame several of.
func (r *Relay) capture(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if bytes.TrimSpace(data)[0] == '{' {
		return r.srv.event(data)
	}
	for len(data) > 0 {
		ev, rest, err := r.unframe(data)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(ev)) > 0 {
			if err := r.srv.event(ev); err != nil {
				return err
			}
		}
		data = rest
	}
	return nil
}

// ServerCalls are the web search tool's calls the relay has read, with their arguments as the client-format events
// carried them. It is read after the upstream's end, before the relay is released.
func (r *Relay) ServerCalls() []ServerToolCall {
	if r.srv == nil {
		return nil
	}
	return r.srv.serverCalls()
}

// ServerTurn is the assistant turn behind ServerCalls, for replay in the loop's next request.
func (r *Relay) ServerTurn() ServerTurn {
	if r.srv == nil {
		return ServerTurn{}
	}
	return r.srv.turn()
}
