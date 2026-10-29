package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // preserve untouched request members and signatures as Inspect does
	"fmt"
	"unicode/utf8"

	"axiom/internal/platform/jsonx"
)

// CompressionMessageLimit is Anthropic's documented request limit, also enforced by OpenRouter middle-out.
// https://platform.claude.com/docs/en/api/messages/create (read 2026-10-29).
const CompressionMessageLimit = 100000

// CompressionCost bounds the temporary parsed messages and the replacement body. The gateway charges it before
// CompressContext; like cross-format conversion this deliberately pays only on the overflow path.
func CompressionCost(body []byte) int64 { return 2 * nativeRequestCost(body) }

// CompressContext removes middle conversation turns, then truncates plain text in retained messages if necessary.
// The input bound is bytes, including JSON overhead: no tokenizer or external service is needed in admission.
// The returned body is unchanged when it already fits. Callers must Inspect a changed body before using its summary.
func CompressContext(body []byte, context, output int64) ([]byte, error) {
	budget := context - output
	if budget <= 0 {
		return nil, shapeError("context-compression cannot fit the requested output in the context")
	}
	o, err := requestMembers(body)
	if err != nil {
		return nil, err
	}
	messages, err := requestArray(o["messages"])
	if err != nil {
		return nil, err
	}
	if int64(len(body)) <= budget && len(messages) <= CompressionMessageLimit {
		return body, nil
	}
	size, err := compressTurns(messages, int64(len(body)), budget)
	if err != nil {
		return nil, err
	}
	size, err = compressRetained(messages, size, budget)
	if err != nil {
		return nil, err
	}
	if size > budget {
		return nil, shapeError("context-compression cannot fit protected content in the context")
	}
	out := compressedMessages(messages, size)
	compressed, err := replaceRequestMember(body, "messages", out)
	if err != nil {
		return nil, err
	}
	if int64(len(compressed)) > budget {
		return nil, fmt.Errorf("%w: context-compression cannot fit the prompt", ErrInvalid)
	}
	return compressed, nil
}

func compressedMessages(messages []jsonx.Raw, size int64) []byte {
	out := make([]byte, 0, size)
	out = append(out, '[')
	for _, message := range messages {
		if message != nil {
			out = append(comma(out), message...)
		}
	}
	return append(out, ']')
}

func compressTurns(messages []jsonx.Raw, size, budget int64) (int64, error) {
	groups, err := compressionGroups(messages)
	if err != nil {
		return 0, err
	}
	count := len(messages)
	middle := len(groups) / 2
	for distance := range len(groups) {
		if size <= budget && count <= CompressionMessageLimit {
			break
		}
		i := middle + (distance+1)/2
		if distance%2 != 0 {
			i = middle - (distance+1)/2
		}
		if i <= 0 || i >= len(groups)-1 || groups[i].system {
			continue
		}
		for j := groups[i].from; j < groups[i].to; j++ {
			size -= int64(len(messages[j]) + 1)
			count--
			messages[j] = nil
		}
	}
	if count > CompressionMessageLimit {
		return 0, shapeError("context-compression cannot preserve the message limit")
	}
	return size, nil
}

func compressRetained(messages []jsonx.Raw, size, budget int64) (int64, error) {
	for i, message := range messages {
		if size <= budget {
			break
		}
		if message == nil {
			continue
		}
		shortened, err := compressText(message, size-budget)
		if err != nil {
			return 0, err
		}
		size -= int64(len(message) - len(shortened))
		messages[i] = shortened
	}
	return size, nil
}

type compressionGroup struct {
	from, to int
	system   bool
}

// A user turn includes its assistant calls and tool results. Cutting only between turns cannot orphan a tool result.
type compressionMessage struct {
	Role       string `json:"role"`
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	ToolCalls  []struct {
		ID string `json:"id"`
	} `json:"tool_calls"`
	FunctionCall *struct {
		Name string `json:"name"`
	} `json:"function_call"`
}

func compressionGroups(messages []jsonx.Raw) ([]compressionGroup, error) {
	var groups []compressionGroup
	pending := map[string]struct{}{}
	for i, raw := range messages {
		var m compressionMessage
		if err := jsonx.Unmarshal(raw, &m); err != nil {
			return nil, shapeError("context-compression requires message objects")
		}
		system := m.Role == "system" || m.Role == "developer"
		boundary := m.Role == "user" || system
		if i > 0 {
			boundary = boundary || groups[len(groups)-1].system
		}
		if i == 0 || len(pending) == 0 && boundary {
			groups = append(groups, compressionGroup{from: i})
		}
		group := &groups[len(groups)-1]
		group.to, group.system = i+1, group.system || system
		m.pending(pending)
	}
	return groups, nil
}

func (m compressionMessage) pending(pending map[string]struct{}) {
	for _, call := range m.ToolCalls {
		pending["tool:"+call.ID] = struct{}{}
	}
	if m.FunctionCall != nil {
		pending["function:"+m.FunctionCall.Name] = struct{}{}
	}
	if m.Role == "tool" {
		delete(pending, "tool:"+m.ToolCallID)
	}
	if m.Role == "function" {
		delete(pending, "function:"+m.Name)
	}
}

func compressibleText(m requestObject, role string) bool {
	return (role == "user" || role == "assistant") && m["content"].Kind() == '"' && isNull(m["tool_calls"]) && isNull(m["function_call"])
}

func compressText(raw []byte, remove int64) ([]byte, error) {
	m, err := requestMembers(raw)
	if err != nil {
		return nil, err
	}
	role, err := requestString(m["role"])
	if err != nil {
		return nil, err
	}
	if !compressibleText(m, role) {
		return raw, nil
	}
	text, err := requestString(m["content"])
	if err != nil {
		return nil, err
	}
	shortened, err := replaceRequestMember(raw, "content", jsonValue(middleText(text, remove)))
	if err != nil {
		return nil, err
	}
	if len(shortened) >= len(raw) {
		return raw, nil
	}
	return shortened, nil
}

// Keep at least the first and last code point; if the protected envelope is too large, fail rather than erase an end.
func middleText(text string, remove int64) string {
	_, first := utf8.DecodeRuneInString(text)
	_, last := utf8.DecodeLastRuneInString(text)
	if len(text) <= first+last {
		return text
	}
	keep := max(first+last, len(text)-int(min(remove, int64(len(text)))))
	left := max(first, keep/2)
	right := len(text) - (keep - left)
	for left > 0 && !utf8.RuneStart(text[left]) {
		left--
	}
	for right < len(text) && !utf8.RuneStart(text[right]) {
		right++
	}
	return text[:left] + text[right:]
}

// A compression may replace only messages or a text string. Canonicalizing the surrounding raw values would change
// signed content and could overflow on a valid JSON number in a tool schema.
func replaceRequestMember(body []byte, name string, replacement []byte) ([]byte, error) {
	r := reader{dec: jsontext.NewDecoder(bytes.NewBuffer(body))}
	out := body
	err := r.eachMember(func() error {
		matches := string(r.name) == name
		value, err := r.dec.ReadValue()
		if err != nil {
			return err
		}
		if matches {
			end := int(r.dec.InputOffset())
			start := end - len(value)
			out = make([]byte, 0, len(body)-len(value)+len(replacement))
			out = append(out, body[:start]...)
			out = append(out, replacement...)
			out = append(out, body[end:]...)
		}
		return nil
	})
	return out, err
}
