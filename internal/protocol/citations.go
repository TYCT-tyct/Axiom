package protocol

import (
	"unicode/utf8"

	"axiom/internal/platform/jsonx"
)

// citationMemory is what generating annotations may hold beyond the content it reads: the unescaped copy and the
// appended annotation entries. The content itself is charged at citationFactor when the reply may be cited.
const citationMemory = 64 << 10

// citationFactor is how many copies of a cited content piece the aggregate keeps room for: its stored escaped form,
// the unescaped copy cite builds and the annotations it appends.
const citationFactor = 3

// cite adds the url_citation annotations of the links in the choice's content that match a supplied search result.
// It runs once, when the reply is finished, because only then is the whole message known; a link the model wrote to a
// page the search did not return is the model's own and gets no annotation. The content is kept escaped, so it is
// unquoted here before indexes are counted in characters rather than bytes.
func (a *chatAggregate) cite(citations []Citation) {
	if len(citations) == 0 {
		return
	}
	for i := range a.choices {
		c := &a.choices[i]
		if len(c.strings[pieceContent]) == 0 {
			continue
		}
		var text string
		if err := jsonx.Unmarshal([]byte(`"`+string(c.strings[pieceContent])+`"`), &text); err != nil {
			continue
		}
		entries := CitationAnnotations(text, citations)
		if len(entries) == 0 {
			continue
		}
		if len(c.annotations) > 0 {
			c.annotations = append(c.annotations, ',')
		}
		c.annotations = append(c.annotations, entries...)
	}
}

// Citation is one web search result the gateway supplied, for the url_citation annotations of a Chat reply.
type Citation struct {
	URL, Title, Content string
}

// maxCitationAnnotations bounds the annotations one reply carries: a long answer links a few pages, and the reply is
// already the largest body the gateway builds.
const maxCitationAnnotations = 32

// CitationAnnotations returns the annotations that cite the results in a reply's text: every markdown link whose URL
// is one of the results, as OpenRouter reports its web plugin's citations (OpenRouter《Web Search》, read 2026-10-31).
// start_index and end_index are character (rune) offsets into the message's content, the whole link included.
func CitationAnnotations(content string, citations []Citation) []byte {
	byURL := make(map[string]Citation, len(citations))
	for _, c := range citations {
		byURL[c.URL] = c
	}
	var out []byte
	runes, count := 0, 0
	for i := 0; i < len(content) && count < maxCitationAnnotations; {
		if content[i] == '[' {
			if entry, end, ok := citedAnnotation(content, i, runes, byURL); ok {
				if len(out) > 0 {
					out = append(out, ',')
				}
				out = append(out, entry...)
				count++
				runes += utf8.RuneCountInString(content[i:end])
				i = end
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(content[i:])
		runes++
		i += size
	}
	return out
}

// citedAnnotation is the url_citation entry of a link that starts at i and cites one of the results, and the index
// just past its closing parenthesis. at is the link's offset in characters, which the entry reports.
func citedAnnotation(content string, i, at int, byURL map[string]Citation) (entry []byte, end int, ok bool) {
	start, stop, ok := markdownLink(content, i)
	if !ok {
		return nil, 0, false
	}
	c, found := byURL[content[start:stop]]
	if !found {
		return nil, 0, false
	}
	end = stop + 1 // the closing parenthesis
	entry, err := jsonx.MarshalCanonical(map[string]any{
		"type": "url_citation",
		"url_citation": map[string]any{
			"url": c.URL, "title": c.Title, "content": c.Content,
			"start_index": at, "end_index": at + utf8.RuneCountInString(content[i:end]),
		},
	})
	if err != nil {
		return nil, 0, false
	}
	return entry, end, true
}

// markdownLink reads a link that starts at the '[' at i: [text](url), returning the url's bounds and whether the
// shape is complete. Nesting brackets in the text are allowed; a link without a closing parenthesis is not a link.
func markdownLink(s string, i int) (start, end int, ok bool) {
	close := matchingBracket(s, i)
	if close < 0 || close+1 >= len(s) || s[close+1] != '(' {
		return 0, 0, false
	}
	start = close + 2
	for k := start; k < len(s); k++ {
		switch s[k] {
		case ')':
			return start, k, true
		case ' ', '\n':
			return 0, 0, false
		}
	}
	return 0, 0, false
}

// matchingBracket is the index of the ']' that closes the '[' at i, counting nested brackets and skipping an escaped
// character, or -1 when there is none.
func matchingBracket(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return j
			}
		case '\\':
			j++
		}
	}
	return -1
}
