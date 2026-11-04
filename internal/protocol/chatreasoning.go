package protocol

// reasoningMember is a reasoning member of a delta: its value as written, or for a null where it ends, 0 for a member
// the delta does not have.
type reasoningMember struct {
	value   []byte
	nullEnd int
}

// reasoningOf counts a reasoning member as content and keeps where it is, to write its text under the other name.
func (s *chatStream) reasoningOf(m *reasoningMember) error {
	v, err := s.raw()
	s.addText(v)
	if err != nil {
		return err
	}
	to := int(s.dec.InputOffset())
	if v == nil {
		m.nullEnd = to
	} else {
		m.value = s.e.data[to-len(v) : to]
	}
	return nil
}

// delta reads a delta and writes its reasoning under both names. A relay or a vendor writes one of them (DeepSeek's
// reasoning_content, OpenRouter's reasoning); the client reads either (docs/domains/gateway.md「转发规则」). Both with a
// value are left as they are, and a name that is null takes the other's text.
func (s *chatStream) delta() error {
	s.reasoning, s.reasoningContent = reasoningMember{}, reasoningMember{}
	if err := s.walkNoting(chatDeltaFields, true); err != nil {
		return err
	}
	if s.ctx.HidesReasoning {
		return nil
	}
	if s.reasoning.value != nil && s.reasoningContent.value == nil {
		return s.mirror(s.reasoning.value, s.reasoningContent, `,"reasoning_content":`)
	}
	if s.reasoningContent.value != nil && s.reasoning.value == nil {
		return s.mirror(s.reasoningContent.value, s.reasoning, `,"reasoning":`)
	}
	return nil
}

// mirror writes value as the member into: in place of its null, or, when the delta has none, before the delta's
// closing brace, which the decoder has just read.
func (s *chatStream) mirror(value []byte, into reasoningMember, name string) error {
	if into.nullEnd > 0 {
		return s.editEcho(into.nullEnd-len("null"), into.nullEnd, "", value)
	}
	at := int(s.dec.InputOffset()) - 1
	return s.editEcho(at, at, name, value)
}
