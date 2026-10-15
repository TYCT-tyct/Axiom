// Package protocol reads and writes the three call formats: OpenAI Chat, OpenAI Responses and Anthropic Messages
// (README.md).
package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // jsonx has no token-by-token reader; jsontext's defaults are its strictness, and token checks jsonx.MaxDepth
	"errors"
	"fmt"
	"io"
	"strconv"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// Format is a call format.
type Format uint8

// The call formats.
const (
	Messages  Format = iota // Anthropic Messages
	Responses               // OpenAI Responses
	Chat                    // OpenAI Chat Completions
)

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
	Stream bool
	// Previous is Responses' previous_response_id. The stored history it names is input too.
	Previous string
	// MaxOutput is the output cap the request sets, 0 when it sets none and the model's default applies. Choices is
	// how many replies are generated, each under that cap: Chat's n, 1 otherwise.
	MaxOutput, Choices int64
	// TextBytes bounds the text input's tokens: the body's bytes less the image and document data that Images and
	// Documents count instead.
	TextBytes, Images, Documents int64
	// InputItems are the billing items the input may be billed as: input, and the cache writes the request marks or
	// its format makes by default.
	InputItems catalog.ItemSet
	// ServiceTiers are the service tiers the request may be served at.
	ServiceTiers catalog.ServiceTierSet
	// WebSearches and CodeExecutions are the most calls the request allows of each such tool it enables, 0 when it
	// sets no limit and the model's default applies.
	WebSearches, CodeExecutions int64
	// HidesReasoning is set when the request asks for reasoning to be left out of the stream, whose progress then
	// cannot be observed (docs/domains/billing.md「预留与结算」).
	HidesReasoning bool
	Needs          Capabilities
}

// ErrInvalid reports a body Inspect cannot read: not strict JSON within jsonx's policy, or a member it reads with the
// wrong type or value. The gateway answers it with 400.
var ErrInvalid = errors.New("protocol: invalid request")

var errTooDeep = fmt.Errorf("nesting deeper than %d levels", jsonx.MaxDepth)

// Inspect reads the request summary from body in one pass of the tokenizer, never decoding the body into a tree, so
// its allocations do not grow with the body (README.md「请求摘要」). Members it does not read are only checked to be
// JSON.
func Inspect(f Format, body []byte) (Request, error) {
	g := &grammars[f]
	s := scanner{dec: jsontext.NewDecoder(bytes.NewBuffer(body)), g: g}
	s.r = Request{Format: f, Choices: 1, TextBytes: int64(len(body)), InputItems: g.input}
	if err := s.top(); err != nil {
		return Request{}, fmt.Errorf("%w: %w", ErrInvalid, err)
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

// scanner reads one body. Members of an object may come in any order, so a part's meaning is gathered in p and
// settled when the object ends.
type scanner struct {
	dec         *jsontext.Decoder
	g           *grammar
	r           Request
	name        []byte // the member being read, unquoted; valid until the next read
	buf         []byte // scratch for unquoting
	p           part
	tiers, fast catalog.ServiceTierSet // service_tier, and Anthropic's speed, which overrides it
	callCap     int64                  // Responses' max_tool_calls
}

// part is the content part, tool or reasoning setting being read.
type part struct {
	kind        Capabilities
	payload     int64 // the raw bytes of the data the part carries
	binary      bool  // the payload is image or file data, not text
	max         int64 // a server tool's max_uses
	off, hidden bool  // reasoning turned off; reasoning left out of the stream
}

// fields maps an object's member names to their readers; other members are skipped.
type fields map[string]func(*scanner) error

func (s *scanner) top() error {
	if s.dec.PeekKind() != '{' {
		s.name = []byte("the body")
		return s.mismatch("an object")
	}
	if err := s.object(s.g.top); err != nil {
		return err
	}
	if _, err := s.dec.ReadToken(); !errors.Is(err, io.EOF) {
		return errors.Join(errors.New("data after the request"), err)
	}
	if s.r.Model == "" {
		return errors.New("model is missing")
	}
	return nil
}

// token reads one token, enforcing jsonx.MaxDepth: everything Inspect reads passes through it or is a scalar.
func (s *scanner) token() error {
	_, err := s.dec.ReadToken()
	if err == nil && s.dec.StackDepth() > jsonx.MaxDepth {
		err = errTooDeep
	}
	return err
}

// skip reads one value token by token, so its depth is checked too.
func (s *scanner) skip() error {
	depth := s.dec.StackDepth()
	for {
		if err := s.token(); err != nil || s.dec.StackDepth() == depth {
			return err
		}
	}
}

// present reports whether the next value is of kind, reading it when it is null, which stands for an absent member.
func (s *scanner) present(kind jsontext.Kind, want string) (bool, error) {
	if k := s.dec.PeekKind(); k == 'n' {
		return false, s.token()
	} else if k != kind {
		return false, s.mismatch(want)
	}
	return true, nil
}

// open reads the start of an object or array, reporting false for null.
func (s *scanner) open(kind jsontext.Kind, want string) (bool, error) {
	if ok, err := s.present(kind, want); !ok {
		return false, err
	}
	err := s.token()
	return err == nil, err
}

func (s *scanner) object(f fields) error {
	if ok, err := s.open('{', "an object"); !ok {
		return err
	}
	for s.dec.PeekKind() != '}' {
		name, err := s.dec.ReadValue()
		if err == nil {
			s.name, err = s.unquote(name)
		}
		if err != nil {
			return err
		}
		read := f[string(s.name)]
		if read == nil {
			read = (*scanner).skip
		}
		if err := read(s); err != nil {
			return err
		}
	}
	return s.token()
}

func (s *scanner) array(each func(*scanner) error) error {
	if ok, err := s.open('[', "an array"); !ok {
		return err
	}
	for s.dec.PeekKind() != ']' {
		if err := each(s); err != nil {
			return err
		}
	}
	return s.token()
}

// raw reads a string as written, quotes included, or nil for null. It is valid until the next read.
func (s *scanner) raw() (jsontext.Value, error) {
	if ok, err := s.present('"', "a string"); !ok {
		return nil, err
	}
	return s.dec.ReadValue()
}

// str reads a string, unquoted, or nil for null. It is valid until the next read.
func (s *scanner) str() ([]byte, error) {
	v, err := s.raw()
	if v == nil || err != nil {
		return nil, err
	}
	return s.unquote(v)
}

func (s *scanner) unquote(v jsontext.Value) (b []byte, err error) {
	if bytes.IndexByte(v, '\\') < 0 {
		return v[1 : len(v)-1], nil
	}
	s.buf, err = jsontext.AppendUnquote(s.buf[:0], v)
	return s.buf, err
}

// is reads a string and reports whether it is lit.
func (s *scanner) is(lit string) (bool, error) {
	v, err := s.str()
	return v != nil && string(v) == lit, err
}

// flag reads a boolean; null reads as 'n'.
func (s *scanner) flag() (jsontext.Kind, error) {
	if k := s.dec.PeekKind(); k == 't' || k == 'f' || k == 'n' {
		return k, s.token()
	}
	return 0, s.mismatch("a boolean")
}

// count reads a non-negative integer, 0 for null.
func (s *scanner) count() (int64, error) {
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
func (s *scanner) mismatch(want string) error {
	if s.dec.PeekKind() == 0 {
		return s.token()
	}
	return fmt.Errorf("%s is not %s", s.name, want)
}
