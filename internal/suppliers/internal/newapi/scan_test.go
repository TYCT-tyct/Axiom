package newapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/client"
)

// testClock is the time of a scan test, which the tests move.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// record is one row of the supplier's consumption log; seq is its insertion order, which is the id new-api lists by.
type record struct {
	ref     string
	created int64
	seq     int
	cache   int64
}

// logServer behaves as new-api's /api/log/self does, from the recorded shape of its answers: newest insertion first,
// filtered by the second timestamps, paged by offset, and counted against rate buckets by the same limiter the
// program uses. hook runs before the n-th request is answered, to let records arrive during a scan.
type logServer struct {
	mu       sync.Mutex
	clk      *testClock
	limiter  *client.Limiter
	recs     []record
	requests []time.Time
	hook     func(n int, s *logServer)
}

func newLogServer(clk *testClock, specs []adapter.BucketSpec) *logServer {
	return &logServer{clk: clk, limiter: client.NewLimiter(specs, clk.now)}
}

func (s *logServer) add(created time.Time, refs ...string) {
	for _, ref := range refs {
		s.recs = append(s.recs, record{ref: ref, created: created.Unix(), seq: len(s.recs) + 1})
	}
}

// fill adds n records with the request ids r0… spread evenly over (from, to].
func (s *logServer) fill(n int, from, to time.Time) {
	span := to.Sub(from)
	for i := range n {
		s.add(from.Add(span*time.Duration(i+1)/time.Duration(n)), "r"+strconv.Itoa(i))
	}
}

func (s *logServer) Do(_ context.Context, r adapter.Request) (adapter.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.limiter.Take(r.Buckets); err != nil {
		return adapter.Response{}, err
	}
	s.requests = append(s.requests, s.clk.now())
	if s.hook != nil {
		s.hook(len(s.requests), s)
	}
	u, err := url.Parse(r.Path)
	if err != nil || u.Path != "/api/log/self" || u.Query().Get("type") != "2" || u.Query().Get("page_size") != "100" {
		return adapter.Response{}, fmt.Errorf("log server: unexpected request %s", r.Path)
	}
	q := u.Query()
	page, err1 := strconv.Atoi(q.Get("p"))
	start, err2 := strconv.ParseInt(q.Get("start_timestamp"), 10, 64)
	end, err3 := strconv.ParseInt(q.Get("end_timestamp"), 10, 64)
	if err := errors.Join(err1, err2, err3); err != nil {
		return adapter.Response{}, err
	}
	var inRange []record
	for _, rec := range s.recs {
		if rec.created >= start && rec.created <= end {
			inRange = append(inRange, rec)
		}
	}
	slices.SortFunc(inRange, func(a, b record) int { return b.seq - a.seq })
	from := min((page-1)*100, len(inRange))
	to := min(page*100, len(inRange))
	items := make([]map[string]any, 0, to-from)
	for _, rec := range inRange[from:to] {
		items = append(items, map[string]any{
			"type": 2, "created_at": rec.created, "model_name": "gpt-4o-mini", "quota": 23, "prompt_tokens": 100,
			"completion_tokens": 50, "token_id": 1, "request_id": rec.ref,
			"other": fmt.Sprintf(`{"billing_source":"wallet","cache_tokens":%d}`, rec.cache),
		})
	}
	body, err := jsonx.MarshalCanonical(map[string]any{"success": true, "data": map[string]any{"page": page, "total": len(inRange), "items": items}})
	return adapter.Response{Status: 200, Header: answer(200, "").Header, Body: body}, err
}

