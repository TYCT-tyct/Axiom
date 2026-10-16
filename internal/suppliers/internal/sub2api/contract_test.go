package sub2api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
	"axiom/internal/suppliers/internal/client"
	"axiom/internal/suppliers/internal/syncer"
	"axiom/internal/suppliers/internal/syncer/syncertest"
)

// TestScanProtocol: the module's sync starts a scan with no position and stores what comes back.
func TestScanProtocol(t *testing.T) {
	t.Parallel()
	srv := newServer(func(r adapter.Request) (adapter.Response, error) {
		return adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"items":[],"pages":1}}`)}, nil
	})
	adaptertest.RunScan(t, manager0(t, srv, login(t, epoch.Add(24*time.Hour))))
}

// TestCredentialsEchoedInCodesNeverReachErrors: an answer may put a credential in its reason or code. Only codes the
// adapter knows are ever passed on, so no failure path carries it.
func TestCredentialsEchoedInCodesNeverReachErrors(t *testing.T) {
	t.Parallel()
	const leak = "hunter2-password-echo"
	for _, body := range []string{`{"code":401,"reason":"` + leak + `"}`, `{"code":"` + leak + `"}`} {
		echo := adapter.Response{Status: 401, Body: []byte(body)}
		t.Run("login "+body[:12], func(t *testing.T) {
			srv := routes(map[string]adapter.Response{"POST /api/v1/auth/refresh": fixture(t, "refresh_reused"), "POST /api/v1/auth/login": echo})
			_, err := NewWithClock(func() time.Time { return epoch }).Open(t.Context(), srv, login(t, epoch.Add(time.Hour)))
			if !errors.Is(err, adapter.ErrAuthRejected) || strings.Contains(err.Error(), leak) {
				t.Errorf("Open = %v; want a rejection that does not quote the answer", err)
			}
		})
		t.Run("call "+body[:12], func(t *testing.T) {
			m := manager0(t, newServer(func(adapter.Request) (adapter.Response, error) { return echo, nil }), login(t, epoch.Add(24*time.Hour)))
			_, err := m.Account(t.Context())
			if !errors.Is(err, adapter.ErrAuthRejected) || strings.Contains(err.Error(), leak) {
				t.Errorf("Account = %v; want a rejection that does not quote the answer", err)
			}
		})
	}
}

// TestSuccessWithoutItsMembersIsNotAnEmptyResult: an answer that has code 0 but lacks the data, the list or the page
// count is in another form than the one known, never "no keys" or "nothing to scan": the task stops with the state
// unchanged. A genuinely empty list is accepted.
func TestSuccessWithoutItsMembersIsNotAnEmptyResult(t *testing.T) {
	t.Parallel()
	read := map[string]func(m adapter.Manager) error{
		"account": func(m adapter.Manager) error { _, err := m.Account(t.Context()); return err },
		"keys":    func(m adapter.Manager) error { _, err := m.Keys(t.Context()); return err },
		"offer":   func(m adapter.Manager) error { _, err := m.Offer(t.Context()); return err },
		"key":     func(m adapter.Manager) error { _, err := m.KeySecret(t.Context(), "2"); return err },
		"scan": func(m adapter.Manager) error {
			_, _, err := m.Charges(t.Context(), m.(adapter.Rescanner).Rescan(epoch.Add(-time.Hour)))
			return err
		},
	}
	bodies := []string{
		`{"code":0}`, `{"code":0,"data":null}`, `{"code":0,"data":{}}`, `{"code":0,"data":[]}`,
		`{"code":0,"data":{"items":[]}}`, `{"code":0,"data":{"pages":1}}`, `{"code":0,"data":{"items":null,"pages":1}}`,
	}
	for name, do := range read {
		for _, body := range bodies {
			srv := newServer(func(adapter.Request) (adapter.Response, error) {
				return adapter.Response{Status: 200, Body: []byte(body)}, nil
			})
			if err := do(manager0(t, srv, login(t, epoch.Add(24*time.Hour)))); !errors.Is(err, adapter.ErrUnrecognised) {
				// {"code":0,"data":[]} is a real empty list for the subscriptions and the groups, so those two reads may differ.
				if body == `{"code":0,"data":[]}` && (name == "account" || name == "offer") {
					continue
				}
				t.Errorf("%s with %s: %v; want ErrUnrecognised", name, body, err)
			}
		}
	}
	empty := newServer(func(adapter.Request) (adapter.Response, error) {
		return adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"items":[],"pages":1}}`)}, nil
	})
	if keys, err := manager0(t, empty, login(t, epoch.Add(24*time.Hour))).Keys(t.Context()); err != nil || len(keys) != 0 {
		t.Errorf("an empty list of keys: %v, %v", keys, err)
	}
}

