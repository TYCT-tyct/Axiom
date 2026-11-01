package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"errors"
)

// toolChoice maps tool_choice and parallel_tool_calls. Anthropic refuses a forced choice while thinking, and some
// models (Claude Opus 5.5, Sonnet 5.5 and Fable 5.1) refuse one at all.
func (m *messagesWriter) toolChoice() error {
	choice, name, err := messagesChoice(m.c.ToolChoice, m.o.b)
	if err != nil {
		return err
	}
	forced := name != nil || choice == messagesChoices["required"]
	if forced {
		if err := m.forcedRefusal(); err != nil {
			return err
		}
	}
	parallelOff := m.c.ParallelToolCalls != nil && !*m.c.ParallelToolCalls
	if isNull(m.c.ToolChoice) && !parallelOff || len(m.c.Tools) == 0 {
		return nil
	}
	m.o.str(`,"tool_choice":`).str(choice)
	if name != nil {
		m.o.raw(name)
	}
	if parallelOff {
		m.o.str(`,"disable_parallel_tool_use":true`)
	}
	m.o.byte('}')
	return m.o.err
}

// messagesChoice is tool_choice in Messages' terms, but its closing brace: one of the scalar choices, or the head of a
// named one and the name, which forces that tool. A choice is the value the client wrote, not the bytes it spelled it
// with: an escaped "auto" is auto (README.md「跨格式转换」). Neither is copied: a name is a slice of the request.
func messagesChoice(v jsontext.Value, b *budget) (choice string, name jsontext.Value, err error) {
	if isNull(v) {
		return messagesChoices["auto"], nil, nil
	}
	if v.Kind() == '"' && len(v) <= 64 { // the longest scalar choice, every byte escaped, fits
		var buf [64]byte
		name, _ := jsontext.AppendUnquote(buf[:0], v) //nolint:errcheck // decodeCanon validates JSON; Kind above guarantees a string
		if choice := messagesChoices[string(name)]; choice != "" {
			return choice, nil, nil
		}
	}
	var named struct {
		Function struct {
			Name jsontext.Value `json:"name"`
		} `json:"function"`
	}
	if err := unmarshalWithin(v, &named, b); err != nil || isNull(named.Function.Name) {
		if errors.Is(err, ErrMemory) {
			return "", nil, err
		}
		return "", nil, &NotSupported{Reason: "a tool_choice Messages cannot express"}
	}
	return `{"type":"tool","name":`, named.Function.Name, nil
}

var messagesChoices = map[string]string{
	"auto":     `{"type":"` + choiceNames["auto"] + `"`,
	"none":     `{"type":"` + choiceNames["none"] + `"`,
	"required": `{"type":"` + choiceNames["required"] + `"`,
}