// scanOf decodes a state the adapter returned.
func scanOf(t *testing.T, raw adapter.ScanState) scanState {
	t.Helper()
	s, err := decodeScan(raw, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func startState(t *testing.T, watermark time.Time) adapter.ScanState {
	t.Helper()
	raw, err := scanState{V: 1, Watermark: watermark.Unix(), Span: int64(time.Hour / time.Second), LastLate: watermark.Unix()}.encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// scan is a run of Charges calls as the module's sync makes them: after every call the returned state is what is kept,
// a manager is made again for the next call (as after a restart), a call that finds a bucket empty waits until it
// reopens, and records are counted by request id.
type scan struct {
	t       *testing.T
	srv     *logServer
	clk     *testClock
	state   adapter.ScanState
	seen    map[string]int
	spans   []int64
	blocked int
}

func newScan(t *testing.T, srv *logServer, state adapter.ScanState) *scan {
	return &scan{t: t, srv: srv, clk: srv.clk, state: state, seen: map[string]int{}}
}

func (s *scan) manager() adapter.Manager {
	m, err := (&Adapter{now: s.clk.now}).Open(s.t.Context(), s.srv, testLogin(s.t, time.Time{}))
	if err != nil {
		s.t.Fatal(err)
	}
	return m
}

// step makes one call and reports whether the scan has nothing more to read until time passes.
func (s *scan) step() (idle bool) {
	s.t.Helper()
	before := s.state
	page, next, err := s.manager().Charges(s.t.Context(), s.state)
	if b, ok := errors.AsType[adapter.Blocked](err); ok {
		if !bytes.Equal(next, before) {
			s.t.Fatal("a call that found its bucket empty changed the state")
		}
		s.blocked++
		s.clk.set(b.Until)
		return false
	}
	if err != nil {
		s.t.Fatal(err)
	}
	if len(before) > 0 && scanOf(s.t, next).Watermark < scanOf(s.t, before).Watermark {
		s.t.Fatal("the watermark went back")
	}
	s.state = bytes.Clone(next)
	for _, c := range page.Charges {
		s.seen[string(c.Ref)]++
	}
	st := scanOf(s.t, next)
	if st.Pass == nil && page.Done {
		s.spans = append(s.spans, st.Span)
	}
	return st.Pass == nil && nextPass(st, s.clk.now()) == nil
}

func (s *scan) run(limit int) {
	s.t.Helper()
	for range limit {
		if s.step() {
			return
		}
	}
	s.t.Fatalf("the scan did not finish in %d calls", limit)
}

func (s *scan) wantSeen(n int) {
	s.t.Helper()
	for i := range n {
		if s.seen["r"+strconv.Itoa(i)] == 0 {
			s.t.Fatalf("record r%d was never read", i)
		}
	}
}

var t0 = time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)

func globalSpecs() []adapter.BucketSpec { return New().Buckets() }

// TestAScanLargerThanARateWindowReadsEveryRecord: 12,000 records take 120 pages and more than the 90 requests the
// global bucket allows in 180 seconds, so the scan has to stop and wait for the bucket repeatedly; every record is
// read, no 180-second stretch holds more than two windows' worth of requests, and the long first pass makes the
// next ones shorter.
func TestAScanLargerThanARateWindowReadsEveryRecord(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0.Add(2*time.Hour + 2*time.Minute)}
	srv := newLogServer(clk, globalSpecs())
	srv.fill(12000, t0, t0.Add(2*time.Hour))
	run := newScan(t, srv, startState(t, t0))
	run.run(3000)
	run.wantSeen(12000)
	if run.blocked == 0 {
		t.Error("the scan never met a closed bucket, so it did not test waiting")
	}
	for i, at := range srv.requests {
		inWindow := 0
		for _, other := range srv.requests[i:] {
			if other.Before(at.Add(180 * time.Second)) {
				inWindow++
			}
		}
		if inWindow > 180 {
			t.Fatalf("%d requests within 180 seconds from request %d, more than two windows of 90", inWindow, i)
		}
	}
	if run.spans[0] != int64(30*time.Minute/time.Second) {
		t.Errorf("after a pass of 60 pages the next pass covers %d seconds, want half an hour", run.spans[0])
	}
	if w := scanOf(t, run.state).Watermark; w < clk.now().Add(-lag-time.Minute).Unix() {
		t.Errorf("the watermark stops %d seconds short of now", clk.now().Unix()-w)
	}
}

// TestARateLimitInTheMiddleOfAPageLosesNothing: with only three requests allowed per window, a scan of ten pages stops
// and resumes at the same page many times; each stop leaves the state as it was.
func TestARateLimitInTheMiddleOfAPageLosesNothing(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0.Add(20 * time.Minute)}
	srv := newLogServer(clk, []adapter.BucketSpec{{Name: "global", Window: 180 * time.Second, Limit: 3}, {Name: "critical", Window: time.Hour, Limit: 1}})
	srv.fill(950, t0, t0.Add(15*time.Minute))
	run := newScan(t, srv, startState(t, t0))
	run.run(200)
	run.wantSeen(950)
	if run.blocked < 3 {
		t.Errorf("only %d stops for ten pages at three per window", run.blocked)
	}
	for _, n := range srv.requests {
		count := 0
		for _, other := range srv.requests {
			if !other.Before(n) && other.Before(n.Add(180*time.Second)) {
				count++
			}
		}
		if count > 3 {
			t.Fatalf("%d requests in 180 seconds from %v, the bucket allows 3", count, n)
		}
	}
}

