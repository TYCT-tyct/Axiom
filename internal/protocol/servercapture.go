package protocol

import (
	"bytes"
	"encoding/json" //nolint:depguard // a whole event is decoded into a small typed value; the relay already read it
	"fmt"
)

// serverCapture records the calls of the gateway's web search tool as the client-format events carry them. It reads
// the events the relay is about to write, after conversion: one capture serves every pair of formats, and a call's
// arguments accumulate across chunks whatever shape the client's format gives them.
type serverCapture struct {
	format Format
	calls  []ServerToolCall
	index  map[int64]int // the format's call index to the entry in calls
	args   int
}

func newServerCapture(f Format) *serverCapture {
	return &serverCapture{format: f, index: make(map[int64]int)}
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
			ToolCalls []struct {
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
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta       json.RawMessage `json:"delta"`
	OutputIndex int64           `json:"output_index"`
	Item        struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	Arguments string `json:"arguments"`
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
			for _, call := range choice.Delta.ToolCalls {
				key := choice.Index<<32 | call.Index
				at, ok := c.index[key]
				switch {
				case call.Function.Name != "":
					if call.Function.Name != ServerToolName {
						delete(c.index, key)
						continue
					}
					if !ok {
						at = len(c.calls)
						c.index[key] = at
						c.calls = append(c.calls, ServerToolCall{})
					}
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
			if e.ContentBlock.Type != "tool_use" {
				return nil
			}
			if e.ContentBlock.Name != ServerToolName {
				delete(c.index, e.Index)
				return nil
			}
			c.index[e.Index] = len(c.calls)
			c.calls = append(c.calls, ServerToolCall{ID: e.ContentBlock.ID})
			if len(e.ContentBlock.Input) > 0 && string(e.ContentBlock.Input) != "{}" {
				c.calls[len(c.calls)-1].Arguments = string(e.ContentBlock.Input)
				c.args += len(e.ContentBlock.Input)
				return c.bound()
			}
		case "content_block_delta":
			at, ok := c.index[e.Index]
			if !ok {
				return nil
			}
			var d struct {
				PartialJSON string `json:"partial_json"`
			}
			if err := json.Unmarshal(e.Delta, &d); err != nil {
				return nil //nolint:nilerr // a delta this capture cannot read is left to the relay
			}
			return c.append(at, d.PartialJSON)
		}
	case Responses:
		switch e.Type {
		case "response.output_item.added":
			if e.Item.Type != "function_call" {
				return nil
			}
			if e.Item.Name != ServerToolName {
				delete(c.index, e.OutputIndex)
				return nil
			}
			c.index[e.OutputIndex] = len(c.calls)
			c.calls = append(c.calls, ServerToolCall{ID: e.Item.CallID, Arguments: e.Item.Arguments})
			c.args += len(e.Item.Arguments)
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
			at, ok := c.index[e.OutputIndex]
			if !ok {
				return nil
			}
			args := e.Arguments
			if e.Type == "response.output_item.done" {
				args = e.Item.Arguments
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
	return r.srv.calls
}
