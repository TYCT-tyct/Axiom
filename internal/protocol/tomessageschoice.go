package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go

	"axiom/internal/platform/jsonx"
)

// toolChoice maps tool_choice and parallel_tool_calls. Anthropic refuses a forced choice while thinking, and some
// models (Claude Opus 5.5, Sonnet 5.5 and Fable 5.1) refuse one at all.
func (m *messagesWriter) toolChoice(out []byte) ([]byte, error) {
	choice, forced, err := messagesChoice(m.c.ToolChoice)
	if err != nil {
		return nil, err
	}
	if forced {
		if err := m.forcedRefusal(); err != nil {
			return nil, err
		}
	}
	parallelOff := m.c.ParallelToolCalls != nil && !*m.c.ParallelToolCalls
	if isNull(m.c.ToolChoice) && !parallelOff || len(m.c.Tools) == 0 {
		return out, nil
	}
	if parallelOff {
		choice += `,"disable_parallel_tool_use":true`
	}
	return append(append(append(out, `,"tool_choice":`...), choice...), '}'), nil
}

// messagesChoice is tool_choice in Messages' terms, but its closing brace, and whether it forces a tool. A choice is
// the value the client wrote, not the bytes it spelled it with: an escaped "auto" is auto (README.md「跨格式转换」).
func messagesChoice(v jsontext.Value) (choice string, forced bool, err error) {
	if isNull(v) {
		return messagesChoices["auto"], false, nil
	}
	if v.Kind() == '"' {
		var buf [16]byte
		name, err := jsontext.AppendUnquote(buf[:0], v)
		if err != nil {
			return "", false, shapeError("a tool_choice that is not a string")
		}
		if choice := messagesChoices[string(name)]; choice != "" {
			return choice, string(name) == "required", nil
		}
	}
	var named struct {
		Function struct {
			Name jsontext.Value `json:"name"`
		} `json:"function"`
	}
	if err := jsonx.Unmarshal(v, &named); err != nil || isNull(named.Function.Name) {
		return "", false, &NotSupported{Reason: "a tool_choice Messages cannot express"}
	}
	return `{"type":"tool","name":` + string(named.Function.Name), true, nil
}

var messagesChoices = map[string]string{
	"auto":     `{"type":"` + choiceNames["auto"] + `"`,
	"none":     `{"type":"` + choiceNames["none"] + `"`,
	"required": `{"type":"` + choiceNames["required"] + `"`,
}
