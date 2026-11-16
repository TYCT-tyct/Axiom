package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // the request tree is decoded token by token
	"unsafe"
)

//nolint:funlen,cyclop // one dispatch over the members canon names, each one short
func (d *decoder) top(c *canon) error {
	return d.object(func() error {
		switch string(d.name) {
		case "messages":
			return elements(d, &c.Messages, d.message)
		case "tools":
			return elements(d, &c.Tools, d.tool)
		case "functions":
			return elements(d, &c.Functions, d.function)
		case "tool_choice":
			return d.field(&c.ToolChoice)
		case "function_call":
			return d.field(&c.FunctionCall)
		case "parallel_tool_calls":
			return d.pointer(&c.ParallelToolCalls, (*decoder).boolp)
		case "max_tokens":
			return d.pointer(&c.MaxTokens, (*decoder).intp)
		case "max_completion_tokens":
			return d.pointer(&c.MaxCompletionTokens, (*decoder).intp)
		case "temperature":
			return d.field(&c.Temperature)
		case "top_p":
			return d.field(&c.TopP)
		case "stop":
			return d.field(&c.Stop)
		case "stream":
			return d.pointer(&c.Stream, (*decoder).boolp)
		case "reasoning_effort":
			return d.pointer(&c.ReasoningEffort, (*decoder).strp)
		case "reasoning":
			return d.pointer(&c.Reasoning, (*decoder).reasoning)
		case "response_format":
			return d.pointer(&c.ResponseFormat, (*decoder).format)
		case "user":
			return d.pointer(&c.User, (*decoder).strp)
		case "safety_identifier":
			return d.pointer(&c.SafetyIdentifier, (*decoder).strp)
		case "prompt_cache_key":
			return d.pointer(&c.PromptCacheKey, (*decoder).strp)
		case "service_tier":
			return d.pointer(&c.ServiceTier, (*decoder).strp)
		case "verbosity":
			return d.field(&c.Verbosity)
		case "model":
			return d.field(&c.Model)
		case "stream_options":
			return d.field(&c.StreamOptions)
		case "preset":
			return d.field(&c.Preset)
		case "fallbacks":
			return d.field(&c.Fallbacks)
		case "provider":
			return d.field(&c.Provider)
		case "models":
			return d.field(&c.Models)
		case "route":
			return d.field(&c.Route)
		case "transforms":
			return d.field(&c.Transforms)
		case "plugins":
			return d.field(&c.Plugins)
		case "usage":
			return d.field(&c.Usage)
		case "session_id":
			return d.field(&c.SessionID)
		case "axiom":
			return d.field(&c.Axiom)
		default:
			return d.unknown(&c.Unknown)
		}
	})
}

func elements[T any](d *decoder, dst *[]T, read func(*T) error) error {
	return d.array(func() error {
		var err error
		if *dst, err = growWithin(*dst, d.b); err != nil {
			return err
		}
		return read(&(*dst)[len(*dst)-1])
	})
}

// field reads a member kept as written.
func (d *decoder) field(dst *jsontext.Value) error {
	v, err := d.value()
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

// pointer reads an optional pointer member through read; null is nil.
func (d *decoder) pointer[T any](dst **T, read func(*decoder) (*T, error)) error {
	v, err := read(d)
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

// message reads one message or the zero value of null.
//
//nolint:cyclop // one dispatch over the members a message may carry
func (d *decoder) message(m *canonMessage) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "role":
			s, err := d.str()
			m.Role = s
			return err
		case "content":
			v, err := d.value()
			m.Content = v
			if err != nil {
				return err
			}
			m.Parts, err = d.parts(v)
			return err
		case "tool_calls":
			return elements(d, &m.ToolCalls, d.toolCall)
		case "tool_call_id":
			return d.pointer(&m.ToolCallID, (*decoder).strp)
		case "function_call":
			return d.field(&m.FunctionCall)
		case "reasoning_details":
			return elements(d, &m.ReasoningDetails, d.reasoningDetail)
		case "name":
			return d.field(&m.Name)
		case "refusal":
			return d.field(&m.Refusal)
		case "reasoning":
			return d.field(&m.ReasoningText)
		case "reasoning_content":
			return d.field(&m.ReasoningContent)
		default:
			return d.unknown(&m.Unknown)
		}
	})
}

// parts reads a message's content: a string is one text part, null none, an array its parts.
func (d *decoder) parts(v jsontext.Value) ([]canonPart, error) {
	//nolint:exhaustive // the default rejects every other JSON kind with the client's error
	switch v.Kind() {
	case 'n':
		return nil, nil
	case '"':
		if err := d.b.draw(rounded(int64(unsafe.Sizeof(canonPart{})))); err != nil {
			return nil, err
		}
		return []canonPart{{Type: "text", Text: v}}, nil
	case '[':
	default:
		return nil, d.typeError()
	}
	if err := d.b.draw(subDecoderMemory); err != nil {
		return nil, err
	}
	pd := &decoder{dec: newDecoder(v), b: d.b, parent: d}
	var parts []canonPart
	if err := elements(pd, &parts, pd.part); err != nil {
		return nil, err
	}
	return parts, nil
}

