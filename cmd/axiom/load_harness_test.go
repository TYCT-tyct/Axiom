package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These test the load harness's own parts (load_stats_test.go, load_upstream_test.go, load_proxy_test.go): a harness
// that miscounts, or paces a stream wrongly, would put wrong numbers in the report with nothing to show it.

func TestLoadQuantile(t *testing.T) {
	t.Parallel()
	hundred := make(latencies, 100)
	for i := range hundred {
		hundred[100-1-i] = time.Duration(i+1) * time.Millisecond // shuffled in effect: descending
	}
	for name, tc := range map[string]struct {
		in   latencies
		q    float64
		want time.Duration
	}{
		"median of a hundred":      {hundred, 0.50, 50 * time.Millisecond},
		"p95 is the 95th":          {hundred, 0.95, 95 * time.Millisecond},
		"p99 is the 99th":          {hundred, 0.99, 99 * time.Millisecond},
		"the maximum":              {hundred, 1, 100 * time.Millisecond},
		"one sample is every rank": {latencies{7 * time.Millisecond}, 0.99, 7 * time.Millisecond},
		"p95 of 3 rounds up":       {latencies{1, 2, 3}, 0.95, 3},
		"nothing measured":         {nil, 0.95, 0},
	} {
		if got := tc.in.quantile(tc.q); got != tc.want {
			t.Errorf("%s: quantile(%v) = %v, want %v", name, tc.q, got, tc.want)
		}
	}
	if hundred[0] != 100*time.Millisecond {
		t.Error("quantile sorted the caller's samples in place")
	}
}

// TestSummarizeSortsCallsByWhatHappenedToThem: every call lands in exactly one of done, refused, lost on purpose and
// failed, in that order of precedence; the gateway's overhead is the first byte's time less the upstream's own, the
// settle tail is the time from the upstream's last byte to the client's, and neither is taken from a call that lacks
// the upstream's side.
func TestSummarizeSortsCallsByWhatHappenedToThem(t *testing.T) {
	t.Parallel()
	t0 := time.Now()
	reached := stamp{received: t0, firstWrite: t0.Add(30 * time.Millisecond), endWrite: t0.Add(500 * time.Millisecond), deltas: 4}
	cut := reached
	cut.cut = true
	outs := []outcome{
		{status: 200, first: 40 * time.Millisecond, done: true, eof: reached.endWrite.Add(3 * time.Millisecond), up: reached, seen: true},
		{status: 200, first: 70 * time.Millisecond, done: true, pending: true, eof: reached.endWrite.Add(9 * time.Millisecond), up: reached, seen: true},
		{status: 429},
		{status: 503},
		{status: 200, first: 10 * time.Millisecond, cancelled: true, up: reached, seen: true},
		{status: 200, first: 10 * time.Millisecond, up: cut, seen: true, err: "unexpected EOF"},
		{status: 500},
		{status: 0, err: "dial"},
		{status: 200, first: time.Millisecond, seen: true, up: reached}, // cut short with no reason given
		{status: 200, first: 5 * time.Millisecond, done: true, eof: t0}, // never reached the upstream: nothing to subtract
	}
	row := summarize("s", outs)
	if row.Calls != 10 || row.Done != 3 || row.Refused != 2 || row.Lost != 2 || row.Failed != 3 {
		t.Errorf("%d calls: %d done, %d refused, %d lost, %d failed; want 10: 3, 2, 2, 3", row.Calls, row.Done, row.Refused, row.Lost, row.Failed)
	}
	if len(row.Overhead) != 5 {
		t.Errorf("overheads %v, want one for each of the five calls that reached the upstream and were answered", row.Overhead)
	}
	if got := row.Overhead.quantile(1); got != 40*time.Millisecond {
		t.Errorf("the largest overhead is %v, want 40ms (70ms first byte less the upstream's 30ms)", got)
	}
	if want := (latencies{3 * time.Millisecond, 9 * time.Millisecond}); !slices.Equal(row.Settle, want) {
		t.Errorf("settle tails %v, want %v", row.Settle, want)
	}
	if want := "429: 1, 503: 1, >1s: 0, cost pending: 1"; row.Wait != want {
		t.Errorf("queueing %q, want %q", row.Wait, want)
	}
	if got := failures(outs); !strings.Contains(got, "1 x status 500") || !strings.Contains(got, "1 x status 0 dial") {
		t.Errorf("failures %q do not name the 500 and the dial error", got)
	}
}