// TestFiveHundredStaysUnknownWhateverItsCode: a 5xx may have run the request, also when its body names a refusal.
func TestFiveHundredStaysUnknownWhateverItsCode(t *testing.T) {
	t.Parallel()
	q := New().Calls()
	for _, status := range []int{500, 502, 504} {
		for _, body := range []string{`{"error":{"code":"model_not_found"}}`, `{"error":{"code":"context.Contextlength_exceeded"}}`, `{"code":"INSUFFICIENT_BALANCE"}`} {
			if f := q.Classify(status, nil, []byte(body)); f.Executed != adapter.ExecutionUnknown {
				t.Errorf("Classify(%d, %s) = %+v; a 5xx may have run", status, body, f)
			}
		}
	}
	if f := q.Classify(404, nil, []byte(`{"error":{"code":"model_not_found"}}`)); f.Executed != adapter.NotExecuted || f.Cause != (adapter.ModelNotFound{}) {
		t.Errorf("a 404 model_not_found = %+v", f)
	}
}

// limited is a Client that counts requests in the adapter's buckets with the program's own limiter, on a fake clock,
// and answers what the table says.
func limited(clk *clock, answer func(adapter.Request) adapter.Response) *server {
	limiter := client.NewLimiter(New().Buckets(), clk.now)
	return newServer(func(r adapter.Request) (adapter.Response, error) {
		if err := limiter.Take(context.Background(), r.Buckets); err != nil {
			return adapter.Response{}, err
		}
		return answer(r), nil
	})
}

// manyGroups answers a supplier with n groups, each with one key of its own.
func manyGroups(n int) func(adapter.Request) adapter.Response {
	ok := func(body string) adapter.Response { return adapter.Response{Status: 200, Body: []byte(body)} }
	return func(r adapter.Request) adapter.Response {
		switch {
		case r.Path == "/api/v1/groups/available":
			var groups []string
			for i := range n {
				groups = append(groups, fmt.Sprintf(`{"id":%d,"name":"g%d","platform":"anthropic","status":"active","subscription_type":"standard","rate_multiplier":1}`, i+1, i+1))
			}
			return ok(`{"code":0,"data":[` + strings.Join(groups, ",") + `]}`)
		case r.Path == "/api/v1/groups/rates":
			return ok(`{"code":0,"data":{}}`)
		case strings.HasPrefix(r.Path, "/api/v1/keys?"):
			var keys []string
			for i := range n {
				keys = append(keys, fmt.Sprintf(`{"id":%d,"key":"k%d","group_id":%d,"status":"active"}`, i+1, i+1, i+1))
			}
			return ok(`{"code":0,"data":{"items":[` + strings.Join(keys, ",") + `],"pages":1}}`)
		case r.Path == "/v1/models":
			return ok(`{"data":[{"id":"m1"}]}`)
		case r.Path == "/v1/sub2api/billing":
			return ok(`{"resolved_rate_multiplier":1}`)
		}
		return adapter.Response{Status: 404}
	}
}

// TestOfferLargerThanOneRateWindowFinishes: 29 groups with a key each need 3 + 29×2 = 61 panel requests, one more than a
// window allows. The offer is kept between calls, so it ends in the next window instead of starting over for ever; the
// groups are listed once.
func TestOfferLargerThanOneRateWindowFinishes(t *testing.T) {
	t.Parallel()
	clk := &clock{t: epoch}
	srv := limited(clk, manyGroups(29))
	m := managerAt(t, srv, login(t, epoch.Add(24*time.Hour)), clk)
	var offer adapter.Offer
	var err error
	windows := 0
	for ; windows < 4; windows++ {
		if offer, err = m.Offer(t.Context()); err == nil {
			break
		}
		if !errors.Is(err, adapter.ErrBucketsFull) {
			t.Fatal(err)
		}
		clk.advance(61 * time.Second)
	}
	if err != nil || len(offer.Groups) != 29 || windows != 1 {
		t.Fatalf("Offer finished with %d groups after %d full windows, err %v; want 29 after 1", len(offer.Groups), windows, err)
	}
	if n := len(srv.paths()); n < 61 {
		t.Errorf("only %d requests", n)
	}
	listed := 0
	for _, p := range srv.paths() {
		if p == "GET /api/v1/groups/available" {
			listed++
		}
	}
	if listed != 1 {
		t.Errorf("the groups were listed %d times: the progress was not kept", listed)
	}
}

// TestKeyListingLargerThanOneRateWindowFinishes: the same for the keys, 100 to a page.
func TestKeyListingLargerThanOneRateWindowFinishes(t *testing.T) {
	t.Parallel()
	clk := &clock{t: epoch}
	pages := 0
	srv := limited(clk, func(r adapter.Request) adapter.Response {
		q := strings.TrimPrefix(r.Path, "/api/v1/keys?page=")
		page, err := strconv.Atoi(strings.SplitN(q, "&", 2)[0])
		if err != nil {
			panic(err) // the test's own request
		}
		pages++
		var keys []string
		for i := range 100 {
			keys = append(keys, fmt.Sprintf(`{"id":%d,"key":"k","group_id":1,"status":"active"}`, (page-1)*100+i+1))
		}
		return adapter.Response{Status: 200, Body: []byte(`{"code":0,"data":{"items":[` + strings.Join(keys, ",") + `],"pages":70}}`)}
	})
	m := managerAt(t, srv, login(t, epoch.Add(24*time.Hour)), clk)
	var keys []adapter.KeyState
	var err error
	for range 4 {
		if keys, err = m.Keys(t.Context()); err == nil {
			break
		}
		clk.advance(61 * time.Second)
	}
	if err != nil || len(keys) != 7000 || pages != 70 {
		t.Errorf("Keys = %d keys, %d pages read, %v; want 7000 keys from 70 pages read once each", len(keys), pages, err)
	}
}

