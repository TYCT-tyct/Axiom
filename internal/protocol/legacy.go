package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go: strings are kept as written, never decoded and encoded again
	"slices"
	"strconv"

	"axiom/internal/platform/jsonx"
)

// upgradeLegacy normalizes Chat's deprecated function calling before shape and pairing checks.
// Pairing follows sub2api backend/internal/pkg/apicompat/chatcompletions_to_responses.go
// (21cc92693, 2026-10-22): ordered IDs skip existing IDs and results claim the oldest call by name.
func (c *canon) upgradeLegacy() error {
	if err := c.upgradeChoice(); err != nil {
		return err
	}
	c.upgradeTools()
	return c.upgradeCalls()
}

// upgradeChoice makes function_call the tool_choice, and refuses a request that names both.
func (c *canon) upgradeChoice() error {
	if isNull(c.FunctionCall) {
		return nil
	}
	if !isNull(c.ToolChoice) {
		return shapeError("tool_choice and function_call in one request")
	}
	choice, err := legacyChoice(c.FunctionCall)
	if err != nil {
		return err
	}
	c.ToolChoice = choice
	return nil
}

// upgradeTools adds the functions to the tools, after the tools the request names when it names both.
func (c *canon) upgradeTools() {
	for _, f := range c.Functions {
		c.Tools = append(c.Tools, canonTool{Type: "function", Function: f})
	}
}

// upgradeCalls turns the messages' legacy calls into tool calls and their results into tool results, in the order the
// messages were written.
func (c *canon) upgradeCalls() error {
	var calls *legacyCalls
	for i := range c.Messages {
		m := &c.Messages[i]
		if isNull(m.FunctionCall) && m.Role != "function" {
			continue
		}
		if calls == nil {
			calls = newLegacyCalls(c.Messages)
		}
		var err error
		switch {
		case !isNull(m.FunctionCall):
			err = calls.add(m)
		case m.Role == "function":
			err = calls.answer(m)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// canonFunction is a function of Chat's functions, which its tools wrap: the function is the entry itself.
type canonFunction struct {
	Name        jsontext.Value `json:"name"`
	Description jsontext.Value `json:"description"`
	Parameters  jsontext.Value `json:"parameters"`
	Strict      *bool          `json:"strict"`
}

// legacyChoice is function_call as tool_choice: the same choices, a named function written the way tools name one.
func legacyChoice(v jsontext.Value) (jsontext.Value, error) {
	if v.Kind() == '"' {
		name, err := jsontext.AppendUnquote(nil, v)
		if err != nil {
			return nil, shapeError("a function_call that is not a string")
		}
		switch string(name) {
		case "auto", "none", "required":
			return v, nil
		}
		return nil, shapeError("a function_call that is not a choice")
	}
	var named struct {
		Name jsontext.Value `json:"name"`
	}
	if err := jsonx.Unmarshal(v, &named); err != nil || named.Name.Kind() != '"' {
		return nil, shapeError("a function_call without a name")
	}
	return slices.Concat([]byte(`{"type":"function","function":{"name":`), named.Name, []byte(`}}`)), nil
}

// legacyCalls pairs the calls of a legacy request with their results: the IDs it already uses, the number its next ID
// takes, and the calls of each function name that no result has answered yet.
type legacyCalls struct {
	used    map[string]bool
	next    int
	pending map[string][]string
}

func newLegacyCalls(messages []canonMessage) *legacyCalls {
	calls := &legacyCalls{used: map[string]bool{}, pending: map[string][]string{}}
	for _, m := range messages {
		for _, call := range m.ToolCalls {
			calls.used[call.ID] = true
		}
		if m.ToolCallID != nil {
			calls.used[*m.ToolCallID] = true
		}
	}
	return calls
}

// add makes a message's legacy function call a tool call, with an ID of its own.
func (c *legacyCalls) add(m *canonMessage) error {
	if m.Role != "assistant" {
		return shapeError("a function_call in a " + m.Role + " message")
	}
	if len(m.ToolCalls) > 0 {
		return shapeError("tool_calls and function_call in one message")
	}
	var call struct {
		Name      jsontext.Value `json:"name"`
		Arguments jsontext.Value `json:"arguments"`
	}
	if err := jsonx.Unmarshal(m.FunctionCall, &call); err != nil || call.Name.Kind() != '"' {
		return shapeError("a function_call without a name")
	}
	name, err := jsontext.AppendUnquote(nil, call.Name)
	if err != nil {
		return shapeError("a function_call without a name")
	}
	var toolCall canonToolCall
	toolCall.ID, toolCall.Type = c.assign(string(name)), "function"
	toolCall.Function.Name, toolCall.Function.Arguments = call.Name, call.Arguments
	m.ToolCalls = append(m.ToolCalls, toolCall)
	return nil
}

// answer makes a legacy function result a tool result of the call it names.
func (c *legacyCalls) answer(m *canonMessage) error {
	if m.ToolCallID != nil { // the client paired it itself
		m.Role, m.Name = "tool", nil
		return nil
	}
	if m.Name.Kind() != '"' {
		return shapeError("a function result without a name")
	}
	name, err := jsontext.AppendUnquote(nil, m.Name)
	if err != nil {
		return shapeError("a function result without a name")
	}
	id, ok := c.claim(string(name))
	if !ok {
		return &NotSupported{Reason: "a function result that answers no call"}
	}
	m.Role, m.ToolCallID, m.Name = "tool", &id, nil
	return nil
}

// assign is an ID for a call of name that no call or result of the request uses, and the next result of that name takes
// it.
func (c *legacyCalls) assign(name string) string {
	for {
		c.next++
		id := "call_legacy_" + strconv.Itoa(c.next)
		if c.used[id] {
			continue
		}
		c.used[id] = true
		c.pending[name] = append(c.pending[name], id)
		return id
	}
}

// claim is the oldest call of name that no result has answered.
func (c *legacyCalls) claim(name string) (id string, ok bool) {
	queue := c.pending[name]
	if len(queue) == 0 {
		return "", false
	}
	c.pending[name] = queue[1:]
	return queue[0], true
}
