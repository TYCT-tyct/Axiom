package sub2api

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// TestPoolReadsGroupsWindowsAndFailures: the directory is read page by page, failed and unschedulable accounts are
// counted rather than guessed from a usage error, and the window sample is asked for in one batch.
func TestPoolReadsGroupsWindowsAndFailures(t *testing.T) {
	t.Parallel()
	srv := routes(map[string]adapter.Response{
		"GET /api/v1/auth/me": fixture(t, "me"),
		"GET /api/v1/admin/accounts?page=1&page_size=100&lite=1": fixture(t, "admin_accounts_p1"),
		"GET /api/v1/admin/accounts?page=2&page_size=100&lite=1": fixture(t, "admin_accounts_p2"),
		"POST /api/v1/admin/accounts/usage/batch":                fixture(t, "admin_usage"),
	})
	m := manager0(t, srv, login(t, epoch.Add(23*time.Hour)))
	p, ok := m.(adapter.Pool)
	if !ok {
		t.Fatal("the sub2api manager is not a pool")
	}
	reading, err := p.Pool(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reading.Complete {
		t.Error("a directory that fits in one read was reported incomplete")
	}
	head7, head50 := 7, 50
	want := []adapter.GroupCapacity{
		{GroupRef: "2", Accounts: 3, Failed: 1, Usable: 1, Headroom: &head7},
		{GroupRef: "5", Accounts: 2, Usable: 2, Headroom: &head50},
		{GroupRef: "7", Accounts: 1},
	}
	if diff := cmp.Diff(want, reading.Groups); diff != "" {
		t.Errorf("pool capacity differs (-want +got):\n%s", diff)
	}
	wantPaths := []string{
		"GET /api/v1/auth/me",
		"GET /api/v1/admin/accounts?page=1&page_size=100&lite=1",
		"GET /api/v1/admin/accounts?page=2&page_size=100&lite=1",
		"POST /api/v1/admin/accounts/usage/batch",
	}
	if paths := srv.paths(); !reflect.DeepEqual(wantPaths, paths) {
		t.Errorf("requests:\n got %v\nwant %v", paths, wantPaths)
	}
}

// TestPoolIsNotForARelay: a login that is not an administrator answers ErrNotPool without asking an administrator
// endpoint, so syncing a relay costs nothing extra.
func TestPoolIsNotForARelay(t *testing.T) {
	t.Parallel()
	me := fixture(t, "me")
	me.Body = withRole(t, me.Body, "user")
	srv := routes(map[string]adapter.Response{"GET /api/v1/auth/me": me})
	m := manager0(t, srv, login(t, epoch.Add(23*time.Hour)))
	_, err := m.(adapter.Pool).Pool(t.Context())
	if !errors.Is(err, adapter.ErrNotPool) {
		t.Fatalf("a relay's pool: %v, want ErrNotPool", err)
	}
	if paths := srv.paths(); !reflect.DeepEqual(paths, []string{"GET /api/v1/auth/me"}) {
		t.Errorf("a relay was asked %v, want only its account", paths)
	}
}

// TestPoolForbiddenIsNotAPool: an administrator role on a login whose token lacks the administrator scope is refused by
// the directory; that is a relay to this login, not a sync failure that would back off and page.
func TestPoolForbiddenIsNotAPool(t *testing.T) {
	t.Parallel()
	srv := routes(map[string]adapter.Response{
		"GET /api/v1/auth/me": fixture(t, "me"),
		"GET /api/v1/admin/accounts?page=1&page_size=100&lite=1": {Status: 403, Body: []byte(`{"code":403,"message":"FORBIDDEN"}`)},
	})
	m := manager0(t, srv, login(t, epoch.Add(23*time.Hour)))
	_, err := m.(adapter.Pool).Pool(t.Context())
	if !errors.Is(err, adapter.ErrNotPool) {
		t.Fatalf("a forbidden directory: %v, want ErrNotPool", err)
	}
}

// TestPoolWindowRoom: the tighter window decides the room, a window whose reset has passed no longer counts, and a
// report without either window leaves the room unknown rather than full.
func TestPoolWindowRoom(t *testing.T) {
	t.Parallel()
	now := epoch
	at := func(t time.Time) *time.Time { return &t }
	cases := map[string]struct {
		usage poolUsage
		room  int
		ok    bool
		bad   bool
	}{
		"the tighter window": {poolUsage{
			FiveHour: &poolWindow{Utilization: "92.4", ResetsAt: at(now.Add(time.Hour))},
			SevenDay: &poolWindow{Utilization: "30", ResetsAt: at(now.Add(24 * time.Hour))},
		}, 7, true, false},
		"a reset window": {poolUsage{
			FiveHour: &poolWindow{Utilization: "100", ResetsAt: at(now.Add(-time.Minute))},
			SevenDay: &poolWindow{Utilization: "20", ResetsAt: at(now.Add(time.Hour))},
		}, 80, true, false},
		"only one window": {poolUsage{FiveHour: &poolWindow{Utilization: "55"}}, 45, true, false},
		"no measurement":  {poolUsage{FiveHour: &poolWindow{ResetsAt: at(now.Add(time.Hour))}}, 0, false, false},
		"no window":       {poolUsage{}, 0, false, false},
		"over full": {poolUsage{
			FiveHour: &poolWindow{Utilization: "120", ResetsAt: at(now.Add(time.Hour))},
		}, 0, true, false},
		"quoted utilization": {poolUsage{
			FiveHour: &poolWindow{Utilization: `"50"`, ResetsAt: at(now.Add(time.Hour))},
		}, 0, false, true},
	}
	for name, c := range cases {
		room, ok, err := c.usage.remaining(now)
		if c.bad {
			if !errors.Is(err, adapter.ErrUnrecognised) {
				t.Errorf("%s: %v, want ErrUnrecognised", name, err)
			}
			continue
		}
		if err != nil || room != c.room || ok != c.ok {
			t.Errorf("%s: room %d, ok %v, err %v; want %d, %v", name, room, ok, err, c.room, c.ok)
		}
	}
}

// TestPoolSampleStaysWithinItsBudget: a pool with more groups than the sample size still asks about at most that many
// accounts, and spreads what it asks over as many groups as it can.
func TestPoolSampleStaysWithinItsBudget(t *testing.T) {
	t.Parallel()
	groups := map[string]*poolAccountGroup{}
	for i := range 200 {
		groups[fmt.Sprint(i)] = &poolAccountGroup{ids: []number{number(fmt.Sprint(i * 10))}}
	}
	ids, sample := poolSample(groups)
	if len(ids) != poolUsageSample || len(sample) != poolUsageSample {
		t.Fatalf("sample %d accounts over %d groups, want %d", len(ids), len(sample), poolUsageSample)
	}
	if len(sample["0"]) != 1 || len(sample["199"]) != 0 {
		t.Errorf("sample groups 0 and 199: %v and %v, want the first sampled and the last not", sample["0"], sample["199"])
	}
}

// TestATruncatedDirectoryIsIncomplete: a directory with more pages than one read may take is reported incomplete, so the
// engine keeps the previous reading instead of treating the groups past the cap as gone.
func TestATruncatedDirectoryIsIncomplete(t *testing.T) {
	t.Parallel()
	pages := maxPoolPages + 1
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		if r.Path == "/api/v1/auth/me" {
			return fixture(t, "me"), nil
		}
		if r.Method == http.MethodGet {
			body := fmt.Sprintf(`{"code":0,"message":"success","data":{"items":[{"id":1,"status":"active","schedulable":true,"group_ids":[1]}],"total":%d,"page":1,"page_size":100,"pages":%d}}`, pages*poolPageSize, pages)
			return adapter.Response{Status: 200, Body: []byte(body)}, nil
		}
		return adapter.Response{Status: 200, Body: []byte(`{"code":0,"message":"success","data":{"usage":{"1":{"five_hour":{"utilization":10}}}}}`)}, nil
	})
	m := manager0(t, srv, login(t, epoch.Add(23*time.Hour)))
	reading, err := m.(adapter.Pool).Pool(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if reading.Complete {
		t.Error("a directory with more pages than the cap was reported complete")
	}
	if len(reading.Groups) != 1 || reading.Groups[0].Accounts != maxPoolPages {
		t.Errorf("groups %+v, want the one group counted from the pages that were read", reading.Groups)
	}
	if n := len(srv.paths()); n != maxPoolPages+2 { // the account, the pages, one usage batch
		t.Errorf("%d requests, want the read to stop at the page cap", n)
	}
}

