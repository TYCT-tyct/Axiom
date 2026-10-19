package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// loadUpstream is the supplier of the load test: a relay that answers /v1/messages with a Messages stream whose first
// token, speed, length and end the scenario sets, that breaks some streams off, and that notes when it got each call, wrote
// its first and last byte, or saw it cancelled. The calls are told apart by the "load-id:<n>;" the client puts at the
// start of its message, which the gateway passes on unchanged.
type loadUpstream struct {
	server *httptest.Server
	shape  atomic.Pointer[streamShape]

	mu     sync.Mutex
	stamps map[uint64]stamp

	active, peak, cancelled atomic.Int64 // streams open now, the most there were at once, and calls cancelled by their caller
}

// streamShape is how the upstream answers the calls that arrive while it is set.
type streamShape struct {
	firstToken time.Duration // from the whole request read to the first byte
	every      time.Duration // between two content events; each carries one output token
	length     time.Duration // how long the content events run after the first token
	endAt      time.Time     // when set, the closing events wait for this instant: streams that end together
	cutRate    float64       // the share of streams broken off at a random point, without an ending
}

// deltas is how many content events (output tokens) a stream has: at least one, or the reply would be free.
func (s streamShape) deltas() int {
	if s.every <= 0 {
		return 1
	}
	return max(1, int(s.length/s.every))
}

// stamp is what the upstream saw of one call.
type stamp struct {
	received, firstWrite, endWrite, cancelled time.Time
	deltas                                    int // the content events the stream was to have; the usage it reports
	cut                                       bool
}

// The usage every stream reports: the input is fixed and the output is one token per content event, priced as the
// recorded samples are (callpath_world_test.go), so what a completed call must be charged is known.
const (
	loadInputTokens = 25
	inputNano       = 4_500
	outputNano      = 22_500
)

func loadCharge(deltas int) int64 { return loadInputTokens*inputNano + int64(deltas)*outputNano }

const (
	streamStart = `event: message_start
data: {"type":"message_start","message":{"id":"msg_load","type":"message","role":"assistant","content":[],"model":"claude-opus-5-5","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

`
	streamDelta = `event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"token "}}

`
	streamEnd = `event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":%d}}

event: message_stop
data: {"type":"message_stop"}

`
	idMarker = "load-id:"
)

func newLoadUpstream(t testing.TB, shape streamShape) *loadUpstream {
	t.Helper()
	u := &loadUpstream{stamps: map[uint64]stamp{}}
	u.shape.Store(&shape)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", u.serve)
	u.server = httptest.NewServer(mux)
	t.Cleanup(u.server.Close)
	return u
}

func (u *loadUpstream) setShape(s streamShape) { u.shape.Store(&s) }

// take is what the upstream saw of call id, forgotten once taken.
func (u *loadUpstream) take(id uint64) (stamp, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s, ok := u.stamps[id]
	delete(u.stamps, id)
	return s, ok
}

func (u *loadUpstream) note(id uint64, update func(*stamp)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.stamps[id]
	update(&s)
	u.stamps[id] = s
}

// callID reads the request to its end and returns the id the client put at the start of it; the rest is discarded
// unread into memory, so a request of megabytes costs the upstream nothing.
func callID(body io.Reader) (uint64, error) {
	head := make([]byte, 1024)
	n, err := io.ReadFull(body, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) { // a request shorter than the head
		return 0, err
	}
	if _, err := io.Copy(io.Discard, body); err != nil {
		return 0, err
	}
	_, after, ok := bytes.Cut(head[:n], []byte(idMarker))
	digits, _, _ := bytes.Cut(after, []byte(";"))
	if !ok {
		return 0, fmt.Errorf("the request carries no %q", idMarker)
	}
	return strconv.ParseUint(string(digits), 10, 64)
}

func (u *loadUpstream) serve(w http.ResponseWriter, r *http.Request) {
	id, err := callID(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	shape := *u.shape.Load()
	u.open()
	defer u.active.Add(-1)
	u.note(id, func(s *stamp) { s.received, s.deltas = time.Now(), shape.deltas() })
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Oneapi-Request-Id", "relay-"+strconv.FormatUint(id, 10))
	if !u.stream(w, r, id, shape) {
		u.cancelled.Add(1)
		u.note(id, func(s *stamp) { s.cancelled = time.Now() })
	}
}

func (u *loadUpstream) open() {
	now := u.active.Add(1)
	for {
		peak := u.peak.Load()
		if now <= peak || u.peak.CompareAndSwap(peak, now) {
			return
		}
	}
}

// stream writes the answer on the shape's schedule and reports whether the caller stayed to the end of it. The first
// token is the first content event, written with the message's start: the gateway holds a stream back until it has
// content (it commits to an attempt only then), so a start sent earlier would only be held.
func (u *loadUpstream) stream(w http.ResponseWriter, r *http.Request, id uint64, shape streamShape) (stayed bool) {
	flush := http.NewResponseController(w).Flush
	if !sleepUntil(r, time.Now().Add(shape.firstToken)) {
		return false
	}
	start := time.Now()
	writeEvent(w, flush, streamStart+streamDelta)
	u.note(id, func(s *stamp) { s.firstWrite = start })
	n := shape.deltas()
	cutAt := -1
	if n > 1 && rand.Float64() < shape.cutRate { //nolint:gosec // shapes the load; nothing depends on it being unpredictable
		cutAt = 1 + rand.IntN(n-1) //nolint:gosec // as above
	}
	for i := 1; i < n; i++ {
		if i == cutAt {
			u.note(id, func(s *stamp) { s.cut = true })
			panic(http.ErrAbortHandler) // the connection is closed without the chunked body's end
		}
		if !sleepUntil(r, start.Add(time.Duration(i)*shape.every)) {
			return false
		}
		writeEvent(w, flush, streamDelta)
	}
	if !shape.endAt.IsZero() && !sleepUntil(r, shape.endAt) {
		return false
	}
	u.note(id, func(s *stamp) { s.endWrite = time.Now() })
	writeEvent(w, flush, fmt.Sprintf(streamEnd, n))
	return true
}

func writeEvent(w http.ResponseWriter, flush func() error, s string) {
	if _, err := io.WriteString(w, s); err == nil {
		_ = flush() //nolint:errcheck // a caller that went away is seen by the next wait, which returns
	}
}

// sleepUntil waits for t and reports false when the caller went away first.
func sleepUntil(r *http.Request, t time.Time) bool {
	timer := time.NewTimer(time.Until(t))
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-r.Context().Done():
		return false
	}
}
