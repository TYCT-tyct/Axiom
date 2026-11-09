package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: the body is read token by token
	"errors"
	"fmt"

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
		return d.endUser(&d.r, true)
	case "safety_identifier":
		return d.endUser(&d.r, false)
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
		return d.endUser(&d.r, true)
	case "safety_identifier":
		return d.endUser(&d.r, false)
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
		return d.endUser(&d.r, true)
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
	case "safety_identifier":
		return d.endUser(&d.r, false)
	case "prompt_cache_key":
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
