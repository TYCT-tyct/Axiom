package sse_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

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
// bytes or in a later read, with each line ending, even when the last event's blank line is a CR at the very end,
// which would otherwise wait for a byte that never comes; the unfinished event after them is not.
func TestReaderReadError(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n", "\r"} {
		for _, tail := range []string{"", "data: 3"} {
			stream := "data: 1" + eol + eol + "data: 2" + eol + eol + tail
			for name, src := range map[string]io.Reader{
				"separate read": io.MultiReader(strings.NewReader(stream), iotest.ErrReader(errRead)),
				"same read":     &dataThenErr{stream, errRead},
			} {
				t.Run(fmt.Sprintf("%q/%q/%s", eol, tail, name), func(t *testing.T) {
					events, err := readAll(src, 1<<10)
					if !cmp.Equal(events, []string{"1", "2"}) || !errors.Is(err, errRead) {
						t.Fatalf("readAll = %q, %v; want [1 2], %v", events, err, errRead)
					}
				})
			}
		}
	}
}

// stalls reads from src, then blocks until release is closed instead of reporting its end, as an upstream does
// between events.
type stalls struct {
	src     io.Reader
	release chan struct{}
}

func (s *stalls) Read(p []byte) (int, error) {
	n, err := s.src.Read(p)
	if n == 0 && errors.Is(err, io.EOF) {
		<-s.release
	}
	return n, err
}

// TestReaderHandsOverAtOnce: an event is returned as soon as its last byte has arrived, while the upstream still has
// nothing more to say, however the bytes were split into reads. A CR ends a line only once the next byte shows it is
// not half a CRLF, so that byte is part of the CR stream.
func TestReaderHandsOverAtOnce(t *testing.T) {
	for _, tc := range []struct{ name, stream string }{
		{"LF", "data: 1\n\n"},
		{"CRLF", "data: 1\r\n\r\n"},
		{"CR", "data: 1\r\rd"},
	} {
		for name, wrap := range map[string]func(io.Reader) io.Reader{"whole": func(r io.Reader) io.Reader { return r }, "byte by byte": iotest.OneByteReader} {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				release := make(chan struct{})
				t.Cleanup(func() { close(release) }) // lets a reader that failed this test finish
				r := sse.NewReader(&stalls{wrap(strings.NewReader(tc.stream)), release}, 1<<10)
				got := make(chan string, 1)
				go func() {
					data, err := r.Next()
					got <- fmt.Sprintf("%s, %v", data, err)
				}()
				select {
				case g := <-got:
					if g != "1, <nil>" {
						t.Fatalf("Next = %s, want 1, <nil>", g)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("Next did not return the event while the upstream was silent")
				}
			})
		}
	}
}

var errSlow = errors.New("past its deadline")

// filler reads n bytes of 'x', a few KiB per read, as a connection does, and fails with errSlow once stop has passed.
type filler struct {
	left int
	stop time.Time
}

func (f *filler) Read(p []byte) (int, error) {
	n := min(len(p), f.left, 4<<10)
	if n == 0 {
		return 0, io.EOF
	}
	if time.Now().After(f.stop) {
		return 0, errSlow
	}
	for i := range p[:n] {
		p[i] = 'x'
	}
	f.left -= n
	return n, nil
}

// TestReaderLineScales: reading a line that does not end takes time in proportion to its length (README.md「不变量」).
// A line eight times as long should take about eight times as long; scanning it again after every read, or moving it
// onto itself, takes sixty-four times, so one of five runs of the long line must finish within three times what
// proportion allows from the fastest run of the short one (the way TestPlanScales in routing holds its bound). A run that
// is past its deadline stops, so the quadratic reader fails in seconds.
func TestReaderLineScales(t *testing.T) {
	const small, large, runs = 1 << 20, 8 << 20, 5
	read := func(size int, within time.Duration) (time.Duration, error) {
		start := time.Now()
		_, err := sse.NewReader(&filler{left: size, stop: start.Add(within)}, 2*large).Next()
		return time.Since(start), err
	}
	fastest := time.Duration(math.MaxInt64)
	for range runs {
		took, err := read(small, time.Hour)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Next = %v, want %v", err, io.EOF)
		}
		fastest = min(fastest, took)
	}
	within := 3 * large / small * fastest
	for range runs {
		if _, err := read(large, within); errors.Is(err, io.EOF) {
			return
		}
	}
	t.Errorf("a line %d times as long took more than %v, %d times as long as the shorter, in each of %d runs", large/small, within, 3*large/small, runs)
}

