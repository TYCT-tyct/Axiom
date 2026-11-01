package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go: strings are kept as written, never decoded and encoded again
	"errors"
	"slices"
	"strconv"
	"unsafe"
)

// upgradeLegacy normalizes Chat's deprecated function calling before shape and pairing checks.
// Pairing follows sub2api backend/internal/pkg/apicompat/chatcompletions_to_responses.go
// (21cc92693, 2026-10-22): ordered IDs skip existing IDs and results claim the oldest call by name.
func (c *canon) upgradeLegacy(b *budget) error {
	c.legacy = c.legacyOnly()
	if err := c.upgradeChoice(b); err != nil {
		return err
	}
	if err := c.upgradeTools(b); err != nil {
		return err
	}
	if c.legacy {
		c.ParallelToolCalls = nil
		if len(c.Tools) > 0 {
			disabled := false
			c.ParallelToolCalls = &disabled
		}
	}
	return c.upgradeCalls(b)
}

// upgradeChoice makes function_call the tool_choice, and refuses a request that names both.
func (c *canon) upgradeChoice(b *budget) error {
	if isNull(c.FunctionCall) {
		return nil
	}
	if !isNull(c.ToolChoice) {
		return shapeError("tool_choice and function_call in one request")
	}
	choice, err := legacyChoice(c.FunctionCall, b)
	if err != nil {
		return err
	}
	c.ToolChoice = choice
	return nil
}

// upgradeTools adds the functions to the tools, after the tools the request names when it names both.
func (c *canon) upgradeTools(b *budget) error {
	for _, f := range c.Functions {
		var err error
		if c.Tools, err = growWithin(c.Tools, b); err != nil {
			return err
		}
		c.Tools[len(c.Tools)-1] = canonTool{Type: "function", Function: f}
	}
	return nil
}

