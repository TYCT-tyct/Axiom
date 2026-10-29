package sub2api

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// Resuming on a fresh manager must not skip keys after a page shifts, or read
// the same key's usage twice after a legacy node appends a complete listing.
func TestChargeScanResumesAChangingKeyList(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"unchanged", "insert", "delete", "legacy append", "legacy mid-key", "limited"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			resumeChargeKeys(t, change)
		})
	}
}

func resumeChargeKeys(t *testing.T, change string) {
	t.Helper()
	ids := make([]string, 150)
	for i := range ids {
		ids[i] = strconv.Itoa(i + 1)
	}
	var used []string
	limited := false
	srv := changingKeyServer(&ids, &used, &limited)
	clk := &clock{t: epoch}
	loginStore := login(t, epoch.Add(24*time.Hour))
	m := managerAt(t, srv, loginStore, clk)
	initial := begin(t, m)
	clk.advance(time.Hour)
	state, decoded := listKeysStep(t, m, clk, initial)
	switch change {
	case "insert":
		ids = append([]string{"151"}, ids...)
	case "delete":
		ids = ids[1:]
	case "legacy append", "legacy mid-key":
		decoded.Pass.Keys = append(decoded.Pass.Keys, ids...)
		decoded.Pass.Listed, decoded.Pass.Page = true, 1
		if change == "legacy mid-key" {
			decoded.Pass.Key, decoded.Pass.Page = 100, 2
		}
		var err error
		state, err = decoded.encode()
		if err != nil {
			t.Fatal(err)
		}
	case "limited":
		limited = true
		_, next, err := m.Charges(t.Context(), state)
		if !errors.Is(err, adapter.ErrBucketsFull) || !slices.Equal(next, state) {
			t.Fatalf("limited read: position %q, error %v", next, err)
		}
		limited = false
		clk.advance(time.Minute)
	}
	finishChargeKeys(t, managerAt(t, srv, loginStore, clk), state)
	want := ids
	if change == "legacy mid-key" {
		want = ids[100:]
	}
	if !slices.Equal(used, want) {
		t.Fatalf("usage read for %v, want each key once: %v", used, want)
	}
}

func finishChargeKeys(t *testing.T, m adapter.Manager, state adapter.ScanState) {
	t.Helper()
	for range 200 {
		page, next, err := m.Charges(t.Context(), state)
		if err != nil {
			t.Fatal(err)
		}
		state = next
		if page.Done {
			return
		}
	}
	t.Fatal("the charge scan did not finish")
}

func changingKeyServer(ids, used *[]string, limited *bool) *server {
	return newServer(func(r adapter.Request) (adapter.Response, error) {
		if !strings.HasPrefix(r.Path, "/api/v1/keys?") {
			path, err := url.Parse(r.Path)
			if err != nil {
				return adapter.Response{}, err
			}
			if path.Query().Get("page") != "1" {
				return adapter.Response{}, fmt.Errorf("an unread key did not start at usage page 1")
			}
			*used = append(*used, path.Query().Get("api_key_id"))
			return adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"items":[],"pages":1}}`)}, nil
		}
		pg, err := keyPageOf(r.Path)
		if err != nil {
			return adapter.Response{}, err
		}
		if *limited && pg == 2 {
			return adapter.Response{}, adapter.Blocked{Until: epoch.Add(2 * time.Hour)}
		}
		start, end := min((pg-1)*100, len(*ids)), min(pg*100, len(*ids))
		var items []string
		for _, id := range (*ids)[start:end] {
			items = append(items, fmt.Sprintf(`{"id":%s,"status":"active","group_id":1,"key":"placeholder"}`, id))
		}
		return adapter.Response{Status: 200, Body: []byte(fmt.Sprintf(`{"code":0,"data":{"items":[%s],"total":%d,"pages":%d}}`, strings.Join(items, ","), len(*ids), (len(*ids)+99)/100))}, nil
	})
}

func TestOfferKeyListingRestartsAfterDeletion(t *testing.T) {
	t.Parallel()
	ids := make([]string, 150)
	for i := range ids {
		ids[i] = strconv.Itoa(i + 1)
	}
	var used []string
	limited := true
	keyServer := changingKeyServer(&ids, &used, &limited)
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		if strings.HasPrefix(r.Path, "/api/v1/keys?") {
			return keyServer.Do(t.Context(), r)
		}
		if id, ok := strings.CutPrefix(r.Path, "/api/v1/keys/"); ok {
			return adapter.Response{Status: 200, Body: []byte(fmt.Sprintf(`{"code":0,"data":{"id":%s,"key":"placeholder","group_id":1,"status":"active"}}`, id))}, nil
		}
		return manyGroups(1)(r), nil
	})
	loginStore := login(t, epoch.Add(24*time.Hour))
	first, err := manager0(t, srv, loginStore).Offer(t.Context(), nil)
	if err != nil || first.Done {
		t.Fatalf("interrupted offer: %+v, %v", first, err)
	}
	ids, limited = ids[1:], false
	page, err := manager0(t, srv, loginStore).Offer(t.Context(), first.Next)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Done || len(page.Items) != 1 {
		t.Fatalf("resumed offer: %+v", page)
	}
	if !slices.Contains(srv.paths(), "GET /api/v1/keys/2") || slices.Contains(srv.paths(), "GET /api/v1/keys/1") {
		t.Fatalf("the offer kept its deleted key: %v", srv.paths())
	}
}