func TestChargeBounds(t *testing.T) {
	t.Parallel()
	seen := outcome{status: 200, done: true, seen: true, up: stamp{deltas: 4}}
	unfinished := outcome{status: 200, bodyBytes: 400}
	owed := seen
	owed.pending = true // ended without its cost, which has been settled by the time the ledger is read
	low, high := chargeBounds([]outcome{seen, seen, unfinished, owed, {status: 429}})
	if want := 3 * loadCharge(4); low != want {
		t.Errorf("the lowest charge is %d, want exactly the three finished calls' %d, the cost-pending one's included", low, want)
	}
	if want := low + (101*inputNano + maxOutputTokens*outputNano); high != want {
		t.Errorf("the highest charge is %d, want %d: the unfinished call may be charged up to its reservation, the refused one nothing", high, want)
	}
}

// TestChargeMismatchesFindsWhatTheTotalHides: each completed call must have been charged its own price, a cost-pending
// one included; a missing charge is not made up by another call's extra one, and a call with no row is wrong.
func TestChargeMismatchesFindsWhatTheTotalHides(t *testing.T) {
	t.Parallel()
	call := func(id string, pending bool) outcome {
		return outcome{request: id, status: 200, done: true, seen: true, pending: pending, up: stamp{deltas: 4}}
	}
	outs := []outcome{call("a", false), call("b", true), call("c", true), {request: "d", status: 200}} // d did not complete: not checked
	price := loadCharge(4)
	for name, tc := range map[string]struct {
		charged map[string]int64
		want    int
	}{
		"all right":                     {map[string]int64{"a": price, "b": price, "c": price}, 0},
		"a pending call charged zero":   {map[string]int64{"a": price, "b": 0, "c": price}, 1},
		"a double charge":               {map[string]int64{"a": price, "b": 2 * price, "c": price}, 1},
		"one short, one over, same sum": {map[string]int64{"a": price, "b": 0, "c": 2 * price}, 2},
		"no row":                        {map[string]int64{"a": price, "b": price}, 1},
	} {
		if got := chargeMismatches(outs, tc.charged); len(got) != tc.want {
			t.Errorf("%s: %d mismatches %v, want %d", name, len(got), got, tc.want)
		}
	}
}

// TestAnUnexpectedFailureFailsAcceptance: a 500 among calls that otherwise went well is a failed scenario, while a
// refusal for lack of room, a cancel and a cut are not.
func TestAnUnexpectedFailureFailsAcceptance(t *testing.T) {
	t.Parallel()
	reached := stamp{deltas: 4}
	cut := reached
	cut.cut = true
	expected := []outcome{
		{status: 200, done: true, seen: true, up: reached},
		{status: 429},
		{status: 503},
		{status: 200, cancelled: true, seen: true, up: reached},
		{status: 200, seen: true, up: cut},
	}
	if err := summarize("s", expected).unexpected(expected); err != nil {
		t.Errorf("expected endings failed the scenario: %v", err)
	}
	with500 := append(slices.Clone(expected), outcome{status: 500, err: "internal"})
	err := summarize("s", with500).unexpected(with500)
	if err == nil || !strings.Contains(err.Error(), "1 x status 500") {
		t.Errorf("one 500 among the calls gave %v, want a failure that names it", err)
	}
}

func charKind(c byte) string {
	for name, set := range map[string]string{"digit": digits, "lower": lower, "upper": upper} {
		if strings.IndexByte(set, c) >= 0 {
			return name
		}
	}
	return "other"
}

func TestUnknownKeyKeepsItsShape(t *testing.T) {
	t.Parallel()
	text := "aaaa-1111-aaaa-1111-AAAA-1111-aaaa-1111" // lower case, digits and capitals, in the places a key has them
	got := unknownKey(text, func(n int) int { return n - 1 })
	if got == text || len(got) != len(text) || got[:20] != text[:20] {
		t.Fatalf("unknownKey(%q) = %q; want the same length, the same start, and another text", text, got)
	}
	for i := range text {
		if charKind(text[i]) != charKind(got[i]) {
			t.Errorf("character %d changed kind: %q to %q", i, text[i], got[i])
		}
	}
}

