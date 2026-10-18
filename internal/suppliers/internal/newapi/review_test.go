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
		"Keys":    func(m adapter.Manager) error { _, err := readKeys(t, m); return err },
		"Offer":   func(m adapter.Manager) error { _, err := readOffer(t, m); return err },
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
		if err := limiter.Take(context.Background(), r.Buckets); err != nil {
			return adapter.Response{}, err
		}
		*served++
		return inner(r)
	}), served
}

// readPaged reads a listing over as many reads as its bucket allows: a read the bucket stopped is given again after the
// window reopens, from the position it answered with. It returns the read that ended the listing, and how often it had
// to wait.
func readPaged[T any](t *testing.T, clk *testClock, read func(from adapter.Listing) (adapter.Page[T], error)) (adapter.Page[T], int) {
	t.Helper()
	var from adapter.Listing
	var page adapter.Page[T]
	waits := 0
	for range 500 {
		var err error
		if page, err = read(from); err != nil {
			t.Fatal(err)
		}
		from = page.Next
		if page.Done {
			return page, waits
		}
		if !page.Until.IsZero() {
			waits++
			clk.set(page.Until)
		}
	}
	t.Fatal("the listing did not end")
	return page, waits
}

// TestAListingLongerThanARateWindowFinishes: 9,050 keys are 91 pages against a bucket of 90 requests per window. A read
// the bucket stops answers with the page it reached, and the read after the window goes on from there: each page is read
// once and the keys come out whole. The offer of 120 groups, which takes a request per group, does the same.
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
		return answer(200, `{"success":true,"data":{"total":9050,"items":[`+trimComma(items)+`]}}`), nil
	})
	m := open(t, keys, t0)
	page, waits := readPaged(t, clk, func(from adapter.Listing) (adapter.Page[adapter.KeyState], error) {
		return m.Keys(t.Context(), from)
	})
	if len(page.Items) != 9050 || *served != 91 || waits == 0 {
		t.Errorf("%d keys from %d requests with %d waits, want 9050 from 91 with a wait", len(page.Items), *served, waits)
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
	groups, _ := readPaged(t, clk, func(from adapter.Listing) (adapter.Page[adapter.Group], error) {
		return om.Offer(t.Context(), from)
	})
	// 120 model reads, the groups and the prices, and one read of the groups per resume: the position names the groups
	// it was read over, so a pass that goes on checks they are still the supplier's before it goes on reading them.
	if len(groups.Items) != 120 || *served2 != 123 {
		t.Errorf("%d groups from %d requests, want 120 from 123", len(groups.Items), *served2)
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

// TestARecordWrittenLateIntoAStretchAlreadyReadIsFoundADayLater: the daily pass reads from a day before where the last
// one ended, so a record that belongs to a stretch the last pass covered and was written after it, up to a day late, is
// found by the next; the overlap of five minutes the forward passes have is not enough for it.
func TestARecordWrittenLateIntoAStretchAlreadyReadIsFoundADayLater(t *testing.T) {
	t.Parallel()
	d1 := t0.Add(25 * time.Hour)
	clk := &testClock{t: d1}
	srv := newLogServer(clk, globalSpecs())
	run := newScan(t, srv, startState(t, t0))
	run.run(200) // the daily pass of the first day, and the forward passes up to it
	clk.set(d1.Add(2 * time.Hour))
	run.run(200)
	srv.add(d1.Add(-3*time.Hour), "late-y") // belongs to the stretch the daily pass covered, written 80 minutes after it
	clk.set(d1.Add(26 * time.Hour))
	run.run(400)
	if run.seen["late-y"] == 0 {
		t.Error("a record written an hour after the daily pass that belongs to its stretch was never read")
	}
}

// groupsOf answers the offer's reads with the groups given, an empty price list and one model for each group.
func groupsOf(groups string) adapter.Client {
	return funcClient(func(r adapter.Request) (adapter.Response, error) {
		switch mustParse(r.Path).Path {
		case "/api/user/self/groups":
			return answer(200, `{"success":true,"data":`+groups+`}`), nil
		case "/api/pricing":
			return answer(200, `{"success":true,"data":[]}`), nil
		}
		return answer(200, `{"success":true,"data":["m"]}`), nil
	})
}

// TestOnlyTheAutoRatioLeavesAGroupOut: a group whose ratio is "auto" has none of its own and is not offered; one without
// a ratio, with null, or with anything else is a response in another form, not a group to leave out.
func TestOnlyTheAutoRatioLeavesAGroupOut(t *testing.T) {
	t.Parallel()
	for name, groups := range map[string]string{
		"a missing ratio":            `{"g":{"desc":"x"}}`,
		"an empty group":             `{"g":{}}`,
		"a null ratio":               `{"g":{"ratio":null}}`,
		"a word":                     `{"g":{"ratio":"cheap"}}`,
		"an empty string":            `{"g":{"ratio":""}}`,
		"a good group and a bad one": `{"a":{"ratio":1},"b":{}}`,
	} {
		if _, err := readOffer(t, open(t, groupsOf(groups), t0)); !errors.Is(err, adapter.ErrUnrecognised) {
			t.Errorf("%s: %v, want an unrecognised answer", name, err)
		}
	}
	offer, err := readOffer(t, open(t, groupsOf(`{"a":{"ratio":1.5},"auto":{"ratio":"auto"}}`), t0))
	if err != nil || len(offer) != 1 || offer[0].Ref != "a" {
		t.Errorf("a group and the auto group: %+v, %v, want only the group", offer, err)
	}
}

// TestTheFirstDailyPassDoesNotReadBeforeTheConnection: a scan that begins at t0 reads nothing from before t0: not the
// forward passes, which start a little before the watermark, nor the daily pass a day later, which looks a day back
// from where the last one ended; records after t0 are read.
func TestTheFirstDailyPassDoesNotReadBeforeTheConnection(t *testing.T) {
	t.Parallel()
	clk := &testClock{t: t0}
	srv := newLogServer(clk, globalSpecs())
	srv.add(t0.Add(-time.Hour), "before")
	srv.add(t0.Add(-time.Minute), "just before") // inside the forward passes' overlap
	srv.add(t0.Add(10*time.Minute), "after")
	run := newScan(t, srv, nil) // the first scan: it begins now
	run.run(10)
	clk.set(t0.Add(25 * time.Hour)) // the first daily pass is due
	run.run(200)
	if run.seen["before"] != 0 || run.seen["just before"] != 0 || run.seen["after"] == 0 {
		t.Errorf("read the records from before the connection %d and %d times and the one after %d times, want 0, 0 and at least 1",
			run.seen["before"], run.seen["just before"], run.seen["after"])
	}
}
