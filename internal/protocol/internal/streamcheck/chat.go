package streamcheck

import (
	"io"

	"axiom/internal/protocol/internal/sse"
)

// Chat checks an OpenAI Chat Completions stream against OpenAI's "Chat Completions streaming events" and the
// stream_options of "Create chat completion" (read 2026-10-14): every chunk is a chat.completion.chunk with the same
// id; a choice gets no delta after its finish_reason; a tool call's first delta, the one that raises its index by
// one, carries its id and function name; the usage chunk, when asked for, comes after every choice has finished and
// has no choices; [DONE] ends the stream once every choice has finished. A chunk with a top-level error, the way
// OpenRouter reports a failure after the stream started ("Errors", read 2026-10-14), ends it too, and only [DONE]
// may follow.
func Chat(src io.Reader) error { return check(src, &chat{choices: map[int64]*choice{}}) }

type chat struct {
	id                   string
	choices              map[int64]*choice
	usage, failed, ended bool
}

type choice struct {
	finished bool
	tools    int // tool calls started
}

type chatChunk struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Choices []chatChoice `json:"choices"`
	Usage   *struct{}    `json:"usage"` // nil when absent or null
	Error   *struct{}    `json:"error"`
}

type chatChoice struct {
	Index *int64 `json:"index"`
	Delta struct {
		ToolCalls []struct {
			Index    *int64 `json:"index"`
			ID       string `json:"id"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

func (c *chat) event(data []byte) error {
	switch {
	case c.ended:
		return invalid("an event after [DONE]")
	case string(data) == sse.Done:
		return c.done()
	case c.failed || c.usage:
		return invalid("a chunk after the error or usage chunk")
	}
	var k chatChunk
	if err := decode(data, &k); err != nil {
		return err
	}
	return c.chunk(&k)
}

func (c *chat) chunk(k *chatChunk) error {
	switch {
	case k.Error != nil:
		c.failed = true
		return nil
	case k.Object != "chat.completion.chunk" || k.ID == "" || c.id != "" && k.ID != c.id:
		return invalid("chunk object %q or id %q is not that of the stream's chat.completion.chunk", k.Object, k.ID)
	}
	c.id = k.ID
	if k.Usage != nil {
		return c.usageChunk(len(k.Choices))
	}
	for i := range k.Choices {
		if err := c.choice(&k.Choices[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *chat) choice(d *chatChoice) error {
	if d.Index == nil {
		return invalid("a choice without an index")
	}
	ch := c.choices[*d.Index]
	if ch == nil {
		ch = &choice{}
		c.choices[*d.Index] = ch
	}
	if ch.finished {
		return invalid("choice %d continues after its finish_reason", *d.Index)
	}
	for _, call := range d.Delta.ToolCalls {
		_, continues := at(call.Index, ch.tools)
		switch {
		case continues:
		case next(call.Index, ch.tools) && call.ID != "" && call.Function.Name != "":
			ch.tools++
		default:
			return invalid("choice %d: a tool call delta that neither continues a call nor starts call %d with its id and name", *d.Index, ch.tools)
		}
	}
	ch.finished = d.FinishReason != nil
	return nil
}

func (c *chat) usageChunk(choices int) error {
	if choices > 0 || !c.allFinished() {
		return invalid("a usage chunk with choices or before every choice finished")
	}
	c.usage = true
	return nil
}

func (c *chat) done() error {
	if !c.failed && !c.allFinished() {
		return invalid("[DONE] before every choice finished")
	}
	c.ended = true
	return nil
}

func (c *chat) allFinished() bool {
	for _, ch := range c.choices {
		if !ch.finished {
			return false
		}
	}
	return true
}

func (c *chat) end() error {
	if !c.ended {
		return invalid("the stream ends before [DONE]")
	}
	return nil
}
