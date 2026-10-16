package newapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/client"
)

// TestAnAnswerWithoutItsMembersIsNotAnEmptyResult: a successful answer that lacks what the adapter needs, or holds null
// for it, is an answer in another form, not a supplier with no keys, no groups or no charges. It is an error, and the
// scan keeps its position.
func TestAnAnswerWithoutItsMembersIsNotAnEmptyResult(t *testing.T) {
	t.Parallel()
	hollow := []string{
		`{"success":true}`, `{"success":true,"data":null}`, `{"success":true,"data":{}}`,
		`{"success":true,"data":{"items":null,"total":null}}`,
	}
	reads := map[string]func(m adapter.Manager) error{
		"Keys":    func(m adapter.Manager) error { _, err := m.Keys(t.Context()); return err },
		"Offer":   func(m adapter.Manager) error { _, err := m.Offer(t.Context()); return err },
		"Account": func(m adapter.Manager) error { _, err := m.Account(t.Context()); return err },
		"Charges": func(m adapter.Manager) error {
			state := startState(t, t0)
			_, got, err := m.Charges(t.Context(), state)
			if !bytes.Equal(got, state) {
				t.Error("an unreadable page changed the scan position")
			}
			return err
		},
	}
	for name, read := range reads {
		for _, body := range hollow {
			f := hollowClient(body)
			err := read(open(t, f, t0.Add(2*time.Hour)))
			if !errors.Is(err, adapter.ErrUnrecognised) {
				t.Errorf("%s over %s: %v, want an unrecognised answer", name, body, err)
			}
		}
	}
}

// hollowClient answers every request with one body.
func hollowClient(body string) adapter.Client {
	return funcClient(func(adapter.Request) (adapter.Response, error) { return answer(200, body), nil })
}

type funcClient func(adapter.Request) (adapter.Response, error)

func (f funcClient) Do(_ context.Context, r adapter.Request) (adapter.Response, error) { return f(r) }

// limited counts a client's requests in the buckets of the adapter, on a clock the test moves.
func limited(clk *testClock, inner funcClient) (adapter.Client, *int) {
	limiter := client.NewLimiter(globalSpecs(), clk.now)
	served := new(int)
	return funcClient(func(r adapter.Request) (adapter.Response, error) {
		if err := limiter.Take(r.Buckets); err != nil {
			return adapter.Response{}, err
		}
		*served++
		return inner(r)
	}), served
}

// retry calls read on the same manager until it succeeds, moving the clock to when the bucket reopens each time it is
// refused.
func retry(t *testing.T, clk *testClock, read func() error) (waits int) {
	t.Helper()
	for range 50 {
		err := read()
		if b, ok := errors.AsType[adapter.Blocked](err); ok {
			waits++
			clk.set(b.Until)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		return waits
	}
	t.Fatal("never finished")
	return waits
}

// TestAListingLongerThanARateWindowFinishes: 9,050 keys are 91 pages against a bucket of 90 requests per window. The
// listing stops when the bucket is empty and goes on from the same page on the same manager, so each page is read once
// and the keys come out whole; the offer of 120 groups, which takes a request per group, does the same.
func TestAListingLongerThanARateWindowFinishes(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0}
	keys, served := limited(clk, func(r adapter.Request) (adapter.Response, error) {
		u := mustParse(r.Path)
		page, err := strconv.Atoi(u.Query().Get("p"))
		if err != nil {
			return adapter.Response{}, err
		}
		from, to := min((page-1)*100, 9050), min(page*100, 9050)
		items := ""
		for i := from; i < to; i++ {
			items += fmt.Sprintf(`{"id":%d,"status":1,"group":"g"},`, i+1)
		}
		return answer(200, `{"success":true,"data":{"items":[`+trimComma(items)+`]}}`), nil
	})
	m := open(t, keys, t0)
	var got int
	waits := retry(t, clk, func() error {
		k, err := m.Keys(t.Context())
		got = len(k)
		return err
	})
	if got != 9050 || *served != 91 || waits == 0 {
		t.Errorf("%d keys from %d requests with %d waits, want 9050 from 91 with a wait", got, *served, waits)
	}

	clk = &testClock{t: t0}
	offer, served2 := limited(clk, func(r adapter.Request) (adapter.Response, error) {
		switch mustParse(r.Path).Path {
		case "/api/user/self/groups":
			groups := ""
			for i := range 120 {
				groups += fmt.Sprintf(`"g%03d":{"ratio":1,"desc":""},`, i)
			}
			return answer(200, `{"success":true,"data":{`+trimComma(groups)+`}}`), nil
		case "/api/pricing":
			return answer(200, `{"success":true,"data":[]}`), nil
		}
		return answer(200, `{"success":true,"data":["m"]}`), nil
	})
	om := open(t, offer, t0)
	var groups int
	retry(t, clk, func() error {
		o, err := om.Offer(t.Context())
		groups = len(o.Groups)
		return err
	})
	if groups != 120 || *served2 != 122 {
		t.Errorf("%d groups from %d requests, want 120 from 122", groups, *served2)
	}
}

// mustParse reads a path the test itself built.
func mustParse(path string) *url.URL {
	u, err := url.Parse(path)
	if err != nil {
		panic(err)
	}
	return u
}

func trimComma(s string) string {
	if s == "" {
		return s
	}
	return s[:len(s)-1]
}

// TestALateStretchIsReadHoweverLateThePassRuns: the daily pass starts at where the one before ended, if that is
// earlier than a day ago, so a pass run three days after the last leaves no time unread; a record that appeared late in
// the stretch between them is found.
func TestALateStretchIsReadHoweverLateThePassRuns(t *testing.T) {
	t.Parallel()
	d1 := t0.Add(25 * time.Hour)
	clk := &testClock{t: d1}
	srv := newLogServer(clk, globalSpecs())
	run := newScan(t, srv, startState(t, t0))
	run.run(200) // the first daily pass and the forward passes up to a day after the start
	clk.set(d1.Add(150 * time.Minute))
	run.run(200)
	srv.add(d1.Add(2*time.Hour), "late-x") // written half an hour late: after the forward pass that would have read it
	clk.set(d1.Add(72 * time.Hour))        // and the next daily pass is two days late
	run.run(400)
	if run.seen["late-x"] == 0 {
		t.Error("a record written late in a stretch the daily pass came late for was never read")
	}
}
