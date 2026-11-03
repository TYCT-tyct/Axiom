package cpa

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
)

var epoch = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

type server struct {
	answer func(adapter.Request) (adapter.Response, error)
	reqs   []adapter.Request
}

func (s *server) Do(_ context.Context, r adapter.Request) (adapter.Response, error) {
	s.reqs = append(s.reqs, r)
	return s.answer(r)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name) //nolint:gosec // the name is a fixture chosen by this test
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func recorded(t *testing.T) *server {
	t.Helper()
	return &server{answer: func(r adapter.Request) (adapter.Response, error) {
		switch {
		case r.Path == "/":
			return adapter.Response{Status: http.StatusOK, Body: fixture(t, "root.json")}, nil
		case strings.HasPrefix(r.Path, "/v0/management/auth-files?"):
			return adapter.Response{Status: http.StatusOK, Body: fixture(t, "auth-files.json")}, nil
		case strings.HasPrefix(r.Path, "/v0/management/auth-files/models?name=claude-1.json"):
			return adapter.Response{Status: http.StatusOK, Body: fixture(t, "models-claude.json")}, nil
		case strings.HasPrefix(r.Path, "/v0/management/auth-files/models?name=codex-1.json"):
			return adapter.Response{Status: http.StatusOK, Body: fixture(t, "models-codex.json")}, nil
		case strings.HasPrefix(r.Path, "/v0/management/auth-files/models?name=dead-1.json"):
			return adapter.Response{Status: http.StatusOK, Body: fixture(t, "models-dead.json")}, nil
		case r.Path == "/v0/management/api-keys":
			return adapter.Response{Status: http.StatusOK, Body: fixture(t, "api-keys.json")}, nil
		case strings.HasPrefix(r.Path, "/v0/management/usage-queue?"):
			return adapter.Response{Status: http.StatusOK, Body: fixture(t, "usage-queue.json")}, nil
		default:
			return adapter.Response{Status: http.StatusNotFound}, nil
		}
	}}
}

type testLogin struct{ raw []byte }

func (l testLogin) Load(context.Context) ([]byte, int64, error) { return l.raw, 1, nil }
func (testLogin) Swap(context.Context, int64, []byte) error     { return adapter.ErrStaleLogin }

