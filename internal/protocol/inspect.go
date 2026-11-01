// Package protocol reads and writes the three call formats: OpenAI Chat, OpenAI Responses and Anthropic Messages
// (README.md).
package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // jsonx has no token-by-token reader; jsontext's defaults are its strictness, and token checks jsonx.MaxDepth
	"encoding/json/v2"       //nolint:depguard // only the type of the errors jsonx returns, to tell the client what they are about
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// Format is a call format.
type Format uint8

// The call formats. A format's number is stored in suppliers' state and in a routing bit set, so one is appended, never
// inserted (docs/domains/gateway.md「决策与理由」).
const (
	Messages    Format = iota // Anthropic Messages
	Responses                 // OpenAI Responses
	Chat                      // OpenAI Chat Completions
	Embeddings                // OpenAI's embeddings
	Rerank                    // OpenRouter's rerank
	Completions               // OpenAI's legacy text completions
)

// Direct reports whether a format answers one whole JSON body that is not a generation, with no stream to relay and
// no content to meter: embeddings and rerank.
func (f Format) Direct() bool { return f == Embeddings || f == Rerank }

// Convertible reports whether the format converts to or from another: the three chat formats. A direct call and a
// legacy completion are native only.
func (f Format) Convertible() bool { return f <= Chat }

// Capabilities is a set of features a request uses that an endpoint may lack. Routing keeps only the endpoints that
// have all of them (docs/domains/routing.md「一个请求的路由步骤」).
type Capabilities uint16

// The capabilities.
const (
	Tools            Capabilities = 1 << iota // tools the client runs: functions, custom and vendor-defined client tools
	Images                                    // image input
	Documents                                 // PDF and other file input
	Reasoning                                 // reasoning the request asks for
	PromptCache                               // explicit cache markers
	StructuredOutput                          // a JSON schema or JSON mode for the reply
	WebSearch                                 // the vendor's web search, billed per call
	CodeExecution                             // the vendor's code execution, billed per call
)

// Has reports whether s holds every capability in c.
func (s Capabilities) Has(c Capabilities) bool { return s&c == c }

