package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // slices refer to the already validated upstream event
	"fmt"
)

// Reuse the token reader and tool slice; increment strings point into data until the choice has been consumed.
func (s *chatToNative) readChunk(data []byte) error {
	s.begin(data)
	return s.eachMember(func() error {
		if string(s.name) == "choices" {
			return s.eachElement(s.readNativeChoice)
		}
		return s.skip()
	})
}

func (s *chatToNative) readNativeChoice() error {
	clear(s.chunk.Delta.Tools)
	tools := s.chunk.Delta.Tools[:0]
	s.chunk = nativeChatChoice{}
	s.chunk.Delta.Tools = tools
	if err := s.eachMember(func() (err error) {
		switch string(s.name) {
		case "index":
			s.chunk.Index, err = s.count()
		case "delta":
			err = s.eachMember(s.readNativeDelta)
		case "finish_reason":
			var v []byte
			v, err = s.str()
			s.chunk.Finish = string(v)
		default:
			err = s.skip()
		}
		return err
	}); err != nil {
		return err
	}
	return s.choice(s.chunk)
}

func (s *chatToNative) readNativeDelta() error {
	switch string(s.name) {
	case "content":
		return s.captureNative(&s.chunk.Delta.Content)
	case "reasoning":
		return s.captureNative(&s.chunk.Delta.Reasoning)
	case "reasoning_content":
		return s.captureNative(&s.chunk.Delta.ReasoningContent)
	case "refusal":
		return s.captureNative(&s.chunk.Delta.Refusal)
	case "reasoning_details":
		return s.eachElement(func() error {
			return fmt.Errorf("%w: Chat reasoning_details cannot be represented by this client", ErrUpstream)
		})
	case "tool_calls":
		return s.eachElement(s.readNativeTool)
	default:
		return s.skip()
	}
}

func (s *chatToNative) captureNative(dst *jsontext.Value) error {
	if ok, err := s.present('"', "a string"); !ok {
		return err
	}
	v, err := s.value()
	*dst = v
	return err
}

func (s *chatToNative) readNativeTool() error {
	var call nativeChatTool
	if err := s.eachMember(func() (err error) {
		switch string(s.name) {
		case "index":
			call.Index, err = s.count()
		case "id", "type":
			name := string(s.name)
			var v []byte
			v, err = s.str()
			if name == "id" {
				call.ID = string(v)
			} else {
				call.Type = string(v)
			}
		case "function":
			err = s.readNativeFunction(&call.Function)
		default:
			err = s.skip()
		}
		return err
	}); err != nil {
		return err
	}
	tools, err := grow(&s.aggBase, s.chunk.Delta.Tools, 1, 1)
	if err == nil {
		s.chunk.Delta.Tools = append(tools, call)
	}
	return err
}

func (s *chatToNative) readNativeFunction(fn *nativeChatFunction) error {
	return s.eachMember(func() (err error) {
		switch string(s.name) {
		case "name":
			var v []byte
			v, err = s.str()
			fn.Name = string(v)
		case "arguments":
			err = s.captureNative(&fn.Arguments)
		default:
			err = s.skip()
		}
		return err
	})
}
