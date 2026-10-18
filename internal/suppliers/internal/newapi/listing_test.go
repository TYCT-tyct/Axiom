package newapi

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// keyList answers the key list of a supplier whose total is *total: pages of a hundred, the last one short.
func keyList(total *int) func(adapter.Request) (adapter.Response, error) {
	return func(r adapter.Request) (adapter.Response, error) {
		page, err := strconv.Atoi(mustParse(r.Path).Query().Get("p"))
		if err != nil {
			return adapter.Response{}, err
		}
		var items []string
		for i := (page-1)*100 + 1; i <= min(page*100, *total); i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"status":1}`, i))
		}
		return answer(200, fmt.Sprintf(`{"success":true,"data":{"total":%d,"items":[%s]}}`, *total, strings.Join(items, ","))), nil
	}
}

// TestAKeyListingThatChangedUnderItsPositionBeginsAgain: a key removed while a listing was stopped moves its pages
// forward, so going on from the stored page would skip one. The position is given up and the listing is read again whole;
// the keys that are gone do not come back with it.
func TestAKeyListingThatChangedUnderItsPositionBeginsAgain(t *testing.T) {
	t.Parallel()
	total, asked := 250, []int{}
	limited := true
	serve := keyList(&total)
	client := funcClient(func(r adapter.Request) (adapter.Response, error) {
		page := pageOf(t, r)
		asked = append(asked, page)
		if limited && page == 2 {
			return adapter.Response{}, adapter.Blocked{Until: t0.Add(time.Minute)}
		}
		return serve(r)
	})
	m := open(t, client, t0)
	first, err := m.Keys(t.Context(), nil)
	if err != nil || first.Done || len(first.Next) == 0 {
		t.Fatalf("the read the limits stopped: done %v, position %q, %v; want a position to go on from", first.Done, first.Next, err)
	}

	limited, total = false, 150 // a hundred keys went away
	page, err := m.Keys(t.Context(), first.Next)
	if err != nil || !page.Done || len(page.Items) != total {
		t.Fatalf("after the listing changed: done %v, %d keys, %v; want the %d it holds now", page.Done, len(page.Items), err, total)
	}
	if want := []int{1, 2, 2, 1, 2}; !slices.Equal(asked, want) {
		t.Errorf("the pages asked for were %v, want %v: the listing began again", asked, want)
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
	client := funcClient(func(r adapter.Request) (adapter.Response, error) {
		page := pageOf(t, r)
		asked = append(asked, page)
		if limited && page == 2 {
			return adapter.Response{}, adapter.Blocked{Until: t0.Add(time.Minute)}
		}
		return serve(r)
	})
	m := open(t, client, t0)
	first, err := m.Keys(t.Context(), nil)
	if err != nil || first.Done || len(first.Next) == 0 || !first.Until.Equal(t0.Add(time.Minute)) {
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

// pageOf is the page a key list request asks for.
func pageOf(t *testing.T, r adapter.Request) int {
	t.Helper()
	page, err := strconv.Atoi(mustParse(r.Path).Query().Get("p"))
	if err != nil {
		t.Fatal(err)
	}
	return page
}