func TestLoadRowHasAsManyCellsAsTheHeader(t *testing.T) {
	t.Parallel()
	cells := func(line string) int { return strings.Count(strings.TrimSpace(line), "|") - 1 }
	header, rule, _ := strings.Cut(loadHeader, "\n")
	if cells(rule) != cells(header) {
		t.Errorf("the header has %d cells and the line under it %d", cells(header), cells(rule))
	}
	empty, full := loadRow{Scenario: "s"}.markdown(), loadRow{
		Scenario: "s", Calls: 3, Lost: 1, Overhead: latencies{time.Millisecond}, TTFT: latencies{time.Millisecond}, Settle: latencies{time.Millisecond}, PeakMiB: 9,
	}.markdown()
	for _, row := range []string{empty, full} {
		if cells(row) != cells(header) {
			t.Errorf("row %q has %d cells, the header %d", row, cells(row), cells(header))
		}
	}
	if !strings.Contains(empty, "| - |") || !strings.Contains(full, "| 9 |") {
		t.Errorf("an unmeasured cell must read -, a measured one its value:\n%s%s", empty, full)
	}
}

func TestStatusBytes(t *testing.T) {
	t.Parallel()
	status := "Name:\tx\nVmHWM:\t  2048 kB\nVmRSS:\t   1024 kB\nVmSwap:\t0 MB\n"
	for name, tc := range map[string]struct {
		field   string
		want    int64
		wantErr bool
	}{
		"resident": {"VmRSS", 1 << 20, false}, "peak": {"VmHWM", 2 << 20, false},
		"absent": {"VmData", 0, true}, "not in kB": {"VmSwap", 0, true},
	} {
		got, err := statusBytes(status, tc.field)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: statusBytes(%s) = %d, %v; want %d, error %v", name, tc.field, got, err, tc.want, tc.wantErr)
		}
	}
	if runtime.GOOS == "linux" {
		if now, peak, err := processRSS(os.Getpid()); err != nil || now <= 0 || peak < now {
			t.Errorf("this process: resident %d, peak %d, %v; want a positive size and a peak not below it", now, peak, err)
		}
	}
}

