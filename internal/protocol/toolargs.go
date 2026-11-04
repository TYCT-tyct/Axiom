package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // the arguments are read in place, as the events that carry them
	"unicode/utf8"

	"axiom/internal/platform/jsonx"
)

// argScan checks, piece by piece as a stream delivers them, that a client tool call's arguments are one JSON text
// (RFC 8259): what a client's JSON.parse accepts, a repeated member name included. It holds a fixed state, which
// containers are open and where in a token it is, never the arguments themselves, so a call whose arguments run to
// megabytes costs the stream nothing more (docs/domains/quality.md「线上观测」). It nests at most jsonx.MaxDepth
// levels, as everything else the gateway reads.
type argScan struct {
	objects [jsonx.MaxDepth / 64]uint64 // bit d is set when the container open at depth d is an object
	depth   int
	lit     string // what is left of the literal being read
	hex     int    // the hex digits left of a \u escape
	state   scanState
	name    bool // the string being read is a member name
}

type scanState uint8

const (
	scanValue       scanState = iota // a value is due: at the start, after a colon, after a comma in an array
	scanArrayFirst                   // after '[': a value or ']'
	scanObjectFirst                  // after '{': a name or '}'
	scanName                         // after a comma in an object
	scanColon
	scanAfter // after a value in a container: a comma or its closer; step takes the states up to here as between tokens
	scanString
	scanEscape
	scanHex
	scanLiteral
	scanMinus
	scanZero
	scanInt
	scanDot
	scanFrac
	scanE
	scanESign
	scanExp
	scanDone // the text is complete; only whitespace may follow
	scanBad
)

// ok reports whether what was fed is one complete JSON text, or nothing but whitespace: a call without arguments,
// which Messages streams as no delta at all and a tool without parameters may send as "". Taking it for an error would
// count a correct call against the endpoint; the measure errs on the side of not blaming one.
func (s *argScan) ok() bool {
	if s.state == scanValue && s.depth == 0 {
		return true
	}
	s.step(' ') // a number at the top level ends only where something follows it
	return s.state == scanDone
}

// step reads one byte. The states between tokens and those within one have a transition table each, a method per
// state's rule; a string's content, most of what arguments hold, is read first.
func (s *argScan) step(c byte) {
	switch {
	case s.state == scanString:
		s.inString(c)
	case s.state <= scanAfter:
		s.between(c)
	default:
		s.within(c)
	}
}

// between reads a byte between tokens.
func (s *argScan) between(c byte) {
	switch s.state { //nolint:exhaustive // step gives it only the states up to scanAfter
	case scanValue, scanArrayFirst:
		s.valueDue(c)
	case scanObjectFirst, scanName:
		s.nameDue(c)
	case scanColon:
		s.colon(c)
	case scanAfter:
		s.after(c)
	}
}

// within reads a byte within a token other than a string's content, or after the text.
func (s *argScan) within(c byte) {
	switch s.state { //nolint:exhaustive // step gives it only the states after scanString
	case scanEscape:
		s.escape(c)
	case scanHex:
		s.hexDigit(c)
	case scanLiteral:
		s.literal(c)
	case scanMinus:
		s.digitOr(c, c == '0', scanZero, scanInt)
	case scanDot:
		s.digitOr(c, false, scanFrac, scanFrac)
	case scanE, scanESign:
		s.exponent(c)
	case scanZero, scanInt, scanFrac, scanExp:
		s.number(c)
	case scanDone:
		if !space(c) {
			s.state = scanBad
		}
	case scanBad:
	}
}

// valueDue reads where a value is due, or, right after '[', the array's end.
func (s *argScan) valueDue(c byte) {
	switch {
	case space(c):
	case c == ']' && s.state == scanArrayFirst:
		s.close()
	default:
		s.value(c)
	}
}

// nameDue reads where a member name is due, or, right after '{', the object's end.
func (s *argScan) nameDue(c byte) {
	switch {
	case space(c):
	case c == '}' && s.state == scanObjectFirst:
		s.close()
	case c == '"':
		s.state, s.name = scanString, true
	default:
		s.state = scanBad
	}
}

func (s *argScan) colon(c byte) {
	switch {
	case space(c):
	case c == ':':
		s.state = scanValue
	default:
		s.state = scanBad
	}
}

