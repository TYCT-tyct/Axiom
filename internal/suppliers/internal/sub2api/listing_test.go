package sub2api

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// keyPageOf is the page a key list path asks for.
func keyPageOf(path string) (int, error) {
	q := strings.TrimPrefix(path, "/api/v1/keys?page=")
	return strconv.Atoi(strings.SplitN(q, "&", 2)[0])
}

// keyList answers the key list of a supplier whose total is *total: pages of a hundred, the last one short.
func keyList(total *int) func(adapter.Request) adapter.Response {
	return func(r adapter.Request) adapter.Response {
		page, err := keyPageOf(r.Path)
		if err != nil {
			panic(err) // the test's own request
		}
		var items []string
		for i := (page-1)*100 + 1; i <= min(page*100, *total); i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"key":"k","group_id":1,"status":"active"}`, i))
		}
		return adapter.Response{Status: 200, Body: []byte(fmt.Sprintf(`{"code":0,"data":{"items":[%s],"total":%d,"pages":%d}}`,
			strings.Join(items, ","), *total, (*total+99)/100))}
	}
}

// TestAKeyListingThatChangedUnderItsPositionBeginsAgain: a key removed during a listing moves its pages forward, so going
// on from the stored page would skip one. The position is given up and the listing is read again whole; the keys that are
// gone do not come back with it.
func TestAKeyListingThatChangedUnderItsPositionBeginsAgain(t *testing.T) {
	t.Parallel()
	total, asked := 250, []int{}
	serve := keyList(&total)
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		page, err := keyPageOf(r.Path)
		if err != nil {
			return adapter.Response{}, err
		}
		asked = append(asked, page)
		return serve(r), nil
	})
	m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
	first, err := m.Keys(t.Context(), nil)
	if err != nil || first.Done || len(first.Next) == 0 {
		t.Fatalf("the first read: done %v, position %q, %v; want a position to go on from", first.Done, first.Next, err)
	}

	total = 150 // a hundred keys went away
	page, err := m.Keys(t.Context(), first.Next)
	if err != nil || !page.Done || len(page.Items) != total {
		t.Fatalf("after the listing changed: done %v, %d keys, %v; want the %d it holds now", page.Done, len(page.Items), err, total)
	}
	if want := []int{1, 2, 1, 2}; !slices.Equal(asked, want) {
		t.Errorf("the pages read were %v, want %v: the listing began again", asked, want)
	}
	for i, k := range page.Items {
		if k.Ref != adapter.KeyRef(strconv.Itoa(i+1)) {
			t.Fatalf("key %d is %q, want the keys the listing holds now, none skipped", i+1, k.Ref)
		}
	}
}

// TestALimitedReadAnswersWithItsPosition: a read that the supplier's limits refuse answers with the page it reached and
// the moment to go on; the read made then reads that page, so no page is skipped and none is read twice.
func TestALimitedReadAnswersWithItsPosition(t *testing.T) {
	t.Parallel()
	total, asked := 250, []int{}
	limited := true
	serve := keyList(&total)
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		page, err := keyPageOf(r.Path)
		if err != nil {
			return adapter.Response{}, err
		}
		asked = append(asked, page)
		if limited && page == 2 {
			return adapter.Response{}, adapter.Blocked{Until: epoch.Add(time.Minute)}
		}
		return serve(r), nil
	})
	m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
	first, err := m.Keys(t.Context(), nil)
	if err != nil || first.Done || len(first.Next) == 0 || !first.Until.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("the read the limits stopped: done %v, position %q, until %v, %v; want a position and the moment",
			first.Done, first.Next, first.Until, err)
	}

	limited = false
	page, err := m.Keys(t.Context(), first.Next)
	if err != nil || !page.Done || len(page.Items) != total {
		t.Fatalf("after the limits reopened: done %v, %d keys, %v; want the %d keys it read on to", page.Done, len(page.Items), err, total)
	}
	if want := []int{1, 2, 2, 3}; !slices.Equal(asked, want) {
		t.Errorf("the pages asked for were %v, want %v: the page the limits stopped is the one read again", asked, want)
	}
}

// TestThePositionOfAListingHoldsNoKeyText: the position of a listing is stored in the supplier's sync state, which holds
// no credentials. The offer reads keys to list their groups' models; the position it answers with when the panel bucket
// stops it names them by id, never by their text.
func TestThePositionOfAListingHoldsNoKeyText(t *testing.T) {
	t.Parallel()
	table := panel(t)
	calls := 0
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		if calls++; calls > 4 { // the groups, the multipliers, one page of keys, and one key's own read
			return adapter.Response{}, adapter.Blocked{Until: epoch.Add(time.Minute)}
		}
		return table[r.Method+" "+r.Path], nil
	})
	m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
	page, err := m.Offer(t.Context(), nil)
	if err != nil || page.Done || len(page.Next) == 0 {
		t.Fatalf("the read the limits stopped: done %v, position %q, %v; want a position", page.Done, page.Next, err)
	}
	if strings.Contains(string(page.Next), "placeholder-key") {
		t.Errorf("the position of the offer holds a key's text: %s", page.Next)
	}
}

// TestAnOfferWhoseGroupsChangedBeginsAgain: a group added to the supplier after a pass began is not one the pass read, so
// the pass begins again over the groups it has now, its key page with them.
func TestAnOfferWhoseGroupsChangedBeginsAgain(t *testing.T) {
	t.Parallel()
	groups := 2
	calls := 0
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		if calls++; calls == 3 { // the groups and their multipliers are read; then the limits close
			return adapter.Response{}, adapter.Blocked{Until: epoch.Add(time.Minute)}
		}
		return manyGroups(groups)(r), nil
	})
	m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
	first, err := m.Offer(t.Context(), nil)
	if err != nil || first.Done || len(first.Next) == 0 {
		t.Fatalf("the read the limits stopped: done %v, position %q, %v; want a position", first.Done, first.Next, err)
	}

	groups = 3 // a group was added
	page, err := m.Offer(t.Context(), first.Next)
	if err != nil || !page.Done || len(page.Items) != groups {
		t.Fatalf("after the groups changed: done %v, %d groups, %v; want the %d it has now", page.Done, len(page.Items), err, groups)
	}
	listed := 0
	for _, p := range srv.paths() {
		if p == "GET /api/v1/groups/available" {
			listed++
		}
	}
	if listed != 2 {
		t.Errorf("the groups were listed %d times, want twice: the pass that found them changed began again", listed)
	}
}