// TestReaderAppendToEvent: the data of an event may share the reader's buffer, but not its room, so appending to it
// cannot overwrite the bytes that follow.
func TestReaderAppendToEvent(t *testing.T) {
	r := sse.NewReader(strings.NewReader("data: 1\n\ndata: 2\n\n"), 1<<10)
	first, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	_ = append(first, "-----------"...)
	if second, err := r.Next(); err != nil || string(second) != "2" {
		t.Fatalf("Next after appending to the first event = %q, %v; want 2", second, err)
	}
}

// steadyAllocs is testing.AllocsPerRun without the runtime's own allocations, which a collection during a run adds and
// which only ever raise a count: the collector is off and runs just before.
func steadyAllocs(f func()) float64 {
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	runtime.GC()
	return testing.AllocsPerRun(5, f)
}

// TestReaderAllocs: once the buffers have grown, reading an event allocates nothing, whether it has one data line or
// several, so a longer stream costs no more allocations (docs/performance.md「调用路径上的规则」).
func TestReaderAllocs(t *testing.T) {
	for _, event := range []string{"data: {\"a\":[1,\"b\"]}\n\n", "data: {\"a\":\ndata: [1,\"b\"]}\n\n"} {
		allocs := func(events int) float64 {
			stream := []byte(strings.Repeat(event, events))
			return steadyAllocs(func() {
				r := sse.NewReader(bytes.NewReader(stream), 1<<10)
				for {
					if _, err := r.Next(); err != nil {
						return
					}
				}
			})
		}
		if few, many := allocs(1000), allocs(2000); few != many {
			t.Errorf("%q: %v allocations for 1000 events, %v for 2000", event, few, many)
		}
	}
}

// drain returns the data of every event r reads to the end of its stream.
func drain(t *testing.T, r *sse.Reader) []string {
	t.Helper()
	var events []string
	for {
		data, err := r.Next()
		if errors.Is(err, io.EOF) {
			return events
		} else if err != nil {
			t.Fatal(err)
		}
		events = append(events, string(data))
	}
}

// TestReaderShrinks: a large event does not leave the stream holding its buffers: they are given back, to the budget
// ChargeGrowth reports to, once reads have stayed small for a while, but not between large events that follow one
// another, which would allocate them again every time.
func TestReaderShrinks(t *testing.T) {
	largeData := `"` + strings.Repeat("x", 512<<10) + `"`
	large := "data: " + largeData + "\n\n"
	small := "data: 1\n\n"
	// An event of many data lines, so that the reader's data buffer grows, followed by small events whose reads each
	// stop right after the event's data line: the reader has copied that line into the data buffer by the time it
	// could give the buffer back.
	lines := []string{"["}
	for range 4 {
		lines = append(lines, `"`+strings.Repeat("x", 128<<10)+`",`)
	}
	lines = append(lines, "0]")
	splitReads := []io.Reader{strings.NewReader("data: " + strings.Join(lines, "\ndata: ") + "\n\n"), strings.NewReader("data: 1\n")}
	for range 40 {
		splitReads = append(splitReads, strings.NewReader("\ndata: 1\n"))
	}
	splitReads = append(splitReads, strings.NewReader("\n"))
	for _, tc := range []struct {
		name         string
		stream       io.Reader
		events       []string
		wantReleased bool
		wantHeld     int // at most, at the end
	}{
		{
			// Byte by byte, so that some read finds the reader with a small event half read when it gives the buffers back.
			"small events after a large one",
			io.MultiReader(strings.NewReader(large), iotest.OneByteReader(strings.NewReader(strings.Repeat(small, 40)))),
			append([]string{largeData}, slices.Repeat([]string{"1"}, 40)...), true, 64 << 10,
		},
		{
			"reads ending after a data line",
			io.MultiReader(splitReads...),
			append([]string{strings.Join(lines, "\n")}, slices.Repeat([]string{"1"}, 41)...), true, 64 << 10,
		},
		{"large events in a row", strings.NewReader(strings.Repeat(large, 8)), slices.Repeat([]string{largeData}, 8), false, 4 * len(large)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var held, peak int
			released := false
			r := sse.NewReader(tc.stream, 2*len(large))
			r.ChargeGrowth(func(n int) error {
				held += n
				peak = max(peak, held)
				released = released || n < 0
				return nil
			})
			if events := drain(t, r); !slices.Equal(events, tc.events) {
				t.Errorf("read %d events, want %d, and these", len(events), len(tc.events))
			}
			if released != tc.wantReleased || held > tc.wantHeld || peak < len(largeData) {
				t.Errorf("released %t, holding %d at the end and %d at most; want released %t, at most %d at the end, at least %d at most",
					released, held, peak, tc.wantReleased, tc.wantHeld, len(largeData))
			}
		})
	}
}

