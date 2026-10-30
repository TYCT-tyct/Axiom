package protocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// convertNative writes a Messages or Responses request for an endpoint of the other of the two (README.md
// 「跨格式转换」): the request is read into the canonical model by its own format's reader and written by the target
// format's writer, so both directions share Chat's mapping and cannot drift from it.
func convertNative(from Format, body []byte, t Target, w Rewrites) ([]byte, error) {
	chat, err := nativeChatRequest(from, body, t, w)
	if err != nil {
		return nil, err
	}
	if from == Messages { // a Messages reply hands back thinking signatures, not the encrypted content include asks for
		t.EncryptedReasoning = catalog.ClaimNo
	}
	return convert(chat, t, w)
}

// convert writes a Chat request for a Messages or Responses endpoint (README.md「跨格式转换」): what the endpoint's
// format can express is written; reasoning uses the documented approximations in README.md,
// and other unrepresentable settings are *NotSupported.
func convert(body []byte, t Target, w Rewrites) ([]byte, error) {
	c, err := decodeCanon(body)
	if err != nil {
		return nil, err
	}
	if err := c.checkShape(); err != nil {
		return nil, err
	}
	if err := c.unsupported(); err != nil {
		return nil, err
	}
	if err := c.checkCalls(); err != nil {
		return nil, err
	}
	if t.Format == Messages {
		return toMessages(c, t, w)
	}
	return toResponses(c, t, w)
}

// checkCalls holds the tool calls to what both other formats require: every call has a result and every result a
// call, and no two calls share an ID. OpenAI refuses the first two itself.
func (c *canon) checkCalls() error {
	calls := map[string]bool{} // answered
	for _, m := range c.Messages {
		if err := m.checkCalls(calls); err != nil {
			return err
		}
	}
	for id, answered := range calls {
		if !answered {
			return &NotSupported{Reason: "a tool call has no result: " + id}
		}
	}
	return nil
}

// checkCalls adds a message's calls to calls, or marks the one its result answers.
func (m *canonMessage) checkCalls(calls map[string]bool) error {
	for _, call := range m.ToolCalls {
		if call.Type != "function" {
			return &NotSupported{Reason: "a " + call.Type + " tool call is not converted"}
		}
		if _, seen := calls[call.ID]; seen || call.ID == "" {
			return &NotSupported{Reason: "a tool call ID is empty or repeated: " + call.ID}
		}
		calls[call.ID] = false
	}
	if m.Role != "tool" {
		return nil
	}
	id := ""
	if m.ToolCallID != nil {
		id = *m.ToolCallID
	}
	if answered, ok := calls[id]; !ok || answered {
		return &NotSupported{Reason: "a tool result answers no call, or one answered before: " + id}
	}
	calls[id] = true
	return nil
}

// dataURL splits a base64 data URL into its media type and data.
func dataURL(url string) (media, data string, ok bool) {
	meta, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
	media, base64 := strings.CutSuffix(meta, ";base64")
	return media, data, ok && base64 && strings.HasPrefix(url, "data:") && media != ""
}

// toolUseID is a tool call ID as Anthropic accepts it, ^[a-zA-Z0-9_-]+$ (《Messages API reference》, tool_use.id): an
// ID that matches and does not start with axid_ stays; any other is axid_ and the hex of its bytes. The two sets do
// not meet and hex is one to one, so distinct IDs stay distinct, and calls and results map alike.
func toolUseID(id string) string {
	if id != "" && !strings.HasPrefix(id, "axid_") && strings.Trim(id, idChars) == "" {
		return id
	}
	return "axid_" + hex.EncodeToString([]byte(id))
}

const idChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"

// arguments is a call's arguments, a JSON string holding a JSON object, as the object; empty arguments are an empty
// object, as models that call a function without parameters may send.
func arguments(v jsontext.Value) (jsontext.Value, error) {
	s, err := jsontext.AppendUnquote(nil, v)
	if err != nil {
		return nil, &NotSupported{Reason: "a tool call's arguments are not a string"}
	}
	s = bytes.TrimSpace(s)
	if len(s) == 0 {
		return jsontext.Value("{}"), nil
	}
	if s[0] != '{' || jsonx.Validate(s) != nil {
		return nil, &NotSupported{Reason: "a tool call's arguments are not a JSON object"}
	}
	return s, nil
}

// isNull reports whether a member is absent or null.
func isNull(v jsontext.Value) bool { return len(v) == 0 || v.Kind() == 'n' }
