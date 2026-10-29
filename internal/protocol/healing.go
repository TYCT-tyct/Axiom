package protocol

import (
	"bytes"
	"strings"

	"axiom/internal/platform/jsonx"
)

// healingMemoryFactor prepays the original and final strings, unquoted copies, a repair buffer up to twice the input,
// re-escaping and strict validation. The allowance is drawn as content grows, since Finish cannot fail for budget.
const healingMemoryFactor = 20

// healContent returns escaped JSON-string contents. Repair is best effort: failure preserves the exact reply.
// Its working buffers are prepaid while the aggregate receives content, before settlement seals the budget.
func healContent(escaped []byte) []byte {
	text, err := requestString(appendContents(nil, escaped, true))
	if err != nil || jsonx.Validate([]byte(text)) == nil {
		return escaped
	}
	start := strings.IndexAny(text, "{[")
	if start < 0 {
		return escaped
	}
	p := jsonRepair{src: text[start:], out: make([]byte, 0, 2*len(text))}
	if !p.value(0) || jsonx.Validate(p.out) != nil {
		return escaped
	}
	value := appendQuoted(nil, string(p.out))
	return value[1 : len(value)-1]
}

type jsonRepair struct {
	src string
	at  int
	out []byte
}

func (p *jsonRepair) space() {
	for p.at < len(p.src) && strings.ContainsRune(" \t\r\n", rune(p.src[p.at])) {
		p.at++
	}
}

func (p *jsonRepair) value(depth int) bool {
	p.space()
	if p.at == len(p.src) || depth >= jsonx.MaxDepth {
		return false
	}
	switch p.src[p.at] {
	case '{', '[':
		return p.container(depth + 1)
	case '"':
		return p.quoted()
	default:
		start := p.at
		for p.at < len(p.src) && !strings.ContainsRune(",]} \t\r\n", rune(p.src[p.at])) {
			p.at++
		}
		value := []byte(p.src[start:p.at])
		if len(value) == 0 || jsonx.Validate(value) != nil {
			return false
		}
		p.out = append(p.out, value...)
		return true
	}
}

func (p *jsonRepair) quoted() bool {
	start := p.at
	p.at++
	for p.at < len(p.src) {
		c := p.src[p.at]
		p.at++
		if c == '\\' {
			p.at++
			continue
		}
		if c == '"' {
			p.out = append(p.out, p.src[start:p.at]...)
			return true
		}
	}
	return false
}

func bareKeyByte(c byte, digit bool) bool {
	if c == '_' || c == '$' {
		return true
	}
	if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
		return true
	}
	return digit && c >= '0' && c <= '9'
}

func (p *jsonRepair) key() bool {
	p.space()
	if p.at == len(p.src) {
		return false
	}
	if p.src[p.at] == '"' {
		return p.quoted()
	}
	start := p.at
	for p.at < len(p.src) {
		c := p.src[p.at]
		if !bareKeyByte(c, p.at > start) {
			break
		}
		p.at++
	}
	if p.at == start {
		return false
	}
	p.out = appendQuoted(p.out, p.src[start:p.at])
	return true
}

func (p *jsonRepair) container(depth int) bool {
	object := p.src[p.at] == '{'
	close := byte(']')
	if object {
		close = '}'
	}
	p.out = append(p.out, p.src[p.at])
	p.at++
	first := true
	for {
		p.space()
		if p.at == len(p.src) {
			p.out = append(p.out, close)
			return true
		}
		if p.src[p.at] == close {
			p.out = append(p.out, close)
			p.at++
			return true
		}
		if !first {
			p.out = append(p.out, ',')
		}
		if !p.element(object, depth) {
			return false
		}
		first = false
		p.space()
		if p.at < len(p.src) && p.src[p.at] == ',' {
			p.at++
		}
	}
}

func (p *jsonRepair) element(object bool, depth int) bool {
	if object {
		return p.member(depth)
	}
	return p.value(depth)
}

func (p *jsonRepair) member(depth int) bool {
	if !p.key() {
		return false
	}
	p.space()
	if p.at == len(p.src) || p.src[p.at] != ':' {
		return false
	}
	p.out = append(p.out, ':')
	p.at++
	return p.value(depth)
}

func (a *chatAggregate) heal() {
	if !a.healing || a.hadError {
		return
	}
	for i := range a.choices {
		c := &a.choices[i]
		if len(c.tools) > 0 || !controlIs(c.finish, "stop") || len(c.strings[pieceRefusal]) > 0 {
			continue
		}
		original := c.strings[pieceContent]
		c.strings[pieceContent] = healContent(original)
		if !bytes.Equal(original, c.strings[pieceContent]) {
			// Token logprobs describe the original output, so they cannot describe repaired content.
			c.logs, c.logSeen = [2][]byte{}, [2]bool{}
		}
	}
}