// after reads what follows a value in a container: a comma or the container's closer.
func (s *argScan) after(c byte) {
	object := s.objects[(s.depth-1)/64]&(1<<((s.depth-1)%64)) != 0
	switch {
	case space(c):
	case c == ',' && object:
		s.state = scanName
	case c == ',':
		s.state = scanValue
	case c == '}' && object, c == ']' && !object:
		s.close()
	default:
		s.state = scanBad
	}
}

func (s *argScan) inString(c byte) {
	switch {
	case c == '"' && s.name:
		s.state, s.name = scanColon, false
	case c == '"':
		s.end()
	case c == '\\':
		s.state = scanEscape
	case c < 0x20:
		s.state = scanBad
	}
}

func (s *argScan) escape(c byte) {
	switch c {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		s.state = scanString
	case 'u':
		s.state, s.hex = scanHex, 4
	default:
		s.state = scanBad
	}
}

// hexDigit reads one of a \u escape's four hex digits.
func (s *argScan) hexDigit(c byte) {
	switch {
	case !hexDigit(c):
		s.state = scanBad
	case s.hex == 1:
		s.state = scanString
	default:
		s.hex--
	}
}

func (s *argScan) literal(c byte) {
	switch {
	case c != s.lit[0]:
		s.state = scanBad
	case len(s.lit) == 1:
		s.end()
	default:
		s.lit = s.lit[1:]
	}
}

// exponent reads what follows an exponent's e: a sign, once, or its first digit.
func (s *argScan) exponent(c byte) {
	if (c == '+' || c == '-') && s.state == scanE {
		s.state = scanESign
		return
	}
	s.digitOr(c, false, scanExp, scanExp)
}

// value starts the value c begins.
func (s *argScan) value(c byte) {
	switch c {
	case '{', '[':
		s.open(c == '{')
	case '"':
		s.state = scanString
	case '-':
		s.state = scanMinus
	case '0':
		s.state = scanZero
	case 't', 'f', 'n':
		s.state, s.lit = scanLiteral, literalRests[c]
	default:
		s.digitOr(c, false, scanInt, scanInt)
	}
}

// literalRests are what follows the first letter of true, false and null.
var literalRests = [...]string{'f': "alse", 'n': "ull", 't': "rue"}

// open starts an object or an array, one level deeper.
func (s *argScan) open(object bool) {
	if s.depth == jsonx.MaxDepth {
		s.state = scanBad
		return
	}
	word, bit := s.depth/64, uint64(1)<<(s.depth%64)
	s.objects[word] &^= bit
	s.state = scanArrayFirst
	if object {
		s.objects[word] |= bit
		s.state = scanObjectFirst
	}
	s.depth++
}

// digitOr moves to zero on a 0 when zero is set, to digits on any digit otherwise, and fails on anything else.
func (s *argScan) digitOr(c byte, zero bool, zeroState, digits scanState) {
	switch {
	case zero:
		s.state = zeroState
	case '0' <= c && c <= '9':
		s.state = digits
	default:
		s.state = scanBad
	}
}

// number reads on in a number that is complete so far: a byte that cannot continue it ends it and is read again.
func (s *argScan) number(c byte) {
	switch {
	case '0' <= c && c <= '9' && s.state != scanZero:
	case c == '.' && (s.state == scanZero || s.state == scanInt):
		s.state = scanDot
	case (c == 'e' || c == 'E') && s.state != scanExp:
		s.state = scanE
	default:
		s.end()
		s.step(c)
	}
}

func (s *argScan) close() {
	s.depth--
	s.end()
}

// end follows a complete value.
func (s *argScan) end() {
	s.state = scanAfter
	if s.depth == 0 {
		s.state = scanDone
	}
}

func space(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func hexDigit(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// feedArgs feeds the arguments v, a JSON string as written in the event or nil, to scan. It reads the escapes in place
// rather than unquote v into a buffer, which would cost the stream an allocation and a copy of the arguments, as
// addShape does: the event's decoder has already checked v is a valid string, and the scan only tells characters apart
// below 0x80, so an escape stands for the first byte of its character's UTF-8: itself below 0x80, a byte above
// otherwise.
func feedArgs(scan *argScan, v jsontext.Value) {
	if len(v) < 2 {
		return
	}
	v = v[1 : len(v)-1]
	var enc [utf8.UTFMax]byte
	for i := 0; i < len(v) && scan.state != scanBad; {
		c, n := v[i], 1
		if c == '\\' {
			var r rune
			r, n = unescape(v[i:])
			c = utf8.AppendRune(enc[:0], r)[0]
		}
		scan.step(c)
		i += n
	}
}
