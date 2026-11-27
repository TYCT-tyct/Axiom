package google

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"

	"axiom/internal/platform/jsonx"
)

// withTier is a Chat answer of the compatible layer, streamed or whole, with the tier, in Chat's name, as its
// service_tier where it names none: Google says a downgrade in the x-gemini-service-tier header and need not say it
// in the body, and the relay bills a reply that names no tier at the tier asked. A stream's chunk names it in each
// chunk that lacks one, since the relay takes a tier with the usage of the same chunk or one before it.
func withTier(body io.ReadCloser, tier string, stream bool) io.ReadCloser {
	member := []byte(`"service_tier":` + strconv.Quote(tier) + ",")
	if stream {
		return &tierLines{body: body, src: bufio.NewReaderSize(body, maxLine), member: member}
	}
	return &tierWhole{body: body, member: member}
}

// maxLine is the longest chunk a tier is put into: a usage chunk is far shorter, and a longer line goes on unchanged
// rather than be held whole.
const maxLine = 64 << 10

// tierLines puts the tier into each data line of a stream that is an object and names none.
type tierLines struct {
	body   io.ReadCloser
	src    *bufio.Reader
	member []byte
	out    []byte
	long   bool // the line being read is past maxLine
	err    error
}

func (t *tierLines) Read(p []byte) (int, error) {
	for len(t.out) == 0 && t.err == nil {
		line, err := t.src.ReadSlice('\n')
		t.out = append(t.out[:0], line...)
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			t.long = true
			continue
		case !t.long:
			t.out = insertTier(t.out, []byte("data: {"), t.member)
		}
		t.long, t.err = false, err
	}
	n := copy(p, t.out)
	t.out = t.out[n:]
	if len(t.out) > 0 {
		return n, nil
	}
	return n, t.err
}

func (t *tierLines) Close() error { return t.body.Close() }

// tierWhole puts the tier into a whole answer, read on the relay's first read.
type tierWhole struct {
	body   io.ReadCloser
	member []byte
	r      io.Reader
}

func (t *tierWhole) Read(p []byte) (int, error) {
	if t.r == nil {
		v, err := io.ReadAll(io.LimitReader(t.body, maxAnswer))
		if err != nil {
			return 0, err
		}
		t.r = bytes.NewReader(insertTier(v, []byte("{"), t.member))
	}
	return t.r.Read(p)
}

func (t *tierWhole) Close() error { return t.body.Close() }

// insertTier is b with member after the opening of the object that starts with open (leading space aside), when the
// object is sound JSON and has no service_tier member of its own; b as it is otherwise. Only the object's own members
// are read: a nested one, or text that reads "service_tier", is not the reply's tier.
func insertTier(b, open, member []byte) []byte {
	trimmed := bytes.TrimLeft(b, " \t\r\n")
	if !bytes.HasPrefix(trimmed, open) {
		return b
	}
	var top struct {
		Tier jsonx.Raw `json:"service_tier"`
	}
	if jsonx.Unmarshal(trimmed[len(open)-1:], &top) != nil || len(top.Tier) > 0 {
		return b
	}
	at := len(b) - len(trimmed) + len(open)
	if bytes.HasPrefix(bytes.TrimLeft(b[at:], " \t\r\n"), []byte("}")) {
		return b // an empty object, which a member and its comma would not leave sound
	}
	return bytes.Join([][]byte{b[:at], member, b[at:]}, nil)
}
