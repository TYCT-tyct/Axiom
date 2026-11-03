package declared

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// tmpl is a path or a request body member with {name} placeholders. It is parsed at load, so a path with a
// placeholder the operation cannot fill, or with a stray brace, stops the node instead of sending a request that means
// something else.
type tmpl struct {
	parts []tmplPart
}

type tmplPart struct {
	literal  string
	variable string
}

// parseTemplate reads text, allowing only the named placeholders. A placeholder may appear more than once. Literal
// braces are refused: a relay whose path needs one can write it percent-encoded.
func parseTemplate(what, text string, variables ...string) (tmpl, error) {
	if text == "" {
		return tmpl{}, fmt.Errorf("%s is empty", what)
	}
	allowed := make(map[string]bool, len(variables))
	for _, v := range variables {
		allowed[v] = true
	}
	var t tmpl
	for text != "" {
		open := strings.IndexByte(text, '{')
		close := strings.IndexByte(text, '}')
		if close >= 0 && (open < 0 || close < open) {
			return tmpl{}, fmt.Errorf("%s %q has a } without a {", what, text)
		}
		if open < 0 {
			t.parts = append(t.parts, tmplPart{literal: text})
			break
		}
		if open > 0 {
			t.parts = append(t.parts, tmplPart{literal: text[:open]})
		}
		end := strings.IndexByte(text[open:], '}')
		if end < 0 {
			return tmpl{}, fmt.Errorf("%s has a { that does not close", what)
		}
		name := text[open+1 : open+end]
		if !allowed[name] {
			return tmpl{}, fmt.Errorf("%s %q names {%s}, which it cannot fill", what, text, name)
		}
		t.parts = append(t.parts, tmplPart{variable: name})
		text = text[open+end+1:]
	}
	return t, nil
}

// expand fills the placeholders. Path values are escaped as one path segment: a ref or group name with a slash, a
// question mark or a space cannot change the shape of the request.
func (t tmpl) expand(values map[string]string) string {
	var b strings.Builder
	for _, p := range t.parts {
		if p.variable == "" {
			b.WriteString(p.literal)
			continue
		}
		b.WriteString(url.PathEscape(values[p.variable]))
	}
	return b.String()
}

// raw fills the placeholders without escaping, for a JSON body member where the encoder does the escaping.
func (t tmpl) raw(values map[string]string) string {
	var b strings.Builder
	for _, p := range t.parts {
		if p.variable == "" {
			b.WriteString(p.literal)
			continue
		}
		b.WriteString(values[p.variable])
	}
	return b.String()
}

// pathTemplate is a template that must be a path below the supplier's origin.
func pathTemplate(what, text string, variables ...string) (tmpl, error) {
	if !strings.HasPrefix(text, "/") {
		return tmpl{}, errors.New(what + " must start with /")
	}
	for _, r := range text {
		if r <= ' ' || r == 0x7f || r == '#' {
			return tmpl{}, fmt.Errorf("%s %q has a character a request path cannot carry", what, text)
		}
	}
	return parseTemplate(what, text, variables...)
}
