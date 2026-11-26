package main

import (
	"encoding/json/v2" //nolint:depguard // decodes the node's own responses and what it sent; jsonx guards input from outside
	"fmt"
	"slices"
	"testing"
)

// How each format of TestToolCallsRoundTrip carries function calls in a streamed event, a whole answer and a request.

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func chatToolEvent(s *toolStream, data []byte) error {
	var e struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					Index    int          `json:"index"`
					ID       string       `json:"id"`
					Function chatFunction `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return err
	}
	for _, c := range e.Choices {
		for _, d := range c.Delta.ToolCalls {
			call := s.at(d.Index)
			call.id += d.ID
			call.name += d.Function.Name
			call.args += d.Function.Arguments
		}
		if c.FinishReason != nil && *c.FinishReason != "" {
			s.stop = *c.FinishReason
		}
	}
	return nil
}

func chatToolAnswer(body []byte) (calls []fnCall, stop string, err error) {
	var b struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string       `json:"id"`
					Function chatFunction `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, "", err
	}
	if len(b.Choices) != 1 {
		return nil, "", fmt.Errorf("%d choices, want one", len(b.Choices))
	}
	for _, c := range b.Choices[0].Message.ToolCalls {
		calls = append(calls, fnCall{c.ID, c.Function.Name, canonicalJSON(c.Function.Arguments)})
	}
	return calls, b.Choices[0].FinishReason, nil
}

// responsesItem is an output or input item of Responses.
type responsesItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Output    any    `json:"output"`
}

func responsesToolEvent(s *toolStream, data []byte) error {
	var e struct {
		Type     string        `json:"type"`
		Item     responsesItem `json:"item"`
		Response struct {
			Status string `json:"status"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return err
	}
	switch e.Type {
	case "response.output_item.done":
		if e.Item.Type == "function_call" {
			*s.at(len(s.order)) = fnCall{e.Item.CallID, e.Item.Name, e.Item.Arguments}
		}
	case "response.completed", "response.incomplete", "response.failed":
		s.stop = e.Response.Status
	}
	return nil
}

func responsesToolAnswer(body []byte) (calls []fnCall, stop string, err error) {
	var b struct {
		Status string          `json:"status"`
		Output []responsesItem `json:"output"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, "", err
	}
	for _, o := range b.Output {
		if o.Type == "function_call" {
			calls = append(calls, fnCall{o.CallID, o.Name, canonicalJSON(o.Arguments)})
		}
	}
	return calls, b.Status, nil
}

// messagesBlock is a content block of Messages, of an answer or a request.
type messagesBlock struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
}

func messagesToolEvent(s *toolStream, data []byte) error {
	var e struct {
		Type  string        `json:"type"`
		Index int           `json:"index"`
		Block messagesBlock `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return err
	}
	switch {
	case e.Type == "content_block_start" && e.Block.Type == "tool_use":
		*s.at(e.Index) = fnCall{id: e.Block.ID, name: e.Block.Name}
	case e.Type == "content_block_delta" && e.Delta.Type == "input_json_delta":
		s.at(e.Index).args += e.Delta.PartialJSON
	case e.Type == "message_delta":
		s.stop = e.Delta.StopReason
	}
	return nil
}

func messagesToolAnswer(body []byte) (calls []fnCall, stop string, err error) {
	var b struct {
		StopReason string          `json:"stop_reason"`
		Content    []messagesBlock `json:"content"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, "", err
	}
	for _, c := range b.Content {
		if c.Type == "tool_use" {
			calls = append(calls, fnCall{c.ID, c.Name, canonicalArgs(c.Input)})
		}
	}
	return calls, b.StopReason, nil
}

// The readers of what the upstream was sent return the calls, and the call ID of each result in the order sent, and fail
// the test unless each sits where the format puts it.

func decodeSent(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("the upstream was sent no JSON: %v: %s", err, body)
	}
}

// sentWhere fails the test unless ok, the upstream having been sent what the description says.
func sentWhere(t *testing.T, ok bool, what string, body []byte) {
	t.Helper()
	if !ok {
		t.Errorf("the upstream was sent %s: %s", what, body)
	}
}

// chatToolsSent: calls on an assistant message, followed by a tool message for each.
func chatToolsSent(t *testing.T, body []byte) (calls []fnCall, results []string) {
	t.Helper()
	var b struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string       `json:"id"`
				Type     string       `json:"type"`
				Function chatFunction `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	decodeSent(t, body, &b)
	for _, m := range b.Messages {
		// An array of content parts has at least one: OpenAI refuses an empty one, which a turn of calls alone is not.
		parts, isArray := m.Content.([]any)
		sentWhere(t, !isArray || len(parts) > 0, "an empty content array", body)
		for _, c := range m.ToolCalls {
			calls = append(calls, fnCall{c.ID, c.Function.Name, canonicalJSON(c.Function.Arguments)})
			sentWhere(t, m.Role == "assistant" && c.Type == "function", "a tool call that is not an assistant's function call", body)
		}
		if m.Role == "tool" {
			results = append(results, m.ToolCallID)
			sentWhere(t, len(calls) > 0, "a tool result before any call", body)
		}
	}
	return calls, results
}

// responsesToolsSent: call items before their outputs.
func responsesToolsSent(t *testing.T, body []byte) (calls []fnCall, results []string) {
	t.Helper()
	var b struct {
		Input []responsesItem `json:"input"`
	}
	decodeSent(t, body, &b)
	for _, item := range b.Input {
		switch item.Type {
		case "function_call":
			calls = append(calls, fnCall{item.CallID, item.Name, canonicalJSON(item.Arguments)})
		case "function_call_output":
			results = append(results, item.CallID)
			called := slices.ContainsFunc(calls, func(c fnCall) bool { return c.id == item.CallID })
			sentWhere(t, item.Output != nil && called, "a function call output with no output or before its call", body)
		}
	}
	return calls, results
}

// messagesToolsSent: tool uses in an assistant turn, their results in the user turn after it.
func messagesToolsSent(t *testing.T, body []byte) (calls []fnCall, results []string) {
	t.Helper()
	var b struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"` // a string or blocks
		} `json:"messages"`
	}
	decodeSent(t, body, &b)
	for i, m := range b.Messages {
		answered := i+1 < len(b.Messages) && b.Messages[i+1].Role == "user"
		for _, block := range messagesBlocks(t, m.Content) {
			switch block.Type {
			case "tool_use":
				calls = append(calls, fnCall{block.ID, block.Name, canonicalArgs(block.Input)})
				sentWhere(t, m.Role == "assistant" && answered, "a tool use outside an assistant turn followed by a user one", body)
			case "tool_result":
				results = append(results, block.ToolUseID)
				sentWhere(t, m.Role == "user", "a tool result outside a user turn", body)
			}
		}
	}
	return calls, results
}

// messagesBlocks are the blocks of a turn's content; a turn of text alone is a string, which has none.
func messagesBlocks(t *testing.T, content any) []messagesBlock {
	t.Helper()
	if _, ok := content.([]any); !ok {
		return nil
	}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []messagesBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("the upstream was sent content that is no blocks: %v: %s", err, raw)
	}
	return blocks
}
