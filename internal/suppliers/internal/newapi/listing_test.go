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

func TestAKeyListingResumesAcrossManagers(t *testing.T) {
	t.Parallel()
	for _, shrink := range []bool{false, true} {
		t.Run(fmt.Sprint(shrink), func(t *testing.T) {
			t.Parallel()
			resumeKeys(t, shrink)
		})
	}
}

func resumeKeys(t *testing.T, shrink bool) {
	t.Helper()
	total, asked, limited := 250, []int{}, true
	serve := keyList(&total)
	client := funcClient(func(r adapter.Request) (adapter.Response, error) {
		page := pageOf(t, r)
		asked = append(asked, page)
		if limited && page == 2 {
			return adapter.Response{}, adapter.Blocked{Until: t0.Add(time.Minute)}
		}
		return serve(r)
	})
	openManager := func() adapter.Manager { return open(t, client, t0) }
	until := t0.Add(time.Minute)
	first, err := openManager().Keys(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Done || len(first.Next) == 0 || !first.Until.Equal(until) {
		t.Fatalf("no resumable position: %+v", first)
	}
	limited = false
	want := []int{1, 2, 2, 3}
	if shrink {
		total = 50
		want = []int{1, 2, 2, 1}
	}
	page, err := openManager().Keys(t.Context(), first.Next)
	if err != nil {
		t.Fatal(err)
	}
	assertKeyListing(t, page, total)
	if !slices.Equal(asked, want) {
		t.Fatalf("pages %v, want %v", asked, want)
	}
}

func assertKeyListing(t *testing.T, page adapter.Page[adapter.KeyState], total int) {
	t.Helper()
	if !page.Done || len(page.Next) != 0 || len(page.Items) != total {
		t.Fatalf("incomplete listing: done %v, %d keys, position %q", page.Done, len(page.Items), page.Next)
	}
	for i, k := range page.Items {
		if k.Ref != adapter.KeyRef(strconv.Itoa(i+1)) {
			t.Fatalf("key %d: %q", i+1, k.Ref)
		}
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

func TestLegacyKeyListingRestartsBeforeProjectingPolicies(t *testing.T) {
	t.Parallel()
	asked := []int{}
	total := 2
	client := funcClient(func(r adapter.Request) (adapter.Response, error) {
		asked = append(asked, pageOf(t, r))
		return sample(t, "57-list-keys2"), nil
	})
	legacy := adapter.Listing(`{"v":1,"page":2,"total":2,"keys":[{"Ref":"1","GroupRef":"","Status":0,"ResetAt":"0001-01-01T00:00:00Z"}]}`)
	page, err := open(t, client, t0).Keys(t.Context(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(asked, []int{1}) {
		t.Fatalf("resumed legacy keys without policies: pages %v", asked)
	}
	if !page.Done || len(page.Items) != total {
		t.Fatalf("incomplete fresh listing: %+v", page)
	}
	for _, key := range page.Items {
		if key.Policy == nil || key.Policy.Models != nil {
			t.Fatalf("key %s kept an unread legacy policy", key.Ref)
		}
	}
}
