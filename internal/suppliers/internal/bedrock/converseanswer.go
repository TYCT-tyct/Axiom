package bedrock

import (
	"bufio"
	"bytes"
	"cmp"
	"errors"
	"io"
	"strings"

	"axiom/internal/platform/jsonx"
)

// Converse's answer back in Chat (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html,
// API_runtime_ConverseStream.html): the whole answer when it is read, and a stream's events each as the chunk that says
// the same as it is read. Chat's prompt_tokens counts the cached and cache-written tokens that Converse's inputTokens
// leaves out.

// maxAnswer bounds a whole answer: text and tool calls of the largest output a model on Bedrock gives are some MiB.
const maxAnswer = 16 << 20

// errAnswer is Converse's whole answer not read as one.
var errAnswer = errors.New("bedrock: Converse's answer does not read")

// chatHead is what a Chat answer and every chunk of a Chat stream begin with.
type chatHead struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
}

// chatAnswer is a Chat answer, or a chunk of a Chat stream.
type chatAnswer struct {
	Head    chatHead     `json:",embed"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitzero"`
}

type chatChoice struct {
	Index        int        `json:"index"`
	Message      *chatReply `json:"message,omitzero"`
	Delta        *chatReply `json:"delta,omitzero"`
	FinishReason *string    `json:"finish_reason"`
}

type chatReply struct {
	Role             string          `json:"role,omitzero"`
	Content          *string         `json:"content,omitzero"`
	ReasoningContent string          `json:"reasoning_content,omitzero"`
	ToolCalls        []chatReplyCall `json:"tool_calls,omitzero"`
}