// Request is what billing's admission and routing need from a request body (README.md「请求摘要」).
type Request struct {
	Format Format
	Model  string
	// Models are OpenRouter's alternates, tried after Model when it cannot serve the request.
	Models    []string
	Preset    string // OpenRouter reference, resolved by gateway before routing
	SessionID string // explicit body session, ahead of the header and legacy identities
	Stream    bool
	// Local OpenRouter plugins are validated before their control fields are removed.
	Compression     Compression
	ResponseHealing bool
	// Web is the web plugin the request enabled, nil when it asked for no external search. The gateway runs it and
	// injects the results; Online marks the :online variant, whose refusal differs while no backend is configured.
	Web *WebPlugin
	// SearchBytes is what the injected search results may add to TextBytes: the admission and the route reserve input
	// for it before the search runs, because the results are not in the body they read.
	SearchBytes int64
	// File is the file-parser plugin the request enabled, nil when it asked for none. The gateway replaces the files
	// with parsed text before admission when the engine is not native, and leaves them to an endpoint that accepts
	// documents otherwise.
	File *FileParser
	// FileParses is how many files the gateway parsed for this request before admission: each is a per-use charge,
	// which the admission reserves.
	FileParses int64
	// MessageCount is Chat conversation length for the vendor message cap.
	MessageCount int64
	// Previous is Responses' previous_response_id. The stored history it names is input too.
	Previous string
	// Provider is OpenRouter's provider object as written, for the gateway to read: how the request narrows its route.
	// Nil when the request has none; at most maxProvider bytes.
	Provider []byte
	// MaxOutput is the output cap the request sets, 0 when it sets none and the model's default applies. Choices is
	// how many replies are generated, each under that cap: Chat's n, 1 otherwise.
	MaxOutput, Choices int64
	// MaxOutputSet is true when the request named its output cap, so an explicit 0 means no output rather than the
	// endpoint's default. Legacy completions sets it; a format that leaves it false keeps the default rule.
	MaxOutputSet bool
	// Echo is legacy completions' echo: the prompt comes back as the first text of every choice (EchoOf reads each
	// prompt's size). ChoiceCount is the choices each prompt generates (n), which maps a choice's index to its prompt;
	// Choices / ChoiceCount is the number of prompts.
	Echo        bool
	ChoiceCount int64
	// TextBytes bounds the text input's tokens: the body's bytes less the image and document data that Images and
	// Documents count instead.
	TextBytes, Images, Documents int64
	// TokenInputs and RepeatedTextBytes are the input the body's strings do not show, which a count of those strings
	// (Text) must add: the token IDs of a token-array input, exact, and the text the body holds once but the model reads
	// again, a legacy completion's suffix for every prompt after the first. Both are already in TextBytes.
	TokenInputs, RepeatedTextBytes int64
	// MaxInputBytes is the largest single input of a batch, in bytes; 0 when the request has one input. A batch's
	// context limit applies to each input, while TextBytes is what all of them are charged for.
	MaxInputBytes int64
	// FileRefs is how many files the request refers to by an id an upstream issued instead of carrying their data. A
	// reference is the supplier account's resource: routing keeps such a request to the one provider the client names,
	// so it can only reach the account that can hold the file (README.md「跨格式转换」).
	FileRefs int64
	// InputItems are the billing items the input may be billed as: input, and the cache writes the request marks or
	// its format makes by default.
	InputItems catalog.ItemSet
	// ServiceTiers are the service tiers the request may be served at.
	ServiceTiers catalog.ServiceTierSet
	// WebSearches and CodeExecutions are the most calls the request allows of each such tool it enables, 0 when it
	// sets no limit and the model's default applies. A rerank request sets WebSearches to one: its search unit is
	// priced as a web search, though the request does not ask for the model's web_search ability.
	WebSearches, CodeExecutions int64
	// HidesReasoning is set when the request asks for reasoning to be left out of the stream, whose progress then
	// cannot be observed (docs/domains/billing.md「预留与结算」).
	HidesReasoning bool
	// Compacts is set when the request turns on server-side compaction, which samples the model a second time
	// (docs/domains/billing.md「预留与结算」).
	Compacts            bool
	Needs               Capabilities
	positions           requestPositions
	legacy, modernTools bool
}

// ZeroOutput is whether the request capped its output at 0, as legacy completions' max_tokens 0 does: it asks for no
// generated text, so an answer without any is not an empty response.
func (r *Request) ZeroOutput() bool { return r.MaxOutputSet && r.MaxOutput == 0 }

// ErrInvalid reports a body Inspect cannot read: not strict JSON within jsonx's policy, or a member it reads with the
// wrong type or value. The gateway answers it with 400.
var ErrInvalid = errors.New("protocol: invalid request")

var errTooDeep = fmt.Errorf("nesting deeper than %d levels", jsonx.MaxDepth)

// badJSON is what a client is told of a body that is not JSON. It is new-api's sentence, which the differential test
// (docs/domains/gateway.md「与 new-api 的有意差异」) holds ours to.
const badJSON = "Invalid request: invalid JSON request body"

// bodyFault is what the client reads of an error met while reading its body. The decoders' own text is not for the
// client: it names offsets and Go types (`jsontext: unexpected EOF within "/model" after offset 9`, `cannot unmarshal
// JSON string into Go struct field canon.model`), which no client can act on and which tell what parses the body.
// A syntax error is badJSON, a value of the wrong type is named by its JSON pointer, which is the client's own path,
// and the errors of ours (a missing model, a member of the wrong type) already say what to change.
func bodyFault(err error) string {
	var syntax *jsontext.SyntacticError
	var semantic *json.SemanticError
	switch {
	case errors.As(err, &syntax):
		return badJSON
	case errors.As(err, &semantic):
		return "the value at " + string(semantic.JSONPointer) + " is not of a type this member takes"
	}
	return err.Error()
}