// part reads one content part.
func (d *decoder) part(p *canonPart) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "type":
			s, err := d.str()
			p.Type = s
			return err
		case "text":
			return d.field(&p.Text)
		case "refusal":
			return d.field(&p.Refusal)
		case "image_url":
			return d.pointer(&p.ImageURL, (*decoder).imageURL)
		case "file":
			return d.pointer(&p.File, (*decoder).file)
		case "cache_control":
			return d.field(&p.CacheControl)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) imageURL() (*canonImage, error) {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return nil, err
	}
	if err := d.b.draw(int64(unsafe.Sizeof(canonImage{}))); err != nil {
		return nil, err
	}
	p := &canonImage{}
	return p, d.object(func() error {
		switch string(d.name) {
		case "url":
			s, err := d.str()
			p.URL = s
			return err
		case "detail":
			s, err := d.str()
			p.Detail = s
			return err
		default:
			return d.skip()
		}
	})
}

func (d *decoder) file() (*canonFile, error) {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return nil, err
	}
	if err := d.b.draw(int64(unsafe.Sizeof(canonFile{}))); err != nil {
		return nil, err
	}
	p := &canonFile{}
	return p, d.object(func() error {
		switch string(d.name) {
		case "file_data":
			return d.pointer(&p.FileData, (*decoder).strp)
		case "file_id":
			return d.pointer(&p.FileID, (*decoder).strp)
		case "filename":
			return d.field(&p.Filename)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) tool(t *canonTool) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "type":
			s, err := d.str()
			t.Type = s
			return err
		case "function":
			return d.function(&t.Function)
		case "cache_control":
			return d.field(&t.CacheControl)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) function(f *canonFunction) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "name":
			return d.field(&f.Name)
		case "description":
			return d.field(&f.Description)
		case "parameters":
			return d.field(&f.Parameters)
		case "strict":
			return d.pointer(&f.Strict, (*decoder).boolp)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) toolCall(c *canonToolCall) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "id":
			s, err := d.str()
			c.ID = s
			return err
		case "type":
			s, err := d.str()
			c.Type = s
			return err
		case "function":
			return d.callFunction(&c.Function)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) callFunction(f *canonCallFunction) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "name":
			return d.field(&f.Name)
		case "arguments":
			return d.field(&f.Arguments)
		default:
			return d.skip()
		}
	})
}

func (d *decoder) reasoning() (*canonReasoning, error) {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return nil, err
	}
	if err := d.b.draw(int64(unsafe.Sizeof(canonReasoning{}))); err != nil {
		return nil, err
	}
	v := &canonReasoning{}
	if err := d.object(func() error {
		switch string(d.name) {
		case "effort":
			return d.pointer(&v.Effort, (*decoder).strp)
		case "max_tokens":
			return d.pointer(&v.MaxTokens, (*decoder).intp)
		case "exclude":
			return d.pointer(&v.Exclude, (*decoder).boolp)
		case "enabled":
			return d.pointer(&v.Enabled, (*decoder).boolp)
		default:
			return d.skip()
		}
	}); err != nil {
		return nil, err
	}
	return v, nil
}

//nolint:cyclop // one dispatch over the members a format may carry
func (d *decoder) format() (*canonFormat, error) {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return nil, err
	}
	if err := d.b.draw(int64(unsafe.Sizeof(canonFormat{}))); err != nil {
		return nil, err
	}
	v := &canonFormat{}
	if err := d.object(func() error {
		switch string(d.name) {
		case "type":
			s, err := d.str()
			v.Type = s
			return err
		case "json_schema":
			if d.dec.PeekKind() == 'n' {
				_, err := d.value()
				return err
			}
			if err := d.b.draw(int64(unsafe.Sizeof(*v.JSONSchema))); err != nil {
				return err
			}
			v.JSONSchema = &struct {
				Name        jsontext.Value `json:"name"`
				Description jsontext.Value `json:"description"`
				Schema      jsontext.Value `json:"schema"`
				Strict      *bool          `json:"strict"`
			}{}
			return d.object(func() error {
				switch string(d.name) {
				case "name":
					return d.field(&v.JSONSchema.Name)
				case "description":
					return d.field(&v.JSONSchema.Description)
				case "schema":
					return d.field(&v.JSONSchema.Schema)
				case "strict":
					return d.pointer(&v.JSONSchema.Strict, (*decoder).boolp)
				default:
					return d.skip()
				}
			})
		default:
			return d.skip()
		}
	}); err != nil {
		return nil, err
	}
	return v, nil
}

func (d *decoder) reasoningDetail(r *canonReasoningDetail) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.value()
		return err
	}
	return d.object(func() error {
		switch string(d.name) {
		case "type":
			s, err := d.str()
			r.Type = s
			return err
		case "format":
			s, err := d.str()
			r.Format = s
			return err
		case "id":
			return d.field(&r.ID)
		case "text":
			return d.field(&r.Text)
		case "summary":
			return d.field(&r.Summary)
		case "signature":
			return d.field(&r.Signature)
		case "data":
			return d.field(&r.Data)
		case "index":
			return d.field(&r.Index)
		default:
			return d.skip()
		}
	})
}

// skip reads one value token by token, so a member the tree does not keep still validates its nesting.
func (d *decoder) skip() error {
	depth := d.dec.StackDepth()
	for {
		if err := d.token(); err != nil {
			return err
		}
		if d.dec.StackDepth() == depth {
			return nil
		}
	}
}

func (d *decoder) token() error {
	_, err := d.dec.ReadToken()
	return err
}