// chatReplyCall is a tool call; Index is a stream's.
type chatReplyCall struct {
	Index    *int   `json:"index,omitzero"`
	ID       string `json:"id,omitzero"`
	Type     string `json:"type,omitzero"`
	Function struct {
		Name      string `json:"name,omitzero"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens     int64 `json:"cached_tokens"`
		CacheWriteTokens int64 `json:"cache_write_tokens,omitzero"`
	} `json:"prompt_tokens_details"`
}

type converseUsage struct {
	InputTokens           int64 `json:"inputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	CacheReadInputTokens  int64 `json:"cacheReadInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
}

// chat is the usage as Chat reports it.
func (u converseUsage) chat() *chatUsage {
	prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheWriteInputTokens
	c := &chatUsage{PromptTokens: prompt, CompletionTokens: u.OutputTokens, TotalTokens: prompt + u.OutputTokens}
	c.PromptTokensDetails.CachedTokens, c.PromptTokensDetails.CacheWriteTokens = u.CacheReadInputTokens, u.CacheWriteInputTokens
	return c
}

// finishReasons are Chat's finish reasons by Converse's stop reason. Another reason ends the turn as end_turn does.
var finishReasons = map[string]string{
	"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length", "model_context_window_exceeded": "length",
	"tool_use": "tool_calls", "guardrail_intervened": "content_filter", "content_filtered": "content_filter",
}

func finishOf(stopReason string) *string {
	r := cmp.Or(finishReasons[stopReason], "stop")
	return &r
}

// converseAnswer is Converse's whole answer, as far as it is read.
type converseAnswer struct {
	Output struct {
		Message struct {
			Content []block `json:"content"`
		} `json:"message"`
	} `json:"output"`
	StopReason string        `json:"stopReason"`
	Usage      converseUsage `json:"usage"`
}

// chatOf is Chat's answer of Converse's: its text blocks the content, its reasoning text the reasoning_content and
// its tool uses the tool calls.
func chatOf(body []byte, head chatHead) ([]byte, error) {
	var a converseAnswer
	if jsonx.Unmarshal(body, &a) != nil {
		return nil, errAnswer
	}
	reply := &chatReply{Role: "assistant"}
	var text, reasoning strings.Builder
	for _, b := range a.Output.Message.Content {
		switch {
		case b.Text != nil:
			text.WriteString(*b.Text)
			reply.Content = new(string)
		case b.ToolUse != nil:
			call := chatReplyCall{ID: b.ToolUse.ToolUseID, Type: "function"}
			call.Function.Name, call.Function.Arguments = b.ToolUse.Name, cmp.Or(string(b.ToolUse.Input), "{}")
			reply.ToolCalls = append(reply.ToolCalls, call)
		case b.ReasoningContent != nil && b.ReasoningContent.ReasoningText != nil:
			reasoning.WriteString(b.ReasoningContent.ReasoningText.Text)
		}
	}
	if reply.Content != nil {
		*reply.Content = text.String()
	}
	reply.ReasoningContent = reasoning.String()
	return jsonx.MarshalCanonical(chatAnswer{
		Head: head, Choices: []chatChoice{{Message: reply, FinishReason: finishOf(a.StopReason)}}, Usage: a.Usage.chat(),
	})
}

// chatAnswerBody reads Converse's whole answer as Chat's on the first Read; an answer that does not read is that Read's
// error, as a body cut short would be.
type chatAnswerBody struct {
	src  io.ReadCloser
	head chatHead
	out  *bytes.Reader
	err  error
}

func newChatAnswer(src io.ReadCloser, head chatHead) io.ReadCloser {
	return &chatAnswerBody{src: src, head: head}
}

// Read implements io.Reader.
func (b *chatAnswerBody) Read(p []byte) (int, error) {
	if b.out == nil && b.err == nil {
		b.out, b.err = b.convert()
	}
	if b.err != nil {
		return 0, b.err
	}
	return b.out.Read(p)
}

func (b *chatAnswerBody) convert() (*bytes.Reader, error) {
	body, err := io.ReadAll(io.LimitReader(b.src, maxAnswer+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxAnswer {
		return nil, errAnswer
	}
	chat, err := chatOf(body, b.head)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(chat), nil
}

// Close implements io.Closer.
func (b *chatAnswerBody) Close() error { return b.src.Close() }

// chatEvents writes ConverseStream's events as Chat's SSE: a chunk for each event that says something Chat can, the
// usage in a chunk of its own, and [DONE] after a stream whose message stopped. tools are the stream's tool calls'
// indexes by their content blocks'.
type chatEvents struct {
	head    chatHead
	tools   map[int]int
	stopped bool
}

// newChatSSE reads ConverseStream's event stream as Chat's SSE.
func newChatSSE(src io.ReadCloser, head chatHead) *sse {
	head.Object = "chat.completion.chunk"
	return &sse{src: src, r: bufio.NewReader(src), events: &chatEvents{head: head, tools: map[int]int{}}}
}

// converseEvent is one of ConverseStream's events, as far as it is read: which members it has, its type says.
type converseEvent struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
	Start             struct {
		ToolUse *struct {
			ToolUseID string `json:"toolUseId"`
			Name      string `json:"name"`
		} `json:"toolUse"`
	} `json:"start"`
	Delta struct {
		Text    *string `json:"text"`
		ToolUse *struct {
			Input string `json:"input"`
		} `json:"toolUse"`
		ReasoningContent *struct {
			Text *string `json:"text"`
		} `json:"reasoningContent"`
	} `json:"delta"`
	StopReason string         `json:"stopReason"`
	Usage      *converseUsage `json:"usage"`
}

func (c *chatEvents) event(eventType string, payload []byte, out *bytes.Buffer) error {
	var e converseEvent
	if jsonx.Unmarshal(payload, &e) != nil {
		return errFraming
	}
	choice := chatChoice{Delta: &chatReply{}}
	switch eventType {
	case "messageStart":
		choice.Delta.Role = "assistant"
	case "contentBlockStart":
		if e.Start.ToolUse == nil {
			return nil
		}
		i := len(c.tools)
		c.tools[e.ContentBlockIndex] = i
		call := chatReplyCall{Index: &i, ID: e.Start.ToolUse.ToolUseID, Type: "function"}
		call.Function.Name = e.Start.ToolUse.Name
		choice.Delta.ToolCalls = []chatReplyCall{call}
	case "contentBlockDelta":
		delta, err := c.delta(e)
		if delta == nil {
			return err
		}
		choice.Delta = delta
	case "messageStop":
		choice.FinishReason = finishOf(e.StopReason)
		c.stopped = true
	case "metadata":
		if e.Usage == nil {
			return nil
		}
		return c.write(out, chatAnswer{Head: c.head, Usage: e.Usage.chat()})
	default:
		return nil // a content block's stop, which Chat does not mark
	}
	return c.write(out, chatAnswer{Head: c.head, Choices: []chatChoice{choice}})
}

// delta is the chunk's delta for a content block's: its text, a piece of a tool call's input or of the reasoning; nil
// for one Chat has no member for (a reasoning signature, a citation), and with an error for the input of a tool call
// that never started.
func (c *chatEvents) delta(e converseEvent) (*chatReply, error) {
	d := e.Delta
	switch {
	case d.Text != nil:
		return &chatReply{Content: d.Text}, nil
	case d.ToolUse != nil:
		i, started := c.tools[e.ContentBlockIndex]
		if !started {
			return nil, errFraming
		}
		call := chatReplyCall{Index: &i}
		call.Function.Arguments = d.ToolUse.Input
		return &chatReply{ToolCalls: []chatReplyCall{call}}, nil
	case d.ReasoningContent != nil && d.ReasoningContent.Text != nil:
		return &chatReply{ReasoningContent: *d.ReasoningContent.Text}, nil
	}
	return nil, nil
}

func (*chatEvents) write(out *bytes.Buffer, chunk chatAnswer) error {
	b, err := jsonx.MarshalCanonical(chunk)
	if err != nil {
		return errFraming
	}
	out.WriteString("data: ")
	out.Write(b)
	out.WriteString("\n\n")
	return nil
}

// failed writes a chunk with Chat's top-level error.
func (*chatEvents) failed(exception string, out *bytes.Buffer) {
	out.WriteString("data: {" + errorMember(exception) + "}\n\n")
}

// end writes [DONE] after a message that stopped; a stream cut before that ends without it, which the gateway reads as
// broken.
func (c *chatEvents) end(out *bytes.Buffer) {
	if c.stopped {
		out.WriteString("data: [DONE]\n\n")
	}
}