// TestRecordsThatArriveLateAreFoundByTheOverlapAndTheDailyPass: a record written while a pass runs, whose time lies in
// the pass, shifts later pages and so can only be read twice, never skipped; one inside the last five minutes of the
// pass is read by the next pass, and one older than that only by the daily pass over the last day.
func TestRecordsThatArriveLateAreFoundByTheOverlapAndTheDailyPass(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0.Add(31 * time.Minute)}
	srv := newLogServer(clk, globalSpecs())
	srv.fill(450, t0, t0.Add(30*time.Minute))
	srv.hook = func(n int, s *logServer) {
		if n == 2 { // after the first page is read, three pages remain
			end := t0.Add(30*time.Minute + 30*time.Second)
			s.add(end.Add(-2*time.Minute), "recent")
			s.add(t0.Add(5*time.Minute), "old")
		}
	}
	run := newScan(t, srv, startState(t, t0))
	run.run(50)
	run.wantSeen(450)
	clk.set(t0.Add(40 * time.Minute))
	run.run(50)
	if run.seen["recent"] != 1 || run.seen["old"] != 0 {
		t.Fatalf("after the first pass: recent read %d times, old %d; want 1 and 0", run.seen["recent"], run.seen["old"])
	}
	dupes := 0
	for i := range 450 {
		dupes += run.seen["r"+strconv.Itoa(i)] - 1
	}
	if dupes == 0 {
		t.Error("the late arrival shifted no page, so the test did not cover a duplicate")
	}
	clk.set(t0.Add(24*time.Hour + 5*time.Minute))
	run.run(100)
	if run.seen["old"] == 0 {
		t.Error("the record older than the overlap was never read, even by the daily pass")
	}
}

// TestPassEdges: a pass whose records fill a whole number of pages ends at the empty page after them, a scan with
// less than a minute of new time makes no request, and a position that is not ours, or an empty one, never reads old
// records or moves the watermark back.
func TestPassEdges(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0.Add(10 * time.Minute)}
	srv := newLogServer(clk, globalSpecs())
	srv.fill(200, t0, t0.Add(5*time.Minute))
	run := newScan(t, srv, startState(t, t0))
	run.run(10)
	run.wantSeen(200)
	if len(srv.requests) != 3 {
		t.Errorf("%d requests for exactly two full pages, want 3 (the empty page ends the pass)", len(srv.requests))
	}
	// Less than a minute beyond the watermark: nothing to ask.
	before := len(srv.requests)
	clk.set(time.Unix(scanOf(t, run.state).Watermark, 0).Add(lag + 59*time.Second))
	page, next, err := run.manager().Charges(t.Context(), run.state)
	if err != nil || !page.Done || len(page.Charges) != 0 || len(srv.requests) != before || !bytes.Equal(next, run.state) {
		t.Errorf("an idle call: %+v, %v, %d new requests", page, err, len(srv.requests)-before)
	}
	// A position this version cannot read is an error that keeps the state.
	for _, garbage := range []string{`{`, `{"v":2,"span":5}`, `{"v":1,"span":0}`, `{"v":1,"span":60,"extra":1}`} {
		if _, got, err := run.manager().Charges(t.Context(), []byte(garbage)); !errors.Is(err, adapter.ErrUnrecognised) || string(got) != garbage {
			t.Errorf("state %q: %v, returned %q", garbage, err, got)
		}
	}
	// No state at all is the first scan: it starts at now, so old records stay unread.
	fresh := newScan(t, srv, nil)
	fresh.run(5)
	if len(fresh.seen) != 0 {
		t.Errorf("a first scan read %d old records", len(fresh.seen))
	}
	if w := scanOf(t, fresh.state).Watermark; w != clk.now().Unix() {
		t.Errorf("a first scan starts at %d, want now", w)
	}
}