// post sends a call with id to the upstream and returns the response, whose body the caller reads.
func post(t *testing.T, ctx context.Context, u *loadUpstream, id uint64) *http.Response {
	t.Helper()
	body := `{"messages":[{"role":"user","content":"` + idMarker + strconv.FormatUint(id, 10) + `; ` + strings.Repeat("x", 4096) + `"}]}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestLoadUpstreamPacesAndReportsCoherentUsage: the stream waits for its first token, sends one content event per
// token, and ends with a usage that counts them (the charge of a completed call is then known); the stamps order the
// call's moments, and a stream told to end at an instant does not end before it.
func TestLoadUpstreamPacesAndReportsCoherentUsage(t *testing.T) {
	t.Parallel()
	endAt := time.Now().Add(400 * time.Millisecond)
	u := newLoadUpstream(t, streamShape{firstToken: 50 * time.Millisecond, every: 10 * time.Millisecond, length: 50 * time.Millisecond, endAt: endAt})
	start := time.Now()
	resp := post(t, t.Context(), u, 7)
	defer resp.Body.Close() //nolint:errcheck // a test client
	lines := bufio.NewReader(resp.Body)
	first, err := lines.ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "event: message_start") {
		t.Fatalf("the first line is %q, %v; want the message_start event", first, err)
	}
	if got := time.Since(start); got < 50*time.Millisecond {
		t.Errorf("the first byte came after %v, before the first token's delay", got)
	}
	rest, err := io.ReadAll(lines)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rest)
	if n := strings.Count(text, `"type":"text_delta"`); n != 5 {
		t.Errorf("%d content events, want 5 (length / every)", n)
	}
	if !strings.Contains(text, `"usage":{"output_tokens":5}`) || !strings.HasSuffix(text, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") {
		t.Errorf("the stream does not end with a usage of 5 output tokens and message_stop:\n%s", text)
	}
	checkStamp(t, u, endAt)
}

func checkStamp(t *testing.T, u *loadUpstream, endAt time.Time) {
	t.Helper()
	s, ok := u.take(7)
	if !ok || s.deltas != 5 || s.cut || !s.received.Before(s.firstWrite) || !s.firstWrite.Before(s.endWrite) || s.endWrite.Before(endAt) {
		t.Errorf("the stamp is %+v (found %v); want the moments in order, the end not before %v, 5 tokens, not cut", s, ok, endAt)
	}
	if _, again := u.take(7); again {
		t.Error("a stamp was kept after it was taken")
	}
	if loadCharge(5) != 25*4_500+5*22_500 {
		t.Errorf("a completed call of 5 tokens is charged %d", loadCharge(5))
	}
}

// TestLoadUpstreamCuts and TestLoadUpstreamSeesCancels: a stream the upstream breaks off ends in a read error and is stamped cut, and a
// caller that goes away is counted cancelled, with the moment.
func TestLoadUpstreamCuts(t *testing.T) {
	t.Parallel()
	cutting := newLoadUpstream(t, streamShape{every: time.Hour})
	cutting.setShape(streamShape{every: 5 * time.Millisecond, length: 100 * time.Millisecond, cutRate: 1})
	resp := post(t, t.Context(), cutting, 1)
	defer resp.Body.Close() //nolint:errcheck // a test client
	if body, err := io.ReadAll(resp.Body); err == nil || strings.Contains(string(body), "message_stop") {
		t.Errorf("a cut stream read to the end: %v, %q", err, body)
	}
	if s, _ := cutting.take(1); !s.cut {
		t.Errorf("the cut stream is not stamped cut: %+v", s)
	}
}

func TestLoadUpstreamSeesCancels(t *testing.T) {
	t.Parallel()
	waiting := newLoadUpstream(t, streamShape{every: 20 * time.Millisecond, length: time.Minute})
	ctx, cancel := context.WithCancel(t.Context())
	resp := post(t, ctx, waiting, 2)
	defer resp.Body.Close() //nolint:errcheck // a test client
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if waiting.active.Load() != 1 || waiting.peak.Load() != 1 {
		t.Errorf("%d streams open, peak %d; want 1 and 1", waiting.active.Load(), waiting.peak.Load())
	}
	cancel()
	select {
	case id := <-waiting.cancels: // published after the stamp was written
		if id != 2 {
			t.Fatalf("the upstream saw call %d go, want 2", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream never saw the caller go")
	}
	if waiting.cancelled.Load() != 1 {
		t.Errorf("%d calls counted cancelled, want 1", waiting.cancelled.Load())
	}
	if s, _ := waiting.take(2); s.cancelled.IsZero() || !s.endWrite.IsZero() {
		t.Errorf("the cancelled call's stamp is %+v; want the cancel noted and no ending written", s)
	}
}

// echoServer answers every byte it is sent with the same byte.
func echoServer(t *testing.T) string {
	t.Helper()
	echo, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() }) //nolint:errcheck // a test server
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()      //nolint:errcheck // a test server
				_, _ = io.Copy(c, c) //nolint:errcheck // ends when the client does
			}()
		}
	}()
	return echo.Addr().String()
}

// roundTrip sends four bytes through c and reads them back.
func roundTrip(c net.Conn) error {
	if _, err := c.Write([]byte("ping")); err != nil {
		return err
	}
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	_, err := io.ReadFull(c, make([]byte, 4))
	return err
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() }) //nolint:errcheck // a test client
	return c
}

func TestFaultProxyCutsAndRestores(t *testing.T) {
	t.Parallel()
	p := newFaultProxy(t, echoServer(t))
	live := dial(t, p.addr())
	if err := roundTrip(live); err != nil {
		t.Fatalf("through the open proxy: %v", err)
	}
	p.setDown(true)
	if err := roundTrip(live); err == nil {
		t.Error("a connection open when the proxy went down still works")
	}
	// A connection made while it is down may be accepted by the listener, but is closed at once.
	if err := roundTrip(dial(t, p.addr())); err == nil {
		t.Error("a connection made while the proxy is down works")
	}
	p.setDown(false)
	if err := roundTrip(dial(t, p.addr())); err != nil {
		t.Errorf("after the proxy came back: %v", err)
	}
}

func TestCPUTicks(t *testing.T) {
	t.Parallel()
	busy, total, err := parseMachineTicks("cpu  100 0 50 800 40 5 5 0 0 0" + string(rune(10)) + "cpu0 1 2 3")
	if err != nil || busy != 160 || total != 1000 { // user nice system idle iowait irq softirq steal: busy is all but idle and iowait
		t.Errorf("machine ticks: busy %d, total %d, %v; want 160 of 1000", busy, total, err)
	}
	if _, _, err := parseMachineTicks("cpu0 1 2 3"); err == nil {
		t.Error("another line was taken for the machine's")
	}
	// The command name may hold spaces and parentheses; utime and stime are the 14th and 15th fields.
	ticks, err := parseProcessTicks("123 (a (b) c) S 1 2 3 4 5 6 7 8 9 10 40 2 0 0 20 0 1 0 100 1000 100")
	if err != nil || ticks != 42 {
		t.Errorf("process ticks %d, %v; want 42", ticks, err)
	}
	if _, err := parseProcessTicks("123 (x) S 1"); err == nil {
		t.Error("a short line was accepted")
	}
	start := cpuTicks{busy: 0, total: 0, process: 0}
	if got := (cpuTicks{busy: 50, total: 100, process: 200}).since(start, 8); got != "machine 50% busy, node 16.0 of 8 cores" {
		t.Errorf("since = %q", got)
	}
}
