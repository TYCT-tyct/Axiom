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
			items = append(items, fmt.Sprintf(`{"id":%d,"name":"k%d","key":"k","group_id":1,"status":"active"}`, i, i))
		}
		return adapter.Response{Status: 200, Body: []byte(fmt.Sprintf(`{"code":0,"data":{"items":[%s],"total":%d,"pages":%d}}`,
			strings.Join(items, ","), *total, (*total+99)/100))}
	}
}

func TestAKeyListingResumesAcrossManagers(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name               string
		total, blockedPage int
		shrink, reopen     bool
		want               []int
	}{
		{"resume", 250, 2, false, true, []int{1, 2, 2, 3}},
		{"shrink", 250, 2, true, true, []int{1, 2, 2, 1}},
		{"past a window", 9050, 90, false, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			asked := resumeKeys(t, c.total, c.blockedPage, c.shrink, c.reopen)
			if c.want != nil && !slices.Equal(asked, c.want) {
				t.Fatalf("pages %v, want %v", asked, c.want)
			}
		})
	}
}

func resumeKeys(t *testing.T, total, blockedPage int, shrink, reopen bool) []int {
	t.Helper()
	asked, limited := []int{}, true
	serve := keyList(&total)
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		page, err := keyPageOf(r.Path)
		if err != nil {
			return adapter.Response{}, err
		}
		asked = append(asked, page)
		if limited && page == blockedPage {
			return adapter.Response{}, adapter.Blocked{Until: epoch.Add(time.Minute)}
		}
		return serve(r), nil
	})
	openManager := func() adapter.Manager { return manager0(t, srv, login(t, epoch.Add(24*time.Hour))) }
	until := epoch.Add(time.Minute)
	m := openManager()
	first, err := m.Keys(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Done || len(first.Next) == 0 || !first.Until.Equal(until) {
		t.Fatalf("no resumable position: %+v", first)
	}
	limited = false
	if shrink {
		total = 50
	}
	if reopen {
		m = openManager()
	}
	page, err := m.Keys(t.Context(), first.Next)
	if err != nil {
		t.Fatal(err)
	}
	assertKeyListing(t, page, total)
	return asked
}

func assertKeyListing(t *testing.T, page adapter.Page[adapter.KeyState], total int) {
	t.Helper()
	if !page.Done || len(page.Next) != 0 || len(page.Items) != total {
		t.Fatalf("incomplete listing: done %v, %d keys, position %q", page.Done, len(page.Items), page.Next)
	}
	for i, k := range page.Items {
		if k.Ref != adapter.KeyRef(strconv.Itoa(i+1)) || k.Name != fmt.Sprintf("k%d", i+1) {
			t.Fatalf("key %d: %q named %q", i+1, k.Ref, k.Name)
		}
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

func TestLegacyKeyListingRestartsBeforeProjectingPolicies(t *testing.T) {
	t.Parallel()
	asked := []int{}
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		page, err := keyPageOf(r.Path)
		if err != nil {
			return adapter.Response{}, err
		}
		asked = append(asked, page)
		return fixture(t, "keys"), nil
	})
	legacy := adapter.Listing(`{"v":1,"page":2,"total":2,"keys":[{"Ref":"2","GroupRef":"2","Status":0,"ResetAt":"0001-01-01T00:00:00Z"}]}`)
	m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
	page, err := m.Keys(t.Context(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Done || len(page.Items) != 2 || !slices.Equal(asked, []int{1}) {
		t.Fatalf("legacy resume: pages %v, result %+v", asked, page)
	}
	for _, key := range page.Items {
		if key.Policy == nil || key.Policy.Models != nil {
			t.Fatalf("key %s lost its unrestricted policy", key.Ref)
		}
	}
}