// pollAll is readAll calling Poll, which must read at most once and ask for 16 KiB at most.
func pollAll(t *testing.T, src io.Reader, limit int) ([]string, error) {
	t.Helper()
	reads := &countReads{src: src}
	r := sse.NewReader(reads, limit)
	var events []string
	for {
		before := reads.n
		data, err := r.Poll()
		if reads.n > before+1 || reads.largest > 16<<10 {
			t.Fatalf("one Poll read %d times, asking for %d bytes at most", reads.n-before, reads.largest)
		}
		switch {
		case err != nil:
			return events, err
		case data != nil:
			events = append(events, string(data))
		}
	}
}

type countReads struct {
	src        io.Reader
	n, largest int
}

func (c *countReads) Read(p []byte) (int, error) {
	c.n++
	c.largest = max(c.largest, len(p))
	return c.src.Read(p)
}

// FuzzReader: however the stream is split into reads, the events and the final error are the same, whether read
// with Next or with Poll, and every event is Done or strictly valid JSON.
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
		polled, err := pollAll(t, &chunks{stream, cuts}, limit)
		if diff := cmp.Diff(wantEvents, polled); diff != "" || fmt.Sprint(err) != fmt.Sprint(wantErr) {
			t.Fatalf("polled, split %v: error %v, want %v; events (-whole +polled):\n%s", cuts, err, wantErr, diff)
		}
		for _, data := range events {
			if data != sse.Done && jsonx.Validate([]byte(data)) != nil {
				t.Fatalf("event %q is not valid JSON", data)
			}
		}
	})
}

// TestPending counts what has arrived and is not yet an event: lines, their line endings and the unfinished line.
func TestPending(t *testing.T) {
	src := io.MultiReader(strings.NewReader("data: 1\n\n: c\r\ndata: [1,"), strings.NewReader("2"), strings.NewReader("]\n\n"))
	r := sse.NewReader(src, 1<<10)
	for _, want := range []struct {
		data    string
		pending int
	}{{"1", len(": c\r\ndata: [1,")}, {"", len(": c\r\ndata: [1,2")}, {"[1,2]", 0}} {
		data, err := r.Poll()
		if err != nil || string(data) != want.data || r.Pending() != want.pending {
			t.Fatalf("Poll = %q, %v with %d pending; want %q with %d", data, err, r.Pending(), want.data, want.pending)
		}
	}
}

// TestChargeGrowth: the reader asks before each buffer grows, so what it holds never passes what was granted, and a
// refusal fails the reader with the refusal.
func TestChargeGrowth(t *testing.T) {
	errBudget := errors.New("budget spent")
	event := `data: "` + strings.Repeat("x", 100<<10) + "\"\n\n"
	for _, budget := range []int{64 << 10, 1 << 20} {
		granted := 0
		r := sse.NewReader(strings.NewReader(event), 1<<20)
		r.ChargeGrowth(func(n int) error {
			if granted+n > budget {
				return errBudget
			}
			granted += n
			return nil
		})
		data, err := r.Next()
		if fits := budget > 4*len(event); fits != (err == nil) || !fits && !errors.Is(err, errBudget) {
			t.Errorf("budget %d: error %v after granting %d", budget, err, granted)
		}
		if err == nil && granted < len(data) { // the event is held in the buffer at least
			t.Errorf("budget %d: granted %d for an event of %d", budget, granted, len(data))
		}
	}
}

// TestAppendEvent: what AppendEvent writes reads back as the same data, in at most EventSize bytes, with an event line
// only for a name that cannot change the framing.
func TestAppendEvent(t *testing.T) {
	for _, c := range []struct {
		name, data string
		eventLine  bool
	}{
		{"message_start", `{"a":1}`, true},
		{"response.output_text.delta", "[1,\n2]", true},
		{"", "{}", false},
		{"a\ndata: 1", "{}", false},
		{"a b", "{}", false},
	} {
		out := sse.AppendEvent([]byte("x"), c.name, []byte(c.data))[1:]
		events, err := readAll(bytes.NewReader(out), 1<<10)
		if !cmp.Equal(events, []string{c.data}) || !errors.Is(err, io.EOF) ||
			len(out) > sse.EventSize(c.name, []byte(c.data)) || strings.HasPrefix(string(out), "event: ") != c.eventLine {
			t.Errorf("AppendEvent(%q, %q) = %q, read back as %q, %v", c.name, c.data, out, events, err)
		}
	}
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
