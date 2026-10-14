package sse_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

const bom = "\xef\xbb\xbf"

var (
	errRead      = errors.New("connection reset")
	errNotSticky = errors.New("a failed Next was followed by another error")
)

// readAll returns the data of every event in src, and the error that ended the stream. Once Next has failed, it
// checks that the next call fails the same way.
func readAll(src io.Reader, limit int) ([]string, error) {
	r := sse.NewReader(src, limit)
	var events []string
	for {
		data, err := r.Next()
		if err != nil {
			if _, again := r.Next(); again != err { //nolint:errorlint // the very same error, not one that wraps it
				return events, errNotSticky
			}
			return events, err
		}
		events = append(events, string(data))
	}
}

// chunks returns data in reads whose sizes come from cuts, so a fuzz input decides where the stream is split.
type chunks struct{ data, cuts []byte }

func (c *chunks) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := len(c.data)
	if len(c.cuts) > 0 {
		n = min(n, 1+int(c.cuts[0]))
		c.cuts = c.cuts[1:]
	}
	n = copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

func TestReader(t *testing.T) {
	for _, tc := range []struct {
		name, stream string
		limit        int // 0: 1 KiB
		events       []string
		err          error // io.EOF: the stream ended cleanly
	}{
		{name: "LF", stream: "data: {\"a\":1}\n\n", events: []string{`{"a":1}`}, err: io.EOF},
		{name: "CRLF", stream: "data: 1\r\n\r\n", events: []string{"1"}, err: io.EOF},
		{name: "CR", stream: "data: 1\r\rdata: 2\r\r", events: []string{"1", "2"}, err: io.EOF},
		{name: "mixed line endings", stream: "data: 1\r\n\ndata: 2\r\r\n", events: []string{"1", "2"}, err: io.EOF},
		{name: "data lines joined by LF", stream: "data: [1,\ndata: 2]\n\n", events: []string{"[1,\n2]"}, err: io.EOF},
		{name: "one space stripped", stream: "data:1\n\ndata:  2\n\n", events: []string{"1", " 2"}, err: io.EOF},
		{name: "field without colon", stream: "data\ndata: 1\n\n", events: []string{"\n1"}, err: io.EOF},
		{
			name:   "other fields and comments ignored",
			stream: ": ping\nevent: message_start\nid: 7\nretry: 10\nfoo: bar\ndata: {}\n\n",
			events: []string{"{}"}, err: io.EOF,
		},
		{name: "no data, no event", stream: ": keep-alive\n\nevent: ping\n\n\n\n", err: io.EOF},
		{name: "byte order mark", stream: bom + "data: 1\n\n", events: []string{"1"}, err: io.EOF},
		{name: "byte order mark only at the start", stream: "data: 1\n\n" + bom + "data: 2\n\n", events: []string{"1"}, err: io.EOF},
		{name: "done", stream: "data: {}\n\ndata: [DONE]\n\n", events: []string{"{}", sse.Done}, err: io.EOF},
		{name: "event without blank line", stream: "data: 1\n\ndata: [DONE]", events: []string{"1"}, err: io.ErrUnexpectedEOF},
		{name: "stream ends inside an event", stream: "data: 1\n", err: io.ErrUnexpectedEOF},
		{name: "stream ends after a field without data", stream: "event: done\n: bye", err: io.EOF},
		{name: "invalid JSON", stream: "data: {\n\ndata: 1\n\n", err: sse.ErrInvalid},
		{name: "duplicate name", stream: "data: {\"a\":1,\"a\":2}\n\n", err: sse.ErrInvalid},
		{name: "invalid UTF-8", stream: "data: \"\xff\"\n\n", err: sse.ErrInvalid},
		{name: "multi-byte UTF-8", stream: "data: \"\xc3\xa9\"\n\n", events: []string{"\"\xc3\xa9\""}, err: io.EOF},
		{name: "empty data", stream: "data:\n\n", err: sse.ErrInvalid},
		{name: "at the limit", stream: "data: 1\r\n\r\n", limit: 11, events: []string{"1"}, err: io.EOF},
		{name: "over the limit", stream: "data: 1\r\n\r\n", limit: 10, err: sse.ErrTooLarge},
		{name: "comments count", stream: ": 4567\ndata: 1\n\n", limit: 15, err: sse.ErrTooLarge},
		{name: "limit is per event", stream: "data: 1\n\ndata: 2\n\n", limit: 9, events: []string{"1", "2"}, err: io.EOF},
		{name: "limit restarts after no event", stream: ": 34567\n\ndata: 1\n\n", limit: 9, events: []string{"1"}, err: io.EOF},
		{name: "unfinished line over the limit", stream: "data: 12345678", limit: 10, err: sse.ErrTooLarge},
	} {
		limit := tc.limit
		if limit == 0 {
			limit = 1 << 10
		}
		for _, read := range []struct {
			name string
			wrap func(io.Reader) io.Reader
		}{
			{"whole", func(r io.Reader) io.Reader { return r }},
			{"byte by byte", iotest.OneByteReader},
			{"data with EOF", iotest.DataErrReader},
		} {
			t.Run(tc.name+"/"+read.name, func(t *testing.T) {
				events, err := readAll(read.wrap(strings.NewReader(tc.stream)), limit)
				if diff := cmp.Diff(tc.events, events); diff != "" {
					t.Errorf("events (-want +got):\n%s", diff)
				}
				checkError(t, err, tc.err)
			})
		}
	}
}

