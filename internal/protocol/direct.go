package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: the body is read token by token
	"fmt"
	"strconv"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// The direct formats answer one JSON body: embeddings and rerank. Their request is read and rewritten member by member,
// without the grammar shared by the three chat formats, which describes content parts and tools that neither has
// (README.md「直接调用的格式」). Legacy completions are generated like Chat and read by its own stream reader
// (completions.go), but their request has no message parts either, so they share this reader.

// inspectNew reads a request of a direct format, or of legacy completions.
func inspectNew(f Format, body []byte) (Request, error) {
	if err := jsonx.Validate(body); err != nil {
		return Request{}, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	d := directScanner{dec: jsontext.NewDecoder(bytes.NewBuffer(body))}
	d.r = Request{Format: f, Choices: 1, InputItems: 1 << catalog.ItemInput, ServiceTiers: 1 << catalog.ServiceTierStandard}
	if err := d.top(); err != nil {
		return Request{}, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	if d.r.Model == "" {
		return Request{}, fmt.Errorf("%w: model is missing", ErrInvalid)
	}
	switch d.r.Format {
	case Embeddings:
		if !d.hasInput {
			return Request{}, fmt.Errorf("%w: input is missing", ErrInvalid)
		}
	case Rerank:
		if !d.hasQuery {
			return Request{}, fmt.Errorf("%w: query is missing", ErrInvalid)
		}
		if !d.hasDocs {
			return Request{}, fmt.Errorf("%w: documents are missing", ErrInvalid)
		}
		// A rerank is one search: its usage reports search_units, which billing charges by the web_search item. The
		// reservation covers one; the request does not need the model's web_search ability, which is for tools.
		d.r.WebSearches = 1
	case Completions:
		if !d.hasPrompt {
			return Request{}, fmt.Errorf("%w: prompt is missing", ErrInvalid)
		}
		if d.prompts > 1 && d.r.Choices > (1<<62)/d.prompts {
			return Request{}, fmt.Errorf("%w: prompt and n ask for too many completions", ErrInvalid)
		}
		d.r.Choices *= max(1, d.prompts)
	}
	return d.r, nil
}

// directScanner reads the top-level members of a request of a direct format. The whole body was validated first, so
// skipping a member only consumes it: its depth and syntax are already known to be jsonx's.
type directScanner struct {
	reader
	r                                      Request
	prompts                                int64 // legacy completions' prompts, which each generate Choices completions
	hasInput, hasQuery, hasDocs, hasPrompt bool
}

func (d *directScanner) top() error {
	if d.dec.PeekKind() != '{' {
		d.name = []byte("the body")
		return d.mismatch("an object")
	}
	if err := d.token(); err != nil {
		return err
	}
	for d.dec.PeekKind() != '}' {
		if err := d.member(); err != nil {
			return err
		}
		var err error
		switch d.r.Format {
		case Embeddings:
			err = d.embeddingsMember()
		case Rerank:
			err = d.rerankMember()
		case Completions:
			err = d.completionsMember()
		}
		if err != nil {
			return err
		}
	}
	return d.token()
}

func (d *directScanner) embeddingsMember() error {
	switch string(d.name) {
	case "model":
		return d.model()
	case "user":
		return d.skip()
	case "provider":
		return d.provider()
	case "session_id":
		return d.skip()
	case "input":
		d.hasInput = true
		return d.embeddingInput()
	case "input_type":
		_, err := d.str()
		return err
	case "encoding_format":
		v, err := d.str()
		if err != nil {
			return err
		}
		if string(v) != "float" && string(v) != "base64" {
			return fmt.Errorf("encoding_format is %q, want float or base64", v)
		}
		return nil
	case "dimensions":
		n, err := d.count()
		if err == nil && n == 0 {
			return fmt.Errorf("dimensions is 0, want a positive integer")
		}
		return err
	case "stream":
		if err := d.noStream(); err != nil {
			return err
		}
		return nil
	default:
		return d.skip()
	}
}

func (d *directScanner) rerankMember() error {
	switch string(d.name) {
	case "model":
		return d.model()
	case "user":
		return d.skip()
	case "provider":
		return d.provider()
	case "session_id":
		return d.skip()
	case "query":
		d.hasQuery = true
		return d.textBytes()
	case "documents":
		d.hasDocs = true
		return d.documents()
	case "top_n":
		n, err := d.count()
		if err == nil && n == 0 {
			return fmt.Errorf("top_n is 0, want a positive integer")
		}
		return err
	case "stream":
		return d.noStream()
	default:
		return d.skip()
	}
}

func (d *directScanner) completionsMember() error {
	switch string(d.name) {
	case "model":
		return d.model()
	case "user":
		return d.skip()
	case "provider":
		return d.provider()
	case "session_id":
		return d.skip()
	case "prompt":
		d.hasPrompt = true
		return d.prompt()
	case "max_tokens":
		n, err := d.count()
		d.r.MaxOutput = max(d.r.MaxOutput, n)
		return err
	case "n":
		n, err := d.count()
		d.r.Choices = max(1, n)
		return err
	case "stream":
		k, err := d.flag()
		d.r.Stream = k == 't'
		return err
	case "prompt_cache_key", "safety_identifier":
		return d.skip()
	default:
		return d.skip()
	}
}

// noStream refuses a stream request: neither direct format streams one reply as events, and forwarding the member
// would make the upstream refuse a request the gateway could have answered.
func (d *directScanner) noStream() error {
	k, err := d.flag()
	if err == nil && k == 't' {
		return fmt.Errorf("%s does not stream", d.r.Format.name())
	}
	return err
}

// model reads the model member, which must be a string. The name is resolved by the gateway.
func (d *directScanner) model() error {
	v, err := d.str()
	if err != nil {
		return err
	}
	d.r.Model = string(v)
	return nil
}

// provider keeps the provider object as written, for the gateway: at most maxProvider bytes, as in the chat formats.
func (d *directScanner) provider() error {
	if d.dec.PeekKind() != '{' {
		return d.mismatch("an object")
	}
	v, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	if len(v) > maxProvider {
		return fmt.Errorf("provider is %d bytes, more than %d", len(v), maxProvider)
	}
	d.r.Provider = bytes.Clone(v)
	return nil
}

// textBytes adds a string value's length to the text the input may be billed for.
func (d *directScanner) textBytes() error {
	v, err := d.str()
	d.r.TextBytes += int64(len(v))
	return err
}

// embeddingInput reads OpenAI's input: a string, an array of strings, a token array, an array of token arrays, or an
// array of objects whose content holds text and media parts (OpenAI《Create embeddings》, OpenRouter《Embeddings》,
// read 2026-10-30).
func (d *directScanner) embeddingInput() error {
	v, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	switch v.Kind() {
	case '"':
		d.r.TextBytes += int64(unquotedLen(v))
		return nil
	case '[':
		return d.embeddingArray(v)
	default:
		return fmt.Errorf("input is %s, want a string or an array", kindName(v))
	}
}

func (d *directScanner) embeddingArray(v jsontext.Value) error {
	elems, err := elements(v)
	if err != nil {
		return err
	}
	if len(elems) == 0 {
		return fmt.Errorf("input is an empty array")
	}
	switch elems[0].Kind() {
	case '0':
		return d.tokens(elems)
	case '"':
		for _, e := range elems {
			if e.Kind() != '"' {
				return fmt.Errorf("input mixes strings with other values")
			}
			d.r.TextBytes += int64(unquotedLen(e))
		}
		return nil
	case '[':
		return d.tokenArrays(elems)
	case '{':
		for _, e := range elems {
			if e.Kind() != '{' {
				return fmt.Errorf("input mixes objects with other values")
			}
			if err := d.embeddingObject(e); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("input has an element of the wrong type")
	}
}

// tokens counts a token array: its tokens are exact, so the reservation is the bytes a token takes at the estimate's
// most (billing.InputBytesPerToken is 4), which never undercounts them.
func (d *directScanner) tokens(elems []jsontext.Value) error {
	for _, e := range elems {
		if e.Kind() != '0' {
			return fmt.Errorf("input mixes tokens with other values")
		}
	}
	d.r.TextBytes += int64(len(elems)) * 4
	return nil
}

func (d *directScanner) tokenArrays(elems []jsontext.Value) error {
	for _, e := range elems {
		if e.Kind() != '[' {
			return fmt.Errorf("input mixes token arrays with other values")
		}
		inner, err := elements(e)
		if err != nil {
			return err
		}
		if err := d.tokens(inner); err != nil {
			return err
		}
	}
	return nil
}

// embeddingObject reads one {content: [...]} input object and its parts.
func (d *directScanner) embeddingObject(v jsontext.Value) error {
	content, ok, err := member(v, "content")
	if err != nil {
		return err
	}
	if !ok || content.Kind() != '[' {
		return fmt.Errorf("input object has no content array")
	}
	parts, err := elements(content)
	if err != nil {
		return err
	}
	for _, part := range parts {
		if part.Kind() != '{' {
			return fmt.Errorf("content part is not an object")
		}
		kind, _, err := member(part, "type")
		if err != nil {
			return err
		}
		switch string(kind) {
		case `"text"`:
			text, ok, err := member(part, "text")
			if err != nil {
				return err
			}
			if !ok || text.Kind() != '"' {
				return fmt.Errorf("text part has no text string")
			}
			d.r.TextBytes += int64(unquotedLen(text))
		case `"image_url"`:
			d.r.Images++
			d.r.Needs |= Images
		case `"file"`:
			d.r.Documents++
			d.r.Needs |= Documents
		case `"input_audio"`, `"input_video"`:
			// Their size is not text and the catalog has no separate media bound: a document's 4096 tokens is larger
			// than an image's 1600, so counting them as files keeps the reservation above what they can take.
			d.r.Documents++
			d.r.Needs |= Documents
		default:
			return fmt.Errorf("content part type %s is not supported", kind)
		}
	}
	return nil
}

// documents reads rerank's documents: strings, or objects with text and/or image.
func (d *directScanner) documents() error {
	v, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	if v.Kind() != '[' {
		return fmt.Errorf("documents is %s, want an array", kindName(v))
	}
	elems, err := elements(v)
	if err != nil {
		return err
	}
	if len(elems) == 0 {
		return fmt.Errorf("documents is empty")
	}
	for _, e := range elems {
		switch e.Kind() {
		case '"':
			d.r.TextBytes += int64(unquotedLen(e))
		case '{':
			text, hasText, err := member(e, "text")
			if err != nil {
				return err
			}
			image, hasImage, err := member(e, "image")
			if err != nil {
				return err
			}
			if !hasText && !hasImage {
				return fmt.Errorf("a document has neither text nor image")
			}
			if hasText {
				if text.Kind() != '"' {
					return fmt.Errorf("a document's text is not a string")
				}
				d.r.TextBytes += int64(unquotedLen(text))
			}
			if hasImage {
				if image.Kind() != '"' {
					return fmt.Errorf("a document's image is not a string")
				}
				d.r.Images++
				d.r.Needs |= Images
			}
		default:
			return fmt.Errorf("documents has an element of the wrong type")
		}
	}
	return nil
}

// prompt reads legacy completions' prompt: a string, an array of strings, a token array, an array of token arrays or an
// array of mixed prompts, which generates one completion per prompt.
func (d *directScanner) prompt() error {
	v, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	switch v.Kind() {
	case '"':
		d.r.TextBytes += int64(unquotedLen(v))
		return nil
	case '[':
		elems, err := elements(v)
		if err != nil {
			return err
		}
		if len(elems) == 0 {
			return fmt.Errorf("prompt is an empty array")
		}
		d.prompts = int64(len(elems))
		for _, e := range elems {
			switch e.Kind() {
			case '"':
				d.r.TextBytes += int64(unquotedLen(e))
			case '0':
				d.r.TextBytes += 4
			case '[':
				inner, err := elements(e)
				if err != nil {
					return err
				}
				d.r.TextBytes += int64(len(inner)) * 4
			default:
				return fmt.Errorf("prompt has an element of the wrong type")
			}
		}
		return nil
	default:
		return fmt.Errorf("prompt is %s, want a string or an array", kindName(v))
	}
}

// elements reads the elements of a JSON array value.
func elements(v jsontext.Value) ([]jsontext.Value, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	var out []jsontext.Value
	for dec.PeekKind() != ']' {
		e, err := dec.ReadValue()
		if err != nil {
			return nil, err
		}
		out = append(out, bytes.Clone(e))
	}
	return out, nil
}

// member reads the member name of an object value.
func member(v jsontext.Value, name string) (jsontext.Value, bool, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	if _, err := dec.ReadToken(); err != nil {
		return nil, false, err
	}
	for dec.PeekKind() != '}' {
		n, err := dec.ReadValue()
		if err != nil {
			return nil, false, err
		}
		key, err := jsontext.AppendUnquote(nil, n)
		if err != nil {
			return nil, false, err
		}
		value, err := dec.ReadValue()
		if err != nil {
			return nil, false, err
		}
		if string(key) == name {
			return bytes.Clone(value), true, nil
		}
	}
	return nil, false, nil
}

func kindName(v jsontext.Value) string {
	switch v.Kind() {
	case '"':
		return "a string"
	case '0':
		return "a number"
	case '[':
		return "an array"
	case '{':
		return "an object"
	case 't', 'f':
		return "a boolean"
	case 'n':
		return "null"
	}
	return "not a value"
}

// name is what a format is called in an error a client reads.
func (f Format) name() string {
	switch f {
	case Completions:
		return "completions"
	case Embeddings:
		return "embeddings"
	case Rerank:
		return "rerank"
	}
	return "the request"
}

// directRewriteRules are the members a direct format's request rewrites: the model, the identifiers, and the members
// that route rather than describe the call. Legacy completions also ask for a stream, as Chat does.
var directRewriteRules = map[Format]formatRules{
	Embeddings: {
		members: withRouting(map[string]rule{
			"model": (*rewriter).model, "user": (*rewriter).identity, "stream": (*rewriter).drop,
			"prompt_cache_key": (*rewriter).drop, "safety_identifier": (*rewriter).drop,
		}),
		identities: 1,
	},
	Rerank: {
		members: withRouting(map[string]rule{
			"model": (*rewriter).model, "user": (*rewriter).identity, "stream": (*rewriter).drop,
			"prompt_cache_key": (*rewriter).drop, "safety_identifier": (*rewriter).drop,
		}),
		identities: 1,
	},
	Completions: {
		members: withRouting(map[string]rule{
			"model": (*rewriter).model, "user": (*rewriter).identity, "prompt_cache_key": (*rewriter).identity,
			"safety_identifier": (*rewriter).identity, "stream": (*rewriter).stream,
		}),
		add: (*rewriter).addStream, identities: 3, room: len(addStream),
	},
}

// rewriteDirect rewrites a direct format's request for the endpoint.
func rewriteDirect(r *Request, body []byte, t Target, w Rewrites) ([]byte, error) {
	rules, ok := directRewriteRules[r.Format]
	if !ok {
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d is not rewritten yet", r.Format)}
	}
	w = w.withDefaults()
	p := &rewriter{dec: jsontext.NewDecoder(bytes.NewBuffer(body)), body: body, t: t, w: w}
	p.out = make([]byte, 0, rewriteSize(rules, len(body), t, w))
	if err := p.top(rules); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	return p.out, nil
}

// DirectUsage reads the usage of an embeddings or rerank reply, in the items the catalog prices. A reply without usage
// is an attempt that reported none, which billing estimates; a usage that cannot be read is Invalid, as UsageOf's is.
func DirectUsage(f Format, body []byte) (Usage, error) {
	v, err := usageValue(body)
	if err != nil {
		return Usage{}, err
	}
	if v == nil {
		return Usage{}, nil
	}
	d := directScanner{dec: jsontext.NewDecoder(bytes.NewReader(v))}
	d.r = Request{Format: f}
	d.name = []byte("usage")
	u, err := d.usageObject()
	if err != nil {
		return Usage{Invalid: err}, nil
	}
	return u, nil
}

// usageValue returns the reply's usage member, or nil when it has none.
func usageValue(body []byte) (jsontext.Value, error) {
	if len(body) == 0 || body[0] != '{' {
		return nil, fmt.Errorf("%w: a direct reply is not a JSON object", ErrUpstream)
	}
	v, _, err := member(body, "usage")
	return v, err
}

func (d *directScanner) usageObject() (Usage, error) {
	if d.dec.PeekKind() == 'n' {
		return Usage{}, nil
	}
	if d.dec.PeekKind() != '{' {
		return Usage{}, fmt.Errorf("usage is not an object")
	}
	if err := d.token(); err != nil {
		return Usage{}, err
	}
	var total, searches int64
	seen := catalog.ItemSet(0)
	for d.dec.PeekKind() != '}' {
		if err := d.member(); err != nil {
			return Usage{}, err
		}
		switch string(d.name) {
		case "prompt_tokens", "total_tokens":
			n, err := d.count()
			if err != nil {
				return Usage{}, err
			}
			total = max(total, n)
			seen |= 1 << catalog.ItemInput
		case "search_units":
			n, err := d.count()
			if err != nil {
				return Usage{}, err
			}
			searches = max(searches, n)
			seen |= 1 << catalog.ItemWebSearch
		default:
			if err := d.skip(); err != nil {
				return Usage{}, err
			}
		}
	}
	if err := d.token(); err != nil {
		return Usage{}, err
	}
	counts := map[catalog.Item]int64{}
	if total > 0 {
		counts[catalog.ItemInput] = total
	}
	if searches > 0 {
		counts[catalog.ItemWebSearch] = searches
	}
	u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
	if err != nil {
		return Usage{}, err
	}
	return Usage{Segments: []Segment{{Usage: u, Reported: seen}}}, nil
}

// ValidateDirectReply checks that a direct reply has the member its format answers with, before the gateway settles it:
// a reply the upstream broke is retried on another endpoint while nothing has been committed.
func ValidateDirectReply(f Format, body []byte) error {
	_, err := replyMembers(f, body)
	return err
}

// RewriteDirectReply writes the reply a direct call's client gets, from the settlement fin: the upstream's data or
// results, Axiom's call ID and model, and the usage and cost the settlement charged. A pending settlement has no cost
// and says so, as the streams' endings do.
func RewriteDirectReply(f Format, body []byte, id, model string, seen Usage, fin *Final) ([]byte, error) {
	u, costString, top := reply(seen), "", ""
	if fin == nil || fin.Pending {
		top = `,"axiom":{"billing_pending":true}`
	} else {
		u, costString = reply(fin.Usage), cost(fin, f)
	}
	return rewriteReply(f, body, id, model, u, costString, top)
}

// rewriteReply writes the reply a direct call's client gets: the upstream's, with its identity and cost replaced by
// Axiom's, in the shape OpenRouter documents for the format (OpenRouter《Embeddings》 and 《Rerank》, read 2026-10-30).
func rewriteReply(f Format, body []byte, id, model string, u catalog.Usage, cost, top string) ([]byte, error) {
	members, err := replyMembers(f, body)
	if err != nil {
		return nil, err
	}
	size := len(body) + len(id) + len(model) + len(cost) + 512
	out := make([]byte, 0, size)
	switch f {
	case Embeddings:
		out = append(out, `{"object":`...)
		out = append(out, members["object"]...)
		out = append(out, `,"data":`...)
		out = append(out, members["data"]...)
		out = append(out, `,"model":`...)
		out = appendQuoted(out, model)
		out = append(out, `,"usage":{`...)
		out = appendIntMember(out, "prompt_tokens", u.Count(catalog.ItemInput))
		out = appendIntMember(append(out, ','), "total_tokens", u.InputTotal())
		out = append(out, cost...)
		out = append(out, `,"is_byok":false}`...)
	case Rerank:
		out = append(out, `{"id":`...)
		out = appendQuoted(out, id)
		out = append(out, `,"model":`...)
		out = appendQuoted(out, model)
		out = append(out, `,"results":`...)
		out = append(out, members["results"]...)
		out = append(out, `,"usage":{`...)
		out = appendIntMember(out, "total_tokens", u.InputTotal())
		if n := u.Count(catalog.ItemWebSearch); n > 0 {
			out = appendIntMember(append(out, ','), "search_units", n)
		}
		out = append(out, cost...)
		out = append(out, `,"is_byok":false}`...)
	default:
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d is not a direct reply", f)}
	}
	if top != "" {
		out = append(out, top...)
	}
	return append(out, '}'), nil
}

// replyMembers reads the members a direct reply keeps: its own object and data (embeddings) or results (rerank). A
// reply without them is the upstream breaking its format.
func replyMembers(f Format, body []byte) (map[string]jsontext.Value, error) {
	if len(body) == 0 || body[0] != '{' {
		return nil, fmt.Errorf("%w: a direct reply is not a JSON object", ErrUpstream)
	}
	members := make(map[string]jsontext.Value, 4)
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	if _, err := dec.ReadToken(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	for dec.PeekKind() != '}' {
		n, err := dec.ReadValue()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
		}
		key, err := jsontext.AppendUnquote(nil, n)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
		}
		v, err := dec.ReadValue()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
		}
		switch string(key) {
		case "object", "data", "results":
			members[string(key)] = bytes.Clone(v)
		}
	}
	switch f {
	case Embeddings:
		if members["data"] == nil || members["data"].Kind() != '[' {
			return nil, fmt.Errorf("%w: an embeddings reply has no data array", ErrUpstream)
		}
		if members["object"] == nil || members["object"].Kind() != '"' {
			members["object"] = []byte(`"list"`)
		}
	case Rerank:
		if members["results"] == nil || members["results"].Kind() != '[' {
			return nil, fmt.Errorf("%w: a rerank reply has no results array", ErrUpstream)
		}
	}
	return members, nil
}

// appendIntMember appends "name":n, without a leading comma.
func appendIntMember(dst []byte, name string, n int64) []byte {
	dst = appendQuoted(dst, name)
	dst = append(dst, ':')
	return strconv.AppendInt(dst, n, 10)
}