func manager0(t *testing.T, srv *server) *manager {
	t.Helper()
	l, err := New().NewLogin(adapter.Credentials{AccessToken: "mgmt-key"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewWithClock(func() time.Time { return epoch }).Open(t.Context(), srv, testLogin{raw: l.Plaintext})
	if err != nil {
		t.Fatal(err)
	}
	return m.(*manager)
}

func TestContract(t *testing.T) {
	t.Parallel()
	adaptertest.Run(t, New())
}

func TestDetectAndChargeRef(t *testing.T) {
	t.Parallel()
	srv := recorded(t)
	got, err := New().Detect(t.Context(), srv)
	if err != nil || got.Generation != "v8" {
		t.Fatalf("Detect = %+v, %v", got, err)
	}
	q := New().Calls()
	if ref, ok := q.ChargeRef(http.Header{"X-Cpa-Trace-Id": {"20261103100000-3-019c0f3e-6a4b-7c8d-9e0f-1a2b3c4d5e6f"}}); !ok || ref != "019c0f3e-6a4b-7c8d-9e0f-1a2b3c4d5e6f" {
		t.Errorf("ChargeRef = %q, %v", ref, ok)
	}
	if _, ok := q.ChargeRef(http.Header{"X-Cpa-Trace-Id": {"not-a-trace"}}); ok {
		t.Error("a malformed trace id was accepted")
	}
}

func TestAccount(t *testing.T) {
	t.Parallel()
	account, err := manager0(t, recorded(t)).Account(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if account.Balance != "2" || account.Funding != adapter.FundingWallet || account.UnitHint != "1" {
		t.Fatalf("Account = %+v", account)
	}
}

func TestPool(t *testing.T) {
	t.Parallel()
	pool, err := manager0(t, recorded(t)).Pool(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !pool.Complete || len(pool.Groups) != 1 {
		t.Fatalf("Pool = %+v", pool)
	}
	p := pool.Groups[0]
	if p.Accounts != 3 || p.Failed != 1 || p.Usable != 2 || p.Headroom == nil || *p.Headroom != 35 {
		t.Fatalf("capacity = %+v, headroom %v", p, p.Headroom)
	}
}

func TestOfferHasTheThreeModelsWithZeroCost(t *testing.T) {
	t.Parallel()
	offer, err := manager0(t, recorded(t)).Offer(t.Context(), nil)
	if err != nil || !offer.Done || len(offer.Items) != 1 {
		t.Fatalf("Offer = %+v, %v", offer, err)
	}
	g := offer.Items[0]
	if g.Ref != groupRef || len(g.Models) != 3 || g.ModelsUnread {
		t.Fatalf("group = %+v", g)
	}
	u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 1, catalog.ItemOutput: 1}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range g.Models {
		if model.Cost == nil {
			t.Fatalf("%s has no zero cost", model.UpstreamName)
		}
		if _, missing, err := model.Cost.Cost(u); err != nil || missing != 0 {
			t.Fatalf("%s cost missing %d: %v", model.UpstreamName, missing, err)
		}
	}
}

func TestKeysHideTheSecret(t *testing.T) {
	t.Parallel()
	m := manager0(t, recorded(t))
	keys, err := m.Keys(t.Context(), nil)
	if err != nil || !keys.Done || len(keys.Items) != 2 {
		t.Fatalf("Keys = %+v, %v", keys, err)
	}
	for _, key := range keys.Items {
		if key.GroupRef != groupRef || strings.Contains(fmt.Sprint(key), "sk-cpa-test") {
			t.Fatalf("key row leaks the secret: %+v", key)
		}
	}
	secret, err := m.KeySecret(t.Context(), keys.Items[0].Ref)
	if err != nil || !strings.HasPrefix(secret.Reveal(), "sk-cpa-test-") {
		t.Fatalf("KeySecret = %q, %v", secret.Reveal(), err)
	}
}

func TestUsageDrainSanitises(t *testing.T) {
	t.Parallel()
	records, err := manager0(t, recorded(t)).DrainUsage(t.Context(), 100)
	if err != nil || len(records) != 2 {
		t.Fatalf("DrainUsage = %d records, %v", len(records), err)
	}
	if strings.Contains(string(records[0].Raw), "sk-cpa-test") || !strings.Contains(string(records[0].Raw), "api_key_sha256") {
		t.Fatalf("the stored record is not sanitised: %s", records[0].Raw)
	}
}

func drained(t *testing.T) []adapter.UsageRecord {
	t.Helper()
	records, err := manager0(t, recorded(t)).DrainUsage(t.Context(), 100)
	if err != nil || len(records) != 2 {
		t.Fatalf("DrainUsage = %d records, %v", len(records), err)
	}
	return records
}

func TestUsageParsesTheCompleteRecord(t *testing.T) {
	t.Parallel()
	records := drained(t)
	first, err := manager0(t, recorded(t)).ParseUsage(records[0].Raw)
	if err != nil {
		t.Fatal(err)
	}
	if first.Ref != "019c0f3e-6a4b-7c8d-9e0f-1a2b3c4d5e6f" || first.Key != keyRef("sk-cpa-test-1") {
		t.Fatalf("first charge identity = %+v", first)
	}
	if first.Amount != "0" || first.Funding != adapter.FundingWallet || first.Model != "claude-sonnet-4-5" {
		t.Fatalf("first charge terms = %+v", first)
	}
	counts := []struct {
		item catalog.Item
		want int64
	}{
		{catalog.ItemInput, 150},
		{catalog.ItemCacheRead, 40},
		{catalog.ItemCacheWrite, 10},
		{catalog.ItemOutput, 70},
		{catalog.ItemReasoning, 30},
	}
	for _, c := range counts {
		if got := first.Usage.Count(c.item); got != c.want {
			t.Errorf("%s = %d, want %d", c.item, got, c.want)
		}
	}
	for _, item := range []catalog.Item{catalog.ItemInput, catalog.ItemCacheRead, catalog.ItemCacheWrite, catalog.ItemOutput, catalog.ItemReasoning} {
		if !first.Reported.Has(item) {
			t.Errorf("%s was not reported", item)
		}
	}
}

func TestUsageToleratesANewBreakdownMember(t *testing.T) {
	t.Parallel()
	raw := bytes.Replace(drained(t)[0].Raw, []byte(`"schema_version":2`), []byte(`"schema_version":2,"future_counter":1`), 1)
	if _, err := manager0(t, recorded(t)).ParseUsage(raw); err != nil {
		t.Fatalf("a new breakdown member was rejected: %v", err)
	}
}

func TestUsageParsesTheUnclassifiedRecord(t *testing.T) {
	t.Parallel()
	second, err := manager0(t, recorded(t)).ParseUsage(drained(t)[1].Raw)
	if err != nil {
		t.Fatal(err)
	}
	if second.Reported != 0 || second.Usage.Count(catalog.ItemInput) != 8 || len(second.Usage.Unknown()) != 2 {
		t.Fatalf("second charge = %+v, unknown %+v", second, second.Usage.Unknown())
	}
}

func TestOfferResumesAfterABlockedModelRead(t *testing.T) {
	t.Parallel()
	blocked := true
	srv := recorded(t)
	base := srv.answer
	srv.answer = func(r adapter.Request) (adapter.Response, error) {
		if blocked && strings.Contains(r.Path, "codex-1.json") {
			return adapter.Response{}, adapter.Blocked{Until: epoch.Add(time.Minute)}
		}
		return base(r)
	}
	m := manager0(t, srv)
	first, err := m.Offer(t.Context(), nil)
	if err != nil || first.Done || len(first.Next) == 0 || first.Until.IsZero() {
		t.Fatalf("first Offer = %+v, %v", first, err)
	}
	blocked = false
	second, err := m.Offer(t.Context(), first.Next)
	if err != nil || !second.Done || len(second.Items) != 1 || len(second.Items[0].Models) != 3 {
		t.Fatalf("second Offer = %+v, %v", second, err)
	}
}

func TestPoolRefusesAnUnreadableWindow(t *testing.T) {
	t.Parallel()
	srv := recorded(t)
	base := srv.answer
	srv.answer = func(r adapter.Request) (adapter.Response, error) {
		if strings.HasPrefix(r.Path, "/v0/management/auth-files?") {
			return adapter.Response{Status: http.StatusOK, Body: []byte(`{"files":[{"name":"a.json","provider":"claude","status":"active","quota":{"signals":{"Anthropic-Ratelimit-Unified-5h-Utilization":"a lot"}}}]}`)}, nil
		}
		return base(r)
	}
	_, err := manager0(t, srv).Pool(t.Context())
	if !errors.Is(err, adapter.ErrUnrecognised) {
		t.Fatalf("Pool error = %v, want ErrUnrecognised", err)
	}
}

func TestAnAnswerWithoutItsListIsNotAnEmptyList(t *testing.T) {
	t.Parallel()
	t.Run("auth files", func(t *testing.T) {
		srv := &server{answer: func(adapter.Request) (adapter.Response, error) {
			return adapter.Response{Status: http.StatusOK, Body: []byte(`{}`)}, nil
		}}
		requireUnrecognised(t, func() error { _, err := manager0(t, srv).Account(t.Context()); return err })
	})
	t.Run("models", func(t *testing.T) {
		srv := recorded(t)
		base := srv.answer
		srv.answer = func(r adapter.Request) (adapter.Response, error) {
			if strings.Contains(r.Path, "auth-files/models") {
				return adapter.Response{Status: http.StatusOK, Body: []byte(`{}`)}, nil
			}
			return base(r)
		}
		requireUnrecognised(t, func() error { _, err := manager0(t, srv).Offer(t.Context(), nil); return err })
	})
	t.Run("keys", func(t *testing.T) {
		srv := recorded(t)
		base := srv.answer
		srv.answer = func(r adapter.Request) (adapter.Response, error) {
			if strings.HasPrefix(r.Path, "/v0/management/api-keys") {
				return adapter.Response{Status: http.StatusOK, Body: []byte(`{}`)}, nil
			}
			return base(r)
		}
		requireUnrecognised(t, func() error { _, err := manager0(t, srv).Keys(t.Context(), nil); return err })
	})
}

func requireUnrecognised(t *testing.T, f func() error) {
	t.Helper()
	if err := f(); !errors.Is(err, adapter.ErrUnrecognised) {
		t.Fatalf("error = %v, want ErrUnrecognised", err)
	}
}

func TestNewLoginTakesOnlyTheManagementKey(t *testing.T) {
	t.Parallel()
	a := New()
	if _, err := a.NewLogin(adapter.Credentials{AccessToken: "k", Password: "p"}); err == nil {
		t.Fatal("a password was accepted")
	}
	l, err := a.NewLogin(adapter.Credentials{AccessToken: "k"})
	if err != nil || l.Kind != "cpa" {
		t.Fatalf("NewLogin = %+v, %v", l, err)
	}
	decoded, err := DecodeLogin(l.Plaintext)
	if err != nil || decoded.Key.Reveal() != "k" {
		t.Fatalf("DecodeLogin = %v, %v", decoded, err)
	}
	if fn := a.LoginFields(false); len(fn) != 1 || fn[0] != adapter.FieldAccessToken {
		t.Fatalf("LoginFields = %v", fn)
	}
	if a.NativeUnit() != "quota" {
		t.Fatalf("NativeUnit = %q", a.NativeUnit())
	}
}

func TestChargesIsAnEmptyDonePage(t *testing.T) {
	t.Parallel()
	page, _, err := manager0(t, recorded(t)).Charges(t.Context(), nil)
	if err != nil || !page.Done || len(page.Charges) != 0 || page.Through.IsZero() {
		t.Fatalf("Charges = %+v, %v", page, err)
	}
}

func TestUsageDrainDefersOn429(t *testing.T) {
	t.Parallel()
	srv := recorded(t)
	srv.answer = func(adapter.Request) (adapter.Response, error) {
		return adapter.Response{Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"7"}}}, nil
	}
	_, err := manager0(t, srv).DrainUsage(t.Context(), 1)
	blocked, ok := errors.AsType[adapter.Blocked](err)
	if !ok || !blocked.Until.Equal(epoch.Add(7*time.Second)) {
		t.Fatalf("DrainUsage error = %v, want Blocked until %v", err, epoch.Add(7*time.Second))
	}
}