// checkError checks that err is in the class of want and no other: a truncated JSON value must not read as a
// truncated stream.
func checkError(t *testing.T, err, want error) {
	t.Helper()
	for _, class := range []error{io.EOF, io.ErrUnexpectedEOF, sse.ErrTooLarge, sse.ErrInvalid} {
		if errors.Is(err, class) != errors.Is(want, class) {
			t.Errorf("error = %v, want %v", err, want)
			return
		}
	}
}

// TestReaderLongLine: a line that does not end fails as soon as it passes the limit, instead of being buffered until
// it ends, which an upstream may never do.
func TestReaderLongLine(t *testing.T) {
	src := strings.NewReader(":" + strings.Repeat("x", 1<<20))
	_, err := readAll(src, 1<<10)
	if read := src.Size() - int64(src.Len()); !errors.Is(err, sse.ErrTooLarge) || read > 64<<10 {
		t.Fatalf("error %v after reading %d bytes, want %v within the first reads", err, read, sse.ErrTooLarge)
	}
}

// dataThenErr returns all its data together with err in a single read, as a reader may when a connection breaks.
type dataThenErr struct {
	data string
	err  error
}

func (d *dataThenErr) Read(p []byte) (int, error) {
	n := copy(p, d.data)
	d.data = d.data[n:]
	return n, d.err
}

// TestReaderReadError: the events complete before a read fails are returned whether the failure comes with the last
// bytes or in a later read; the unfinished event after them is not.
func TestReaderReadError(t *testing.T) {
	const stream = "data: 1\n\ndata: 2\n\ndata: 3"
	for name, src := range map[string]io.Reader{
		"separate read": io.MultiReader(strings.NewReader(stream), iotest.ErrReader(errRead)),
		"same read":     &dataThenErr{stream, errRead},
	} {
		t.Run(name, func(t *testing.T) {
			events, err := readAll(src, 1<<10)
			if !cmp.Equal(events, []string{"1", "2"}) || !errors.Is(err, errRead) {
				t.Fatalf("readAll = %q, %v; want [1 2], %v", events, err, errRead)
			}
		})
	}
}

// FuzzReader: however the stream is split into reads, the events and the final error are the same, and every event
// is Done or strictly valid JSON.
func FuzzReader(f *testing.F) {
	f.Add([]byte("data: 1\r\n\r\ndata: {\"a\":\n: c\rdata: 2}\r\r"), []byte{0, 3, 9})
	f.Add([]byte(bom+"data: [DONE]\n\n"), []byte{0, 1})
	f.Add([]byte("data: \"\xc3\xa9\"\r\n\r\n"), []byte{7})
	f.Fuzz(func(t *testing.T, stream, cuts []byte) {
		const limit = 64 // small, so the limit is reached often
		wantEvents, wantErr := readAll(bytes.NewReader(stream), limit)
		events, err := readAll(&chunks{stream, cuts}, limit)
		if diff := cmp.Diff(wantEvents, events); diff != "" || fmt.Sprint(err) != fmt.Sprint(wantErr) {
			t.Fatalf("split %v: error %v, want %v; events (-whole +split):\n%s", cuts, err, wantErr, diff)
		}
		for _, data := range events {
			if data != sse.Done && jsonx.Validate([]byte(data)) != nil {
				t.Fatalf("event %q is not valid JSON", data)
			}
		}
	})
}

func BenchmarkReader(b *testing.B) {
	for _, size := range []int{1 << 10, 1 << 20} {
		text := strings.Repeat("x", size)
		stream := []byte(strings.Repeat(`data: {"type":"content_block_delta","delta":{"text":"`+text+`"}}`+"\n\n", 16))
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(stream)))
			for b.Loop() {
				r := sse.NewReader(bytes.NewReader(stream), 2<<20)
				for {
					if _, err := r.Next(); errors.Is(err, io.EOF) {
						break
					} else if err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