// upgradeCalls turns the messages' legacy calls into tool calls and their results into tool results, in the order the
// messages were written.
func (c *canon) upgradeCalls(b *budget) error {
	var calls *legacyCalls
	for i := range c.Messages {
		m := &c.Messages[i]
		if isNull(m.FunctionCall) && m.Role != "function" {
			continue
		}
		if calls == nil {
			var err error
			if calls, err = newLegacyCalls(c.Messages, b); err != nil {
				return err
			}
		}
		var err error
		switch {
		case !isNull(m.FunctionCall):
			err = calls.add(m, b)
		case m.Role == "function":
			err = calls.answer(m, b)
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

// legacyChoice maps the deprecated auto/none choices and named functions to tool_choice.
func legacyChoice(v jsontext.Value, b *budget) (jsontext.Value, error) {
	if v.Kind() == '"' {
		if len(v) > 32 { // auto and none fit with every byte escaped; anything longer is neither
			return nil, shapeError("a function_call that is not a choice")
		}
		var buf [32]byte
		name, _ := jsontext.AppendUnquote(buf[:0], v) //nolint:errcheck // decodeCanon validates JSON; Kind above guarantees a string
		switch string(name) {
		case "auto", "none":
			return v, nil
		}
		return nil, shapeError("a function_call that is not a choice")
	}
	var named struct {
		Name jsontext.Value `json:"name"`
	}
	if err := unmarshalWithin(v, &named, b); err != nil {
		if errors.Is(err, ErrMemory) {
			return nil, err
		}
		return nil, shapeError("a function_call without a name")
	}
	if named.Name.Kind() != '"' {
		return nil, shapeError("a function_call without a name")
	}
	if err := b.draw(int64(len(named.Name) + 32)); err != nil {
		return nil, err
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

func newLegacyCalls(messages []canonMessage, b *budget) (*legacyCalls, error) {
	if err := b.draw(2*mapMemory + rounded(int64(unsafe.Sizeof(legacyCalls{})))); err != nil {
		return nil, err
	}
	calls := &legacyCalls{used: map[string]bool{}, pending: map[string][]string{}}
	for _, m := range messages {
		for _, call := range m.ToolCalls {
			if err := b.draw(mapEntryMemory + int64(len(call.ID))); err != nil {
				return nil, err
			}
			calls.used[call.ID] = true
		}
		if m.ToolCallID != nil {
			if err := b.draw(mapEntryMemory + int64(len(*m.ToolCallID))); err != nil {
				return nil, err
			}
			calls.used[*m.ToolCallID] = true
		}
	}
	return calls, nil
}

// add makes a message's legacy function call a tool call, with an ID of its own.
func (c *legacyCalls) add(m *canonMessage, b *budget) error {
	if m.Role != "assistant" {
		return shapeError("a function_call in a " + m.Role + " message")
	}
	if len(m.ToolCalls) > 0 {
		return shapeError("tool_calls and function_call in one message")
	}
	var call canonToolCall
	if err := unmarshalWithin(m.FunctionCall, &call.Function, b); err != nil {
		if errors.Is(err, ErrMemory) {
			return err
		}
		return shapeError("a function_call without a name")
	}
	if call.Function.Name.Kind() != '"' {
		return shapeError("a function_call without a name")
	}
	if err := b.draw(2 * rounded(int64(len(call.Function.Name)))); err != nil { // the name unquoted, and as a string
		return err
	}
	name, _ := jsontext.AppendUnquote(make([]byte, 0, len(call.Function.Name)), call.Function.Name) //nolint:errcheck // Unmarshal validated JSON and Name's kind above
	call.ID, call.Type = "", "function"
	var err error
	if call.ID, err = c.assign(string(name), b); err != nil {
		return err
	}
	if m.ToolCalls, err = growWithin(m.ToolCalls, b); err != nil {
		return err
	}
	m.ToolCalls[len(m.ToolCalls)-1] = call
	return nil
}

// answer makes a legacy function result a tool result of the call it names.
func (c *legacyCalls) answer(m *canonMessage, b *budget) error {
	m.legacyResult = true
	if m.ToolCallID != nil { // the client paired it itself
		m.Role, m.Name = "tool", nil
		return nil
	}
	if m.Name.Kind() != '"' {
		return shapeError("a function result without a name")
	}
	if err := b.draw(rounded(int64(len(m.Name))) + rounded(int64(unsafe.Sizeof("")))); err != nil { // the name unquoted, and &id
		return err
	}
	name, _ := jsontext.AppendUnquote(make([]byte, 0, len(m.Name)), m.Name) //nolint:errcheck // decodeCanon validated JSON and Name's kind above
	id, ok := c.claim(string(name))
	if !ok {
		return &NotSupported{Reason: "a function result that answers no call"}
	}
	m.Role, m.ToolCallID, m.Name = "tool", &id, nil
	return nil
}

// assign is an ID for a call of name that no call or result of the request uses, and the next result of that name takes
// it.
func (c *legacyCalls) assign(name string, b *budget) (string, error) {
	for {
		c.next++
		if err := b.draw(2 * rounded(int64(len("call_legacy_")+20))); err != nil { // the number, and the ID
			return "", err
		}
		id := "call_legacy_" + strconv.Itoa(c.next)
		if c.used[id] {
			continue
		}
		if err := b.draw(2 * mapEntryMemory); err != nil { // its entry in used, and its name's in pending
			return "", err
		}
		c.used[id] = true
		queue, err := growWithin(c.pending[name], b)
		if err != nil {
			return "", err
		}
		queue[len(queue)-1] = id
		c.pending[name] = queue
		return id, nil
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

// legacyOnly keeps modern and mixed clients on the modern response contract.
func (c *canon) legacyOnly() bool {
	if len(c.Tools) > 0 || !isNull(c.ToolChoice) {
		return false
	}
	if len(c.Functions) > 0 || !isNull(c.FunctionCall) {
		return true
	}
	for _, m := range c.Messages {
		if !isNull(m.FunctionCall) || m.Role == "function" {
			return true
		}
	}
	return false
}