// TestARereadOfAPageGivesTheSameCharges: calling again with a state already used (a node that died after reading but
// before keeping the page) gives the page again, so a restart repeats work and loses none.
func TestARereadOfAPageGivesTheSameCharges(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0.Add(10 * time.Minute)}
	srv := newLogServer(clk, globalSpecs())
	srv.fill(250, t0, t0.Add(5*time.Minute))
	state := startState(t, t0)
	first, next, err := (&Adapter{now: clk.now}).managerOf(t, srv).Charges(t.Context(), state)
	if err != nil {
		t.Fatal(err)
	}
	again, next2, err := (&Adapter{now: clk.now}).managerOf(t, srv).Charges(t.Context(), state)
	if err != nil || len(first.Charges) != 100 || len(again.Charges) != 100 || !bytes.Equal(next, next2) {
		t.Fatalf("rereading gave %d and %d charges", len(first.Charges), len(again.Charges))
	}
	for i := range first.Charges {
		if first.Charges[i].Ref != again.Charges[i].Ref {
			t.Fatal("the reread page differs")
		}
	}
}

func (a *Adapter) managerOf(t *testing.T, c adapter.Client) adapter.Manager {
	t.Helper()
	m, err := a.Open(t.Context(), c, testLogin(t, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestTheWindowShrinksAfterALongPassAndGrowsAfterShortOnes: more than 20 pages in a pass halves the next, three short
// ones in a row double it, and it stays between a minute and a day.
func TestTheWindowShrinksAfterALongPassAndGrowsAfterShortOnes(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0.Add(20 * time.Hour)} // under a day, so no daily pass comes first
	srv := newLogServer(clk, globalSpecs())
	srv.fill(2500, t0, t0.Add(time.Hour)) // 25 pages in the first pass
	run := newScan(t, srv, startState(t, t0))
	run.run(500)
	want := []int64{1800, 1800, 1800, 3600}
	if len(run.spans) < len(want) || fmt.Sprint(run.spans[:len(want)]) != fmt.Sprint(want) {
		t.Fatalf("spans after each pass: %v, want it to start %v", run.spans, want)
	}
	for _, span := range run.spans {
		if span < 60 || span > 24*3600 {
			t.Errorf("span %d outside a minute to a day", span)
		}
	}
	// A backlog of many passes shrinks to the minimum and no further.
	if got := finish(scanState{V: 1, Span: 60, Pass: &pass{Pages: 99}}, t0).Span; got != 60 {
		t.Errorf("a long pass at the minimum span gives %d", got)
	}
	if got := finish(scanState{V: 1, Span: 24 * 3600, Fast: 2, Pass: &pass{Pages: 1}}, t0).Span; got != 24*3600 {
		t.Errorf("a short pass at the maximum span gives %d", got)
	}
}

// TestChargesCarryWhatTheRecordSays: the recorded log entries become charges with the key, the amount, the token
// counts and the funding; usage with cached tokens is not offered as correcting an estimate; a record that cannot be
// read stops the pass.
func TestChargesCarryWhatTheRecordSays(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"32-log-by-request-id", "35-log-stream"} {
		var page logPage
		if err := jsonx.Unmarshal(sample(t, name).Body, &page); err != nil {
			t.Fatal(err)
		}
		charges, err := convert(page)
		if err != nil || len(charges) != 1 {
			t.Fatalf("%s: %v", name, err)
		}
		checkRecordedCharge(t, name, charges[0])
	}
}

func checkRecordedCharge(t *testing.T, name string, c adapter.Charge) {
	t.Helper()
	if c.Key != "1" || c.Amount != "23" || c.Funding != adapter.FundingWallet || c.Model != "gpt-4o-mini" || c.Usage.OutputTotal() != 50 {
		t.Errorf("%s: charge %+v", name, c)
	}
	if !c.Reported.Has(catalog.ItemInput) || c.Reported.Has(catalog.ItemOutput) || c.Reported.Has(catalog.ItemCacheRead) {
		t.Errorf("%s: reported items %v, want the input only: the output holds the reasoning", name, c.Reported)
	}
	if name == "32-log-by-request-id" && c.Ref != "202610140453479800647938268d9d62FDITAAx" {
		t.Errorf("ref %q is not the record's request id", c.Ref)
	}
}

// recordedPage is the recorded log answer, read.
func recordedPage(t *testing.T) logPage {
	t.Helper()
	var page logPage
	if err := jsonx.Unmarshal(sample(t, "32-log-by-request-id").Body, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func clonePage(p logPage) logPage {
	data := *p.Data
	data.Items = slices.Clone(data.Items)
	p.Data = &data
	return p
}

// TestUsageWithCachedTokensIsNotOfferedAsCorrection and the other readings of the details: cached tokens leave the
// amount and no reported items, a subscription is not the wallet, and a record that is not a consumption is skipped.
func TestUsageWithCachedTokensIsNotOfferedAsCorrection(t *testing.T) {
	t.Parallel()
	cached := clonePage(recordedPage(t))
	cached.Data.Items[0].Other = new(`{"billing_source":"wallet","cache_tokens":40}`)
	if charges, err := convert(cached); err != nil || charges[0].Reported != 0 || charges[0].Amount != "23" {
		t.Errorf("a record with cached tokens: %+v, %v, want the amount and no reported items", charges, err)
	}
	subscription := clonePage(recordedPage(t))
	subscription.Data.Items[0].Other = new(`{"billing_source":"subscription"}`)
	if charges, err := convert(subscription); err != nil || charges[0].Funding != adapter.FundingOther {
		t.Error("a charge drawn on a subscription is taken for a wallet charge")
	}
	consumed := clonePage(recordedPage(t))
	consumed.Data.Items[0].Type = new(1)
	if charges, err := convert(consumed); err != nil || len(charges) != 0 {
		t.Errorf("a record that is not a consumption: %+v, %v", charges, err)
	}
}

// TestARecordThatCannotBeReadStopsThePass: a charge left out would be money nothing matches, so the pass fails.
func TestARecordThatCannotBeReadStopsThePass(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*logPage){
		"no request id":      func(p *logPage) { p.Data.Items[0].RequestID = "" },
		"no key":             func(p *logPage) { p.Data.Items[0].TokenID = new(int64(0)) },
		"a fractional quota": func(p *logPage) { p.Data.Items[0].Quota = "22.5" },
		"garbled details":    func(p *logPage) { p.Data.Items[0].Other = new(`{`) },
		"negative tokens":    func(p *logPage) { p.Data.Items[0].Prompt = new(int64(-1)) },
		"no details":         func(p *logPage) { p.Data.Items[0].Other = nil },
		"no token counts":    func(p *logPage) { p.Data.Items[0].Output = nil },
		"no time":            func(p *logPage) { p.Data.Items[0].CreatedAt = nil },
		"no type":            func(p *logPage) { p.Data.Items[0].Type = nil },
	} {
		broken := clonePage(recordedPage(t))
		mutate(&broken)
		if _, err := convert(broken); !errors.Is(err, adapter.ErrUnrecognised) {
			t.Errorf("%s: %v, want the pass to stop", name, err)
		}
	}
}