// Inspect reads the request summary from body in one pass of the tokenizer, never decoding the body into a tree, so
// its own allocations do not grow with the body; the tokenizer's grow only with the names of objects wider than 64
// names, as in jsonx.Validate (README.md「请求摘要」). Members it does not read are only checked to be JSON.
//
//nolint:cyclop,gocognit // one dispatch over every format, each branch one reader
func Inspect(f Format, body []byte) (Request, error) {
	if f == Completions || f.Direct() {
		return inspectNew(f, body)
	}
	g := &grammars[f]
	s := scanner{dec: jsontext.NewDecoder(bytes.NewBuffer(body)), g: g}
	s.r = Request{Format: f, Choices: 1, TextBytes: int64(len(body)), InputItems: g.input}
	if err := s.top(body); err != nil {
		if e, ok := errors.AsType[*ExtensionError](err); ok {
			return Request{}, e
		}
		return Request{}, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	if !s.compressionSet && s.transformsSet {
		s.r.Compression = CompressionDisabled
		if s.middleOut {
			s.r.Compression = CompressionEnabled
		}
	}
	if s.reasoningOff {
		s.r.Needs &^= Reasoning
	}
	s.r.ServiceTiers = s.fast
	if s.r.ServiceTiers == 0 {
		s.r.ServiceTiers = s.tiers
	}
	if s.r.ServiceTiers == 0 {
		s.r.ServiceTiers = anyTier
	}
	if s.callCap > 0 { // only Responses has it, and its tools have no limits of their own
		if s.r.Needs.Has(WebSearch) {
			s.r.WebSearches = s.callCap
		}
		if s.r.Needs.Has(CodeExecution) {
			s.r.CodeExecutions = s.callCap
		}
	}
	return s.r, nil
}

// anyTier is what a request that names no tier may get: OpenAI's auto takes the project's tier and Anthropic's auto
// takes Priority Tier capacity when the account has it (OpenAI《Create chat completion》, Anthropic《Service tiers》,
// read 2026-10-14).
const anyTier = catalog.ServiceTierSet(1<<catalog.ServiceTierStandard | 1<<catalog.ServiceTierPriority)

// reader reads a body token by token, holding every token to jsonx.MaxDepth. Inspect, Prepare and the relay share it.
type reader struct {
	dec  *jsontext.Decoder
	name []byte // the member being read, unquoted; valid until the next read
	buf  []byte // scratch for unquoting
	// charge, unless nil, is asked first for what buf grows by: the relay's readers keep buf from event to event.
	charge func(n int) error
}

// scanner reads one body. Members of an object may come in any order, so a part's meaning is gathered in p and
// settled when the object ends.
type scanner struct {
	reader
	g                                        *grammar
	r                                        Request
	p                                        part
	tiers, fast                              catalog.ServiceTierSet // service_tier, and Anthropic's speed, which overrides it
	reasoningOff                             bool
	compressionSet, transformsSet, middleOut bool
	modelsSet, fallbacksSet                  bool
	fallbackModels                           []string
	callCap                                  int64 // Responses' max_tool_calls
}

// part is the content part, tool or reasoning setting being read.
type part struct {
	toolKind                 string // dual-mode Responses tools need an explicit client execution claim
	localShell, clientSearch bool
	kind                     Capabilities
	payload                  int64 // the raw bytes of the data the part carries
	binary                   bool  // the payload is image or file data, not text
	refs                     int64 // files the part refers to by an id
	max                      int64 // a server tool's max_uses
	off, hidden, on          bool  // reasoning turned off; reasoning left out of the stream
}

// fields maps an object's member names to their readers; other members are skipped.
type fields map[string]func(*scanner) error

func (s *scanner) top(body []byte) error {
	if s.dec.PeekKind() != '{' {
		s.name = []byte("the body")
		return s.mismatch("an object")
	}
	if err := s.topMembers(body); err != nil {
		return err
	}
	if _, err := s.dec.ReadToken(); !errors.Is(err, io.EOF) {
		return errors.Join(errors.New("data after the request"), err)
	}
	if s.modelsSet && s.fallbacksSet {
		return errors.New("fallbacks cannot be combined with models")
	}
	if s.fallbacksSet {
		s.r.Models = s.fallbackModels
	}
	if s.r.Model == "" && !strings.HasPrefix(s.r.Preset, "@preset/") {
		return errors.New("model is missing")
	}
	return nil
}

// token reads one token, enforcing jsonx.MaxDepth: everything Inspect reads passes through it or is a scalar.
func (s *reader) token() error {
	_, err := s.dec.ReadToken()
	if err == nil && s.dec.StackDepth() > jsonx.MaxDepth {
		err = errTooDeep
	}
	return err
}

// skip reads one value token by token, so its depth is checked too.
func (s *reader) skip() error {
	depth := s.dec.StackDepth()
	for {
		if err := s.token(); err != nil || s.dec.StackDepth() == depth {
			return err
		}
	}
}

// present reports whether the next value is of kind, reading it when it is null, which stands for an absent member.
func (s *reader) present(kind jsontext.Kind, want string) (bool, error) {
	if k := s.dec.PeekKind(); k == 'n' {
		return false, s.token()
	} else if k != kind {
		return false, s.mismatch(want)
	}
	return true, nil
}

// open reads the start of an object or array, reporting false for null.
func (s *reader) open(kind jsontext.Kind, want string) (bool, error) {
	if ok, err := s.present(kind, want); !ok {
		return false, err
	}
	err := s.token()
	return err == nil, err
}

func (s *scanner) object(f fields) error { return walk(&s.reader, s, f) }

// eachMember reads an object, or null, calling read after reading each member's name into name; read reads the
// value.
func (s *reader) eachMember(read func() error) error {
	if ok, err := s.open('{', "an object"); !ok {
		return err
	}
	for s.dec.PeekKind() != '}' {
		if err := s.member(); err != nil {
			return err
		}
		if err := read(); err != nil {
			return err
		}
	}
	return s.token()
}

func (s *scanner) array(each func(*scanner) error) error {
	return s.eachElement(func() error { return each(s) })
}

// eachElement reads an array, or null, calling read for each element; read reads it.
func (s *reader) eachElement(read func() error) error {
	if ok, err := s.open('[', "an array"); !ok {
		return err
	}
	for s.dec.PeekKind() != ']' {
		if err := read(); err != nil {
			return err
		}
	}
	return s.token()
}

// member reads a member's name into name.
func (s *reader) member() error {
	v, err := s.dec.ReadValue()
	if err == nil {
		s.name, err = s.unquote(v)
	}
	return err
}

// raw reads a string as written, quotes included, or nil for null. It is valid until the next read.
func (s *reader) raw() (jsontext.Value, error) {
	if ok, err := s.present('"', "a string"); !ok {
		return nil, err
	}
	return s.dec.ReadValue()
}

// str reads a string, unquoted, or nil for null. It is valid until the next read.
func (s *reader) str() ([]byte, error) {
	v, err := s.raw()
	if v == nil || err != nil {
		return nil, err
	}
	return s.unquote(v)
}

func (s *reader) unquote(v jsontext.Value) (b []byte, err error) {
	if bytes.IndexByte(v, '\\') < 0 {
		return v[1 : len(v)-1], nil
	}
	if s.buf, err = sse.Grow(s.buf[:0], len(v), s.charge); err != nil { // unquoting never lengthens a string
		return nil, err
	}
	s.buf, err = jsontext.AppendUnquote(s.buf, v)
	return s.buf, err
}

// is reads a string and reports whether it is lit.
func (s *reader) is(lit string) (bool, error) {
	v, err := s.str()
	return v != nil && string(v) == lit, err
}

// flag reads a boolean; null reads as 'n'.
func (s *reader) flag() (jsontext.Kind, error) {
	if k := s.dec.PeekKind(); k == 't' || k == 'f' || k == 'n' {
		return k, s.token()
	}
	return 0, s.mismatch("a boolean")
}

// count reads a non-negative integer, 0 for null.
func (s *reader) count() (int64, error) {
	if ok, err := s.present('0', "an integer"); !ok {
		return 0, err
	}
	v, err := s.dec.ReadValue()
	if err != nil {
		return 0, err
	}
	if n, err := strconv.ParseInt(string(v), 10, 64); err == nil && n >= 0 {
		return n, nil
	}
	return 0, fmt.Errorf("%s is %s, want a non-negative integer", s.name, v)
}

// mismatch reports a value of the wrong type, or the syntax error that hid its type.
func (s *reader) mismatch(want string) error {
	if s.dec.PeekKind() == 0 {
		return s.token()
	}
	return fmt.Errorf("%s is not %s", s.name, want)
}