// TestPoolRefusesQuotedNumbers: sub2api's numbers are JSON numbers. A quoted utilization would read as spent and halve a
// healthy group, and a quoted group id would key a different group, so both are refused instead of counted.
func TestPoolRefusesQuotedNumbers(t *testing.T) {
	t.Parallel()
	page := func(groupID string) adapter.Response {
		return adapter.Response{Status: 200, Body: []byte(`{"code":0,"message":"success","data":{"items":[{"id":1,"status":"active","schedulable":true,"group_ids":[` + groupID + `]}],"total":1,"page":1,"page_size":100,"pages":1}}`)}
	}
	cases := map[string]struct {
		page  adapter.Response
		usage adapter.Response
	}{
		"quoted group id": {page: page(`"2"`)},
		"quoted utilization": {
			page:  page(`2`),
			usage: adapter.Response{Status: 200, Body: []byte(`{"code":0,"message":"success","data":{"usage":{"99":{"five_hour":{"utilization":"50"}}}}}`)},
		},
	}
	for name, c := range cases {
		srv := routes(map[string]adapter.Response{
			"GET /api/v1/auth/me": fixture(t, "me"),
			"GET /api/v1/admin/accounts?page=1&page_size=100&lite=1": c.page,
			"POST /api/v1/admin/accounts/usage/batch":                c.usage,
		})
		m := manager0(t, srv, login(t, epoch.Add(23*time.Hour)))
		if _, err := m.(adapter.Pool).Pool(t.Context()); !errors.Is(err, adapter.ErrUnrecognised) {
			t.Errorf("%s: %v, want ErrUnrecognised", name, err)
		}
	}
}