// usageSupplier is a supplier whose usage list shows each record from the moment it becomes visible and filters by date
// as sub2api does.
type usageSupplier struct {
	clk  *clock
	recs []struct {
		ref     string
		created time.Time
		visible time.Time
	}
}

func (u *usageSupplier) answer(r adapter.Request) adapter.Response {
	ok := func(body string) adapter.Response { return adapter.Response{Status: 200, Body: []byte(body)} }
	if strings.HasPrefix(r.Path, "/api/v1/keys?") {
		return ok(`{"code":0,"data":{"items":[{"id":7,"key":"k","group_id":1,"status":"active"}],"pages":1}}`)
	}
	q := strings.SplitN(r.Path, "?", 2)[1]
	get := func(name string) string {
		for kv := range strings.SplitSeq(q, "&") {
			if k, v, _ := strings.Cut(kv, "="); k == name {
				return v
			}
		}
		return ""
	}
	var items []string
	for _, rec := range u.recs {
		day := rec.created.Format(time.DateOnly)
		if !rec.visible.After(u.clk.now()) && day >= get("start_date") && day <= get("end_date") {
			items = append(items, fmt.Sprintf(`{"request_id":%q,"api_key_id":7,"model":"m","created_at":%q,"actual_cost":1,"billing_type":0,"billing_mode":"token","input_tokens":1,"output_tokens":1}`,
				rec.ref, rec.created.Format(time.RFC3339)))
		}
	}
	return ok(`{"code":0,"data":{"items":[` + strings.Join(items, ",") + `],"pages":1}}`)
}

// healthyOthers lets the engine's other tasks succeed beside the charges.
type healthyOthers struct{ adapter.Manager }

func (healthyOthers) Account(context.Context) (adapter.Account, error) {
	return adapter.Account{Balance: "100", Funding: adapter.FundingWallet}, nil
}

func (healthyOthers) Offer(context.Context) (adapter.Offer, error)     { return adapter.Offer{}, nil }
func (healthyOthers) Keys(context.Context) ([]adapter.KeyState, error) { return nil, nil }

func (h healthyOthers) Rescan(since time.Time) adapter.ScanState {
	return h.Manager.(adapter.Rescanner).Rescan(since)
}

type hooks struct{}

func (hooks) InFlight(context.Context) (int, error) { return 0, nil }
func (hooks) DeleteSecrets(context.Context) error   { return nil }

// TestEngineDrivesTheAdapterAndAMovedRecordIsFound: the real adapter runs under the real engine. A first round only
// marks the start; a quarter of an hour later the records in the window are stored once each, however often they are read. A removal
// then reads once more after the grace period, from before it began, so a record that was created before the first
// final read and appeared during the wait is stored too, though its day is before the adapter's own next window.
func TestEngineDrivesTheAdapterAndAMovedRecordIsFound(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Date(2026, 10, 15, 0, 10, 0, 0, time.UTC)}
	sup := &usageSupplier{clk: clk}
	at := func(s string) time.Time { return mustTime(t, s) }
	add := func(ref, created, visible string) {
		sup.recs = append(sup.recs, struct {
			ref     string
			created time.Time
			visible time.Time
		}{ref, at(created), at(visible)})
	}
	add("client:early", "2026-10-15T00:12:00Z", "2026-10-15T00:12:00Z")
	add("client:late", "2026-10-14T23:59:00Z", "2026-10-15T01:00:00Z") // created before midnight, appears 35 minutes after the removal began
	srv := newServer(func(r adapter.Request) (adapter.Response, error) { return sup.answer(r), nil })
	m := healthyOthers{managerAt(t, srv, login(t, clk.t.Add(24*time.Hour)), clk)}
	store := syncertest.New()
	e := syncer.New(store, m, syncer.Defaults(), clk.now)

	if err := e.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	clk.advance(15 * time.Minute) // 00:25
	if err := e.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Charges["client:early"]; !ok || len(store.Charges) != 1 || store.State().ScanOpen {
		t.Fatalf("after the first pass: %d charges, scan open %v", len(store.Charges), store.State().ScanOpen)
	}
	if done, err := e.Remove(t.Context(), hooks{}); done || err != nil {
		t.Fatalf("Remove = %v, %v; want it to wait out the grace period", done, err)
	}
	clk.advance(36 * time.Minute) // 01:01: the grace period is over; the late record has appeared
	if done, err := e.Remove(t.Context(), hooks{}); !done || err != nil {
		t.Fatalf("Remove = %v, %v", done, err)
	}
	if _, ok := store.Charges["client:late"]; !ok {
		t.Errorf("the record that appeared during the wait was not read: %d charges", len(store.Charges))
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
