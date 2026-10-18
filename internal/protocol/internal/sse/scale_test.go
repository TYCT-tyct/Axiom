package sse

import (
	"errors"
	"io"
	"math/bits"
	"testing"
)

// chunks reads n bytes of 'x', a few KiB per read, as a connection does.
type chunks struct{ left int }

func (c *chunks) Read(p []byte) (int, error) {
	n := min(len(p), c.left, 4<<10)
	if n == 0 {
		return 0, io.EOF
	}
	for i := range p[:n] {
		p[i] = 'x'
	}
	c.left -= n
	return n, nil
}

// TestReaderLineWorkIsLinear: reading a line that does not end costs work in proportion to its length (README.md
// 「不变量」), counted rather than timed, so a loaded machine cannot fail it. The search for the line ending resumes
// after the bytes already searched, so every byte is searched once: a reader that searched the whole pending line after
// every read would search it quadratically often. And the buffer grows by doubling, so it is allocated a number of
// times that grows with the logarithm of the line, not once per read.
func TestReaderLineWorkIsLinear(t *testing.T) {
	const size = 8 << 20
	r := NewReader(&chunks{left: size}, 2*size)
	growths := 0
	r.ChargeGrowth(func(int) error { growths++; return nil })
	searched, reads := 0, 0
	for {
		before := r.scan // the bytes of the pending line the next search skips
		data, err := r.Poll()
		if err != nil {
			if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
				t.Fatalf("Poll = %v", err)
			}
			break
		}
		if data != nil {
			t.Fatalf("Poll returned an event from a line that does not end")
		}
		searched += len(r.buf) - r.start - before
		reads++
	}
	if reads < size/(4<<10) {
		t.Fatalf("%d reads for %d bytes: the source was not read in pieces", reads, size)
	}
	if searched > size+readSize {
		t.Errorf("%d bytes searched for a line of %d: a byte is searched more than once", searched, size)
	}
	if limit := 2 * bits.Len(uint(size/readSize)); growths > limit {
		t.Errorf("the buffer grew %d times for a line of %d, more than %d: it does not grow by doubling", growths, size, limit)
	}
}