// TestPoolCountsAFailedAccountWithoutAWindow: a group whose accounts all failed has no window to sample, and is still a
// reading: the failed share is what makes it low.
func TestPoolCountsAFailedAccountWithoutAWindow(t *testing.T) {
	t.Parallel()
	srv := routes(map[string]adapter.Response{
		"GET /api/v1/auth/me": fixture(t, "me"),
		"GET /api/v1/admin/accounts?page=1&page_size=100&lite=1": {Status: 200, Body: []byte(
			`{"code":0,"message":"success","data":{"items":[{"id":1,"status":"error","schedulable":false,"group_ids":[2]}],"total":1,"page":1,"page_size":100,"pages":1}}`)},
	})
	m := manager0(t, srv, login(t, epoch.Add(23*time.Hour)))
	reading, err := m.(adapter.Pool).Pool(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []adapter.GroupCapacity{{GroupRef: "2", Accounts: 1, Failed: 1}}
	if diff := cmp.Diff(want, reading.Groups); diff != "" {
		t.Errorf("a failed-only group differs (-want +got):\n%s", diff)
	}
}

// withRole returns a recorded account answer with its role changed: the shape is the recording's, only the member a
// relay reports differs.
func withRole(t *testing.T, body []byte, role string) []byte {
	t.Helper()
	var doc map[string]any
	if err := jsonx.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	data, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatal("the recorded account answer has no data object")
	}
	data["role"] = role
	b, err := jsonx.MarshalCanonical(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
