package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // the request's members are copied as written, never decoded and encoded again
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"axiom/internal/platform/jsonx"
)

// WebSearchMaxBytes bounds what a web plugin may add to a Chat body: the injected system message, escaped. The
// gateway reserves input for it before the search runs, because the results are not in the body the request was
// admitted with (docs/domains/billing.md「预留与结算」).
const WebSearchMaxBytes = 40 << 10

// DefaultWebSearchResults is OpenRouter's default max_results for the web plugin (OpenRouter《Web Search》, read
// 2026-10-31).
const DefaultWebSearchResults = 5

// MaxWebSearchQueryBytes bounds the search text taken from the last user message. A longer message is refused rather
// than searched in part: a silently shortened query answers a different question.
const MaxWebSearchQueryBytes = 1024

// WebPlugin is the web plugin as the request asked for it. The gateway runs one search through its configured
// backend and injects the results before the last user turn; Online marks the deprecated :online variant, whose
// refusal differs while no backend is configured.
type WebPlugin struct {
	MaxResults     int
	SearchPrompt   string
	IncludeDomains []string
	ExcludeDomains []string
	// Engine is the search backend the client chose: "native" for the model provider's own search, or an enabled
	// backend's name. Empty leaves the choice to the gateway (docs/domains/gateway.md「联网搜索」).
	Engine string
	Online bool
}

// MaxEngineNameBytes bounds a search engine name. Backends are deployment configuration, so the name only has to be
// short and unambiguous; whether one exists is the gateway's question, not the parser's.
const MaxEngineNameBytes = 32

