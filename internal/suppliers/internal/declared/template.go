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
	query    bool // this part is after the first ? in the template
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
	inQuery := false
	for text != "" {
		open := strings.IndexByte(text, '{')
		close := strings.IndexByte(text, '}')
		if close >= 0 && (open < 0 || close < open) {
			return tmpl{}, fmt.Errorf("%s %q has a } without a {", what, text)
		}
		if open < 0 {
			addLiteral(&t.parts, text, inQuery)
			break
		}
		if open > 0 {
			inQuery = addLiteral(&t.parts, text[:open], inQuery)
		}
		end := strings.IndexByte(text[open:], '}')
		if end < 0 {
			return tmpl{}, fmt.Errorf("%s has a { that does not close", what)
		}
		name := text[open+1 : open+end]
		if !allowed[name] {
			return tmpl{}, fmt.Errorf("%s %q names {%s}, which it cannot fill", what, text, name)
		}
		t.parts = append(t.parts, tmplPart{variable: name, query: inQuery})
		text = text[open+end+1:]
	}
	return t, nil
}

// addLiteral appends one literal part and reports whether a ? in it moves the following placeholders into the query.
func addLiteral(parts *[]tmplPart, text string, query bool) bool {
	*parts = append(*parts, tmplPart{literal: text, query: query})
	return query || strings.ContainsRune(text, '?')
}

// expand fills the placeholders. Values before the first ? are escaped as one path segment; values in the query are
// escaped as query values, where +, &, = and spaces have different meanings. A ref or group name therefore cannot
// change which resource or query member the request names.
func (t tmpl) expand(values map[string]string) string {
	var b strings.Builder
	for _, p := range t.parts {
		if p.variable == "" {
			b.WriteString(p.literal)
			continue
		}
		if p.query {
			b.WriteString(url.QueryEscape(values[p.variable]))
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

// pathTemplate is a template that must be a path below the supplier's origin. A leading // is a network-path
// reference, and a bad percent escape fails URL parsing; both would otherwise be found only on the first request.
func pathTemplate(what, text string, variables ...string) (tmpl, error) {
	if !strings.HasPrefix(text, "/") {
		return tmpl{}, errors.New(what + " must start with /")
	}
	if strings.HasPrefix(text, "//") {
		return tmpl{}, fmt.Errorf("%s %q must start with a single /", what, text)
	}
	for _, r := range text {
		if r <= ' ' || r == 0x7f || r == '#' {
			return tmpl{}, fmt.Errorf("%s %q has a character a request path cannot carry", what, text)
		}
	}
	if !validEscapes(text) {
		return tmpl{}, fmt.Errorf("%s %q has an invalid percent escape", what, text)
	}
	return parseTemplate(what, text, variables...)
}

// validEscapes reports whether every % in a template is followed by two hexadecimal digits. The values filled into
// placeholders are escaped separately; this only validates the literal path the declaration wrote.
func validEscapes(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != '%' {
			continue
		}
		if i+2 >= len(text) || !isHex(text[i+1]) || !isHex(text[i+2]) {
			return false
		}
		i += 2
	}
	return true
}

func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}
