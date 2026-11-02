package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: the body is read token by token
	"errors"
	"fmt"
	"io"
	"strconv"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// The direct formats answer one JSON body: embeddings and rerank. Their request is read and rewritten member by member,
// without the grammar shared by the three chat formats, which describes content parts and tools that neither has
// (README.md「直接调用的格式」). Legacy completions generate text like Chat and are read by their own stream reader
// (completions.go), but their request has no message parts either, so they share this reader.

// inspectNew reads a request of a direct format, or of legacy completions.
func inspectNew(f Format, body []byte) (Request, error) {
	if err := jsonx.Validate(body); err != nil {
		return Request{}, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	d := directScanner{dec: jsontext.NewDecoder(bytes.NewBuffer(body))}
	d.r = Request{Format: f, Choices: 1, ChoiceCount: 1, InputItems: 1 << catalog.ItemInput, ServiceTiers: 1 << catalog.ServiceTierStandard}
	if err := d.top(); err != nil {
		return Request{}, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	if d.r.Model == "" {
		return Request{}, fmt.Errorf("%w: model is missing", ErrInvalid)
	}
	if err := d.validate(); err != nil {
		return Request{}, err
	}
	return d.r, nil
}

// EchoSize is the memory EchoOf allocates for r, which the caller charges to the node's budget first: a prompt's size
// is 8 bytes, and a body of a million empty prompts is 3 MB. 0 when r does not echo.
func EchoSize(r *Request) int64 {
	if !r.Echo {
		return 0
	}
	return 8 * (r.Choices / r.ChoiceCount)
}

// EchoOf reads the text bytes of each prompt of a legacy completion that echoes, in prompt order, for the relay to drop
// from each choice's metered text; nil when r does not echo. The prompt may come before echo, so Inspect cannot know to
// keep the sizes, and a request that echoes is scanned a second time. body must be the one Inspect read into r.
func EchoOf(body []byte, r *Request) *EchoPrompts {
	if !r.Echo {
		return nil
	}
	d := directScanner{dec: jsontext.NewDecoder(bytes.NewBuffer(body)), echo: make([]int64, 0, r.Choices/r.ChoiceCount)}
	d.r = Request{Format: r.Format, Choices: 1, ChoiceCount: 1}
	d.trackEcho = true
	if err := d.top(); err != nil {
		panic(fmt.Sprintf("protocol: EchoOf on a body Inspect did not accept: %v", err))
	}
	return &EchoPrompts{Bytes: d.echo, Choices: r.ChoiceCount}
}

// validate checks the members each direct format requires and fills the counts its request implies.
func (d *directScanner) validate() error {
	switch d.r.Format { //nolint:exhaustive // only the formats inspected here can reach the scanner
	case Embeddings:
		if !d.hasInput {
			return fmt.Errorf("%w: input is missing", ErrInvalid)
		}
	case Rerank:
		if !d.hasQuery {
			return fmt.Errorf("%w: query is missing", ErrInvalid)
		}
		if !d.hasDocs {
			return fmt.Errorf("%w: documents are missing", ErrInvalid)
		}
		// A rerank is one search: its usage reports search_units, which billing charges by the web_search item. The
		// reservation covers one; the request does not need the model's web_search ability, which is for tools.
		d.r.WebSearches = 1
		d.r.MaxInputBytes = d.queryBytes + d.maxDoc
	case Completions:
		if !d.hasPrompt {
			return fmt.Errorf("%w: prompt is missing", ErrInvalid)
		}
		choices := max(1, d.n)
		if d.prompts > 1 && choices > (1<<62)/d.prompts {
			return fmt.Errorf("%w: prompt and n ask for too many completions", ErrInvalid)
		}
		d.r.Choices = choices * max(1, d.prompts)
		d.r.ChoiceCount = choices
		// The suffix is inserted after the completion but is part of what the model reads: count it once for every
		// prompt, and keep the longest prompt's context check including it.
		d.r.TextBytes += d.prompts * d.suffixBytes
		d.r.RepeatedTextBytes = max(d.prompts-1, 0) * d.suffixBytes
		d.r.MaxInputBytes += d.suffixBytes
	}
	return nil
}

// directScanner reads the top-level members of a request of a direct format. The whole body was validated first, so
// skipping a member only consumes it: its depth and syntax are already known to be jsonx's.
type directScanner struct {
	reader
	r                                      Request
	prompts, n, suffixBytes                int64 // legacy completions: prompts, the n each returns, the inserted suffix's bytes
	trackEcho                              bool  // the echo pass (EchoOf): record each prompt's text bytes in echo
	echo                                   []int64
	queryBytes, maxDoc                     int64 // rerank: the query's bytes and the largest document's
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
		switch d.r.Format { //nolint:exhaustive // only the formats inspected here can reach the scanner
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

//nolint:cyclop // one member per key is the format's grammar; splitting it would hide the allowed set
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

//nolint:cyclop // one member per key is the format's grammar; splitting it would hide the allowed set
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
		v, err := d.str()
		if err == nil {
			d.queryBytes = int64(len(v))
			d.r.TextBytes += d.queryBytes
		}
		return err
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

//nolint:cyclop,funlen // one member per key is the format's grammar; splitting it would hide the allowed set
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
		kind := d.dec.PeekKind()
		n, err := d.count()
		if err == nil {
			// null is unset, as Prepare treats it: it takes the endpoint's cap. Only a number, including 0, is a cap
			// the request set.
			d.r.MaxOutput = max(d.r.MaxOutput, n)
			d.r.MaxOutputSet = kind != 'n'
		}
		return err
	case "n":
		n, err := d.count()
		d.n = n
		return err
	case "best_of":
		n, err := d.count()
		// OpenAI's best_of says "Results cannot be streamed". Every call here is a stream, so forwarding the member
		// would turn a legal request into one the upstream refuses.
		if err == nil && n > 1 {
			return fmt.Errorf("%w: best_of above 1 cannot be streamed, and completions are called as a stream; send 1 or omit it", ErrInvalid)
		}
		return err
	case "echo":
		k, err := d.flag()
		d.r.Echo = k == 't'
		return err
	case "suffix":
		v, err := d.str()
		if err == nil && v != nil {
			d.suffixBytes = int64(len(v))
		}
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

// input records one input's text bytes: the batch's total for the charge and the largest input for the context.
func (d *directScanner) input(n int64) {
	d.r.TextBytes += n
	if n > d.r.MaxInputBytes {
		d.r.MaxInputBytes = n
	}
}

// promptBytes records one prompt's text bytes for echo metering, in the order the prompts are read. A token prompt
// has no text here; four bytes per token is the same bound the input uses, so the echo estimate never exceeds what the
// input was charged for.
func (d *directScanner) promptBytes(n int64) {
	if d.trackEcho {
		d.echo = append(d.echo, n)
	}
}

// embeddingInput reads OpenAI's input: a string, an array of strings, a token array, an array of token arrays, or an
// array of objects whose content holds text and media parts (OpenAI《Create embeddings》, OpenRouter《Embeddings》,
// read 2026-10-30).
//
//nolint:exhaustive // the default rejects every other JSON kind with the client's error
func (d *directScanner) embeddingInput() error {
	switch d.dec.PeekKind() {
	case '"':
		v, err := d.str()
		if err == nil {
			d.input(int64(len(v)))
		}
		return err
	case '[':
		v, err := d.dec.ReadValue()
		if err != nil {
			return err
		}
		return d.embeddingArray(v)
	default:
		return fmt.Errorf("input is %s, want a string or an array", kindNameOf(d.dec.PeekKind()))
	}
}

// embeddingArray reads the several forms an embedding input array takes. Each string, token array or object is one
// input of its own: a batch is charged for all of them, while its context limit holds each one separately.
//
//nolint:cyclop,gocognit,exhaustive // the four input shapes are one dispatch; each branch is the shape's rule
func (d *directScanner) embeddingArray(v jsontext.Value) error {
	var first jsontext.Kind
	var tokens int64
	seen := false
	err := eachArray(v, func(kind jsontext.Kind, e jsontext.Value) error {
		if !seen {
			first, seen = kind, true
		}
		switch first {
		case '0':
			if kind != '0' {
				return fmt.Errorf("input mixes tokens with other values")
			}
			tokens++
		case '"':
			if kind != '"' {
				return fmt.Errorf("input mixes strings with other values")
			}
			d.input(unquotedLen(e))
		case '[':
			if kind != '[' {
				return fmt.Errorf("input mixes token arrays with other values")
			}
			n, err := tokenCount(e)
			if err != nil {
				return err
			}
			d.input(n * 4)
			d.r.TokenInputs += n
		case '{':
			if kind != '{' {
				return fmt.Errorf("input mixes objects with other values")
			}
			n, err := d.embeddingObject(e)
			if err != nil {
				return err
			}
			d.input(n)
		default:
			return fmt.Errorf("input has an element of the wrong type")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !seen {
		return fmt.Errorf("input is an empty array")
	}
	if first == '0' {
		d.input(tokens * 4)
		d.r.TokenInputs += tokens
	}
	return nil
}

// tokens counts a token array: its tokens are exact, so the reservation is the bytes a token takes at the estimate's
// most (billing.InputBytesPerToken is 4), which never undercounts them.
func tokenCount(v jsontext.Value) (int64, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	if _, err := dec.ReadToken(); err != nil {
		return 0, err
	}
	var n int64
	for dec.PeekKind() != ']' {
		if dec.PeekKind() != '0' {
			return 0, fmt.Errorf("input mixes tokens with other values")
		}
		if err := dec.SkipValue(); err != nil {
			return 0, err
		}
		n++
	}
	if _, err := dec.ReadToken(); err != nil {
		return 0, err
	}
	return n, nil
}

// embeddingObject reads one {content: [...]} input object and its parts, returning the text bytes it holds. Its
// content is read element by element: a batch of a million numbers must not become a million slice headers.
//
//nolint:cyclop,gocognit // one content part per type is the format's grammar
func (d *directScanner) embeddingObject(v jsontext.Value) (int64, error) {
	content, ok, err := member(v, "content")
	if err != nil {
		return 0, err
	}
	if !ok || content.Kind() != '[' {
		return 0, fmt.Errorf("input object has no content array")
	}
	var text int64
	err = eachArray(content, func(kind jsontext.Kind, part jsontext.Value) error {
		if kind != '{' {
			return fmt.Errorf("content part is not an object")
		}
		typ, _, err := member(part, "type")
		if err != nil {
			return err
		}
		switch string(typ) {
		case `"text"`:
			value, ok, err := member(part, "text")
			if err != nil {
				return err
			}
			if !ok || value.Kind() != '"' {
				return fmt.Errorf("text part has no text string")
			}
			text += unquotedLen(value)
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
			return fmt.Errorf("content part type %s is not supported", typ)
		}
		return nil
	})
	return text, err
}

// documents reads rerank's documents: strings, or objects with text and/or image. Each document is one input; the
// context limit holds the query and one document, not the whole batch.
//
//nolint:cyclop,gocognit,exhaustive // one document shape per branch is the format's grammar
func (d *directScanner) documents() error {
	v, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	if v.Kind() != '[' {
		return fmt.Errorf("documents is %s, want an array", kindName(v))
	}
	seen := false
	err = eachArray(v, func(kind jsontext.Kind, e jsontext.Value) error {
		seen = true
		switch kind {
		case '"':
			n := unquotedLen(e)
			d.r.TextBytes += n
			d.maxDoc = max(d.maxDoc, n)
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
			var n int64
			if hasText {
				if text.Kind() != '"' {
					return fmt.Errorf("a document's text is not a string")
				}
				n = unquotedLen(text)
				d.r.TextBytes += n
			}
			if hasImage {
				if image.Kind() != '"' {
					return fmt.Errorf("a document's image is not a string")
				}
				d.r.Images++
				d.r.Needs |= Images
			}
			d.maxDoc = max(d.maxDoc, n)
		default:
			return fmt.Errorf("documents has an element of the wrong type")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !seen {
		return fmt.Errorf("documents is empty")
	}
	return nil
}

// prompt reads legacy completions' prompt: a string, a token array, an array of strings, or an array of token arrays.
// A token array is one prompt, not one prompt per token; each prompt's length is kept for the context check.
//
//nolint:exhaustive // the default rejects every other JSON kind with the client's error
func (d *directScanner) prompt() error {
	switch d.dec.PeekKind() {
	case '"':
		v, err := d.str()
		if err == nil {
			d.input(int64(len(v)))
			d.promptBytes(int64(len(v)))
			d.prompts = 1
		}
		return err
	case '[':
		v, err := d.dec.ReadValue()
		if err != nil {
			return err
		}
		return d.promptArray(v)
	default:
		return fmt.Errorf("prompt is %s, want a string or an array", kindNameOf(d.dec.PeekKind()))
	}
}

// promptArray distinguishes a token prompt, which is one prompt, from a list of prompts, each of which generates.
//
//nolint:cyclop,gocognit,exhaustive // token prompt, strings and token arrays are one dispatch
func (d *directScanner) promptArray(v jsontext.Value) error {
	var first jsontext.Kind
	var tokens int64
	seen := false
	err := eachArray(v, func(kind jsontext.Kind, e jsontext.Value) error {
		if !seen {
			first, seen = kind, true
		}
		switch first {
		case '0':
			if kind != '0' {
				return fmt.Errorf("prompt mixes tokens with other values")
			}
			tokens++
		case '"':
			if kind != '"' {
				return fmt.Errorf("prompt mixes strings with other values")
			}
			d.input(unquotedLen(e))
			d.promptBytes(unquotedLen(e))
			d.prompts++
		case '[':
			if kind != '[' {
				return fmt.Errorf("prompt mixes token arrays with other values")
			}
			n, err := tokenCount(e)
			if err != nil {
				return err
			}
			d.input(n * 4)
			d.r.TokenInputs += n
			d.promptBytes(n * 4)
			d.prompts++
		default:
			return fmt.Errorf("prompt has an element of the wrong type")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !seen {
		return fmt.Errorf("prompt is an empty array")
	}
	if first == '0' {
		d.input(tokens * 4)
		d.r.TokenInputs += tokens
		d.promptBytes(tokens * 4)
		d.prompts = 1
	}
	return nil
}

// eachArray reads an array's elements in order, without keeping them: a large input must not allocate a slice header
// per element.
func eachArray(v jsontext.Value, read func(jsontext.Kind, jsontext.Value) error) error {
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	for dec.PeekKind() != ']' {
		kind := dec.PeekKind()
		if kind == '0' {
			// A token array can hold a million numbers; skipping each one allocates nothing. A consumer that needs a
			// value (a string, an object or a nested token array) gets it.
			if err := read(kind, nil); err != nil {
				return err
			}
			if err := dec.SkipValue(); err != nil {
				return err
			}
			continue
		}
		e, err := dec.ReadValue()
		if err != nil {
			return err
		}
		if err := read(kind, e); err != nil {
			return err
		}
	}
	_, err := dec.ReadToken()
	return err
}

// member returns an object's named value, or false when it has none. The value aliases v: a caller uses it before
// the body it came from is released.
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
			return value, true, nil
		}
	}
	return nil, false, nil
}

func kindName(v jsontext.Value) string { return kindNameOf(v.Kind()) }

//nolint:exhaustive // every unlisted kind means "not a value"
func kindNameOf(k jsontext.Kind) string {
	switch k {
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
//
//nolint:exhaustive // every unlisted format falls back to "the request"
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
			"safety_identifier": (*rewriter).identity, "max_tokens": (*rewriter).completionsCap,
			"stream": (*rewriter).stream, "stream_options": (*rewriter).streamOptions,
		}),
		add: (*rewriter).addCompletions, identities: 3,
		room: len(addCap) + len(maxInt64) + len(addStreamOptions) + len(addStream),
	},
}

func rewriteDirectWithin(r *Request, body []byte, t Target, w Rewrites, b *budget) ([]byte, error) {
	rules, ok := directRewriteRules[r.Format]
	if !ok {
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d is not rewritten yet", r.Format)}
	}
	w = w.withDefaults()
	size := rewriteSize(rules, len(body), t, w)
	if err := b.draw(rounded(int64(size))); err != nil {
		return nil, err
	}
	p := &rewriter{dec: newDecoder(body), body: body, t: t, w: w}
	p.b = b
	p.out = make([]byte, 0, size)
	if err := p.top(rules); err != nil {
		if errors.Is(err, ErrMemory) {
			return nil, err
		}
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
	//nolint:modernize // the embedded reader's fields are clearer at this boundary
	d := directScanner{
		reader: reader{dec: jsontext.NewDecoder(bytes.NewReader(v)), name: []byte("usage")},
		r:      Request{Format: f},
	}
	u, err := d.usageObject()
	if err != nil {
		return Usage{Invalid: err}, nil //nolint:nilerr // an invalid usage is reported in Usage.Invalid, not as a failed reply
	}
	return u, nil
}

// usageValue returns the reply's usage member, or nil when it has none.
func usageValue(body []byte) (jsontext.Value, error) {
	members, err := topReply(body, "usage")
	if err != nil {
		return nil, err
	}
	m, ok := members["usage"]
	if !ok {
		return nil, nil
	}
	return body[m.off : m.off+m.n], nil
}

//nolint:cyclop,gocognit // each usage member is one counter; the switch is the format's whole grammar
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
		if fin != nil { // the account's key is worth reporting even before the settlement is written
			costString = cost(fin, f)
		}
	} else {
		u, costString = reply(fin.Usage), cost(fin, f)
	}
	return rewriteReply(f, body, id, model, u, costString, top)
}

// rewriteReply writes the reply a direct call's client gets: the upstream's, with its identity and cost replaced by
// Axiom's, in the shape OpenRouter documents for the format (OpenRouter《Embeddings》 and 《Rerank》, read 2026-10-30).
//
//nolint:exhaustive // only the two direct formats reach the encoder
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
		out = append(out, memberBytes(body, members["object"])...)
		out = append(out, `,"data":`...)
		out = append(out, memberBytes(body, members["data"])...)
		out = append(out, `,"model":`...)
		out = appendQuoted(out, model)
		out = append(out, `,"usage":`...)
		out = appendEmbeddingUsage(out, u, cost)
	case Rerank:
		out = append(out, `{"id":`...)
		out = appendQuoted(out, id)
		out = append(out, `,"model":`...)
		out = appendQuoted(out, model)
		out = append(out, `,"results":`...)
		out = append(out, memberBytes(body, members["results"])...)
		out = append(out, `,"usage":`...)
		out = appendRerankUsage(out, u, cost)
	default:
		return nil, &NotSupported{Reason: fmt.Sprintf("format %d is not a direct reply", f)}
	}
	if top != "" {
		out = append(out, top...)
	}
	return append(out, '}'), nil
}

// topReply reads the named members of a reply object, accepting leading whitespace and refusing anything after the
// object: the usage a call is charged by and the data a client reads must come from one complete JSON value.
//
//nolint:gocognit // the boundaries and offsets are the point; splitting hides their one order
func topReply(body []byte, want ...string) (map[string]replyRef, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	k, err := dec.ReadToken()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if k.Kind() != '{' {
		return nil, fmt.Errorf("%w: a direct reply is not a JSON object", ErrUpstream)
	}
	members := make(map[string]replyRef, len(want))
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
		end := int(dec.InputOffset())
		start := end - len(v)
		for _, name := range want {
			if string(key) == name {
				members[name] = replyRef{off: start, n: len(v), kind: byte(v.Kind())}
			}
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: data after a direct reply", ErrUpstream)
	}
	return members, nil
}

// replyMembers reads the members a direct reply keeps: its own object and data (embeddings) or results (rerank). A
// reply without them is the upstream breaking its format.
//
//nolint:exhaustive // only the two direct formats reach the reply reader
func replyMembers(f Format, body []byte) (map[string]replyRef, error) {
	members, err := topReply(body, "object", "data", "results")
	if err != nil {
		return nil, err
	}
	switch f {
	case Embeddings:
		if m, ok := members["data"]; !ok || m.kind != '[' {
			return nil, fmt.Errorf("%w: an embeddings reply has no data array", ErrUpstream)
		}
		if m, ok := members["object"]; !ok || m.kind != '"' {
			members["object"] = replyRef{literal: `"list"`}
		}
	case Rerank:
		if m, ok := members["results"]; !ok || m.kind != '[' {
			return nil, fmt.Errorf("%w: a rerank reply has no results array", ErrUpstream)
		}
	}
	return members, nil
}

// replyRef points at a member's value in the reply body; literal stands for one Axiom supplies when the upstream did
// not. No value is copied out of the body: a 16 MiB data or results array must not become a second 16 MiB slice.
type replyRef struct {
	off, n  int
	kind    byte
	literal string
}

func memberBytes(body []byte, m replyRef) []byte {
	if m.off == 0 && m.n == 0 {
		return []byte(m.literal)
	}
	return body[m.off : m.off+m.n]
}

// appendIntMember appends "name":n, without a leading comma.
func appendIntMember(dst []byte, name string, n int64) []byte {
	dst = appendQuoted(dst, name)
	dst = append(dst, ':')
	return strconv.AppendInt(dst, n, 10)
}