// ValidEngineName reports whether name has the shape of a backend name: lowercase letters, digits, '-' and '_',
// starting with a letter or digit. It is deliberately not a set lookup.
func ValidEngineName(name string) bool {
	if name == "" || len(name) > MaxEngineNameBytes || !isLowerAlnum(name[0]) {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; !isLowerAlnum(c) && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func isLowerAlnum(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }

// ChatSearchQuery returns the text of the last user message, the query a web search runs for. It walks the body's
// tokens instead of decoding the whole request: a long history is read once and only the last user message's text is
// kept, bounded by MaxWebSearchQueryBytes. A Chat body without a user message, or with non-text parts only, gives "".
func ChatSearchQuery(body []byte) (string, error) {
	r := reader{dec: jsontext.NewDecoder(bytes.NewBuffer(body))}
	if k := r.dec.PeekKind(); k != '{' {
		return "", fmt.Errorf("%w: a Chat request is not an object", ErrInvalid)
	}
	if err := r.token(); err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	var query string
	for r.dec.PeekKind() != '}' {
		if err := r.member(); err != nil {
			return "", fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
		}
		if string(r.name) != "messages" {
			if err := r.skip(); err != nil {
				return "", fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
			}
			continue
		}
		q, err := r.searchMessages()
		if err != nil {
			return "", err
		}
		query = q
	}
	if err := r.token(); err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	return strings.TrimSpace(query), nil
}

// searchMessages reads messages and returns the last user message's text. The text of every earlier user message is
// dropped as soon as the next message begins, so the scan's own working set is one message, not the history.
func (r *reader) searchMessages() (string, error) {
	ok, err := r.open('[', "messages")
	if err != nil {
		return "", fmt.Errorf("%w: messages is not an array", ErrInvalid)
	}
	if !ok {
		return "", nil
	}
	var last string
	var tooLong bool
	for r.dec.PeekKind() != ']' {
		q, over, role, err := r.searchMessage()
		if err != nil {
			return "", err
		}
		if role == "user" {
			last, tooLong = q, over
		}
	}
	if err := r.token(); err != nil {
		return "", queryFault(err)
	}
	if tooLong {
		return "", fmt.Errorf("%w: the search query is longer than %d bytes", ErrInvalid, MaxWebSearchQueryBytes)
	}
	return last, nil
}

// searchMessage reads one message's role and content text; every other member is skipped. A too-long content is a
// flag, not an error: only the last user message is the query, so an earlier long message and a non-user message
// must not refuse the request, whatever order their members came in.
func (r *reader) searchMessage() (text string, over bool, role string, err error) {
	if r.dec.PeekKind() != '{' {
		return "", false, "", fmt.Errorf("%w: a message is not an object", ErrInvalid)
	}
	if err := r.token(); err != nil {
		return "", false, "", queryFault(err)
	}
	for r.dec.PeekKind() != '}' {
		if err := r.member(); err != nil {
			return "", false, "", queryFault(err)
		}
		switch string(r.name) {
		case "role":
			role, err = r.searchStringField("a message's role")
		case "content":
			text, over, err = r.searchMemberContent(role)
		default:
			err = r.skip()
		}
		if err != nil {
			return "", false, "", queryFault(err)
		}
	}
	return text, over, role, queryFault(r.token())
}

// searchMemberContent reads a message's content as the query's text, or skips it when the message cannot be the last
// user turn.
func (r *reader) searchMemberContent(role string) (string, bool, error) {
	if role != "" && role != "user" {
		return "", false, queryFault(r.skip())
	}
	return r.searchContent()
}

// searchStringField reads a member's string value, naming the member in the error a body that writes another type
// gets.
func (r *reader) searchStringField(what string) (string, error) {
	v, err := r.str()
	if err != nil {
		return "", fmt.Errorf("%w: %s is not a string", ErrInvalid, what)
	}
	return string(v), nil
}

// searchContent reads a message's content as its text parts, or null. A string is one text part; an array's parts
// contribute only when their type is text. Content over the query bound is discarded and reported with over, so the
// caller can decide after it knows the message's role.
func (r *reader) searchContent() (string, bool, error) {
	k := r.dec.PeekKind()
	if k == 'n' {
		return "", false, queryFault(r.token())
	}
	if k == '"' {
		return r.searchString()
	}
	if k != '[' {
		return "", false, fmt.Errorf("%w: a message's content is not a string or an array", ErrInvalid)
	}
	return r.searchParts()
}

// searchParts reads an array of content parts, keeping the text parts up to the query bound. A part over the bound
// discards the whole content, and its text with it, so the query a search runs is never a shortened question.
func (r *reader) searchParts() (string, bool, error) {
	if err := r.token(); err != nil {
		return "", false, queryFault(err)
	}
	var b strings.Builder
	var over bool
	for r.dec.PeekKind() != ']' {
		kind, text, partOver, err := r.searchPart()
		if err != nil {
			return "", false, err
		}
		if kind == "text" {
			over = over || partOver || appendSearchText(&b, text)
		}
	}
	return b.String(), over, queryFault(r.token())
}

// searchPart reads one content part's type and text; every other member is skipped.
func (r *reader) searchPart() (kind, text string, over bool, err error) {
	if r.dec.PeekKind() != '{' {
		return "", "", false, fmt.Errorf("%w: a content part is not an object", ErrInvalid)
	}
	if err := r.token(); err != nil {
		return "", "", false, queryFault(err)
	}
	for r.dec.PeekKind() != '}' {
		if err := r.member(); err != nil {
			return "", "", false, queryFault(err)
		}
		switch string(r.name) {
		case "type":
			kind, err = r.searchStringField("a content part's type")
		case "text":
			text, over, err = r.searchString()
		default:
			err = r.skip()
		}
		if err != nil {
			return "", "", false, queryFault(err)
		}
	}
	return kind, text, over, queryFault(r.token())
}

// searchString reads one string and bounds it before unquoting. The bound is a little over the query limit because
// escapes may shrink a value; a string that is plainly too large is reported without building the unescaped copy.
func (r *reader) searchString() (string, bool, error) {
	v, err := r.raw()
	if err != nil {
		return "", false, fmt.Errorf("%w: a message's text is not a string", ErrInvalid)
	}
	if v == nil {
		return "", false, nil
	}
	if len(v) > 6*MaxWebSearchQueryBytes+2 {
		return "", true, nil
	}
	b, err := r.unquote(v)
	if err != nil {
		return "", false, fmt.Errorf("%w: a message's text is not a string", ErrInvalid)
	}
	if len(b) > MaxWebSearchQueryBytes {
		return "", true, nil
	}
	return string(b), false, nil
}

// queryFault wraps what reading the query hit as an invalid body, keeping an error that already says what is wrong:
// every reader below reports its own message, and a token error is the body's fault.
func queryFault(err error) error {
	if err == nil || errors.Is(err, ErrInvalid) {
		return err
	}
	return fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
}

// appendSearchText adds a text part, separating parts by a newline as ChatSearchQuery always has, and reports
// whether the result crossed the query bound. The newline is counted too, so the final query stays within the bound.
func appendSearchText(b *strings.Builder, text string) bool {
	if text == "" {
		return false
	}
	need := len(text)
	if b.Len() > 0 {
		need++
	}
	if b.Len()+need > MaxWebSearchQueryBytes {
		return true
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(text)
	return false
}

// InjectWebSearch puts text in a system message before the first user message of a Chat body: leading system messages
// survive a conversion to a format that only takes them there, while the search still answered the last user turn.
// Every other member is copied as written. The added bytes, escaped, must not exceed WebSearchMaxBytes.
func InjectWebSearch(body []byte, text string) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("%w: search results are not valid UTF-8", ErrInvalid)
	}
	var doc map[string]jsontext.Value
	if err := jsonx.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	raw, ok := doc["messages"]
	if !ok {
		return nil, fmt.Errorf("%w: a Chat request has no messages", ErrInvalid)
	}
	var messages []jsontext.Value
	if err := jsonx.Unmarshal(raw, &messages); err != nil {
		return nil, fmt.Errorf("%w: messages is not an array", ErrInvalid)
	}
	at := len(messages)
	for i, m := range messages {
		var role struct {
			Role string `json:"role"`
		}
		if err := jsonx.Unmarshal(m, &role); err != nil {
			return nil, fmt.Errorf("%w: a message is not an object", ErrInvalid)
		}
		if role.Role == "user" {
			at = i
			break
		}
	}
	quoted, err := jsonx.MarshalCanonical(text)
	if err != nil {
		return nil, err
	}
	message := make([]byte, 0, len(quoted)+32)
	message = append(message, `{"role":"system","content":`...)
	message = append(message, quoted...)
	message = append(message, '}')
	messages = append(messages, nil)
	copy(messages[at+1:], messages[at:])
	messages[at] = message
	if raw, err = jsonx.MarshalCanonical(messages); err != nil {
		return nil, err
	}
	doc["messages"] = raw
	out, err := jsonx.MarshalCanonical(doc)
	if err != nil {
		return nil, err
	}
	if len(out)-len(body) > WebSearchMaxBytes {
		return nil, fmt.Errorf("%w: the injected search results are too large", ErrInvalid)
	}
	return out, nil
}

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

// ValidSearchDomain reports whether d is a host name the search query can carry as a site: filter: letters, digits,
// dots and hyphens, no wildcard, path or scheme, and at least one dot. Checking here keeps a client's option from
// writing search operators of its own.
func ValidSearchDomain(d string) bool {
	if len(d) == 0 || len(d) > 253 || !utf8.ValidString(d) || strings.Contains(d, "..") {
		return false
	}
	if strings.ContainsFunc(d, invalidHostRune) {
		return false
	}
	for _, edge := range []string{".", "-"} {
		if strings.HasPrefix(d, edge) || strings.HasSuffix(d, edge) {
			return false
		}
	}
	return strings.Contains(d, ".")
}

// invalidHostRune reports a character no host name has: a label is letters, digits and hyphens.
func invalidHostRune(r rune) bool {
	letter, digit := 'a' <= r && r <= 'z', '0' <= r && r <= '9'
	return !letter && !digit && r != '-' && r != '.'
}

// ErrEmptySearchQuery reports a web request without text to search for.
var ErrEmptySearchQuery = errors.New("the web plugin needs a user message with text to search for")

// CheckWebSearch validates the options a request's web plugin carried, at the boundary where they arrive.
func CheckWebSearch(w *WebPlugin) error {
	if w == nil {
		return nil
	}
	if w.MaxResults < 0 || w.MaxResults > 10 {
		return fmt.Errorf("%w: max_results must be between 1 and 10", ErrInvalid)
	}
	if w.MaxResults == 0 {
		w.MaxResults = DefaultWebSearchResults
	}
	if len(w.SearchPrompt) > 512 || !utf8.ValidString(w.SearchPrompt) {
		return fmt.Errorf("%w: search_prompt must be valid UTF-8 with at most 512 bytes", ErrInvalid)
	}
	for _, d := range w.IncludeDomains {
		if !ValidSearchDomain(d) {
			return fmt.Errorf("%w: include_domains names a domain that is not a plain host name", ErrInvalid)
		}
	}
	for _, d := range w.ExcludeDomains {
		if !ValidSearchDomain(d) {
			return fmt.Errorf("%w: exclude_domains names a domain that is not a plain host name", ErrInvalid)
		}
	}
	return nil
}
