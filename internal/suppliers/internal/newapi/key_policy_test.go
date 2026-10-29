package newapi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/suppliers/internal/adapter"
)

func TestKeyPolicy(t *testing.T) {
	t.Parallel()
	expires := time.Unix(1800000000, 0)
	for _, tc := range []struct {
		name, fields string
		want         adapter.KeyPolicy
	}{
		{"unrestricted", `"expired_time":-1,"model_limits_enabled":false`, adapter.KeyPolicy{}},
		{"restricted and expiring", `"expired_time":1800000000,"model_limits_enabled":true,"model_limits":"gpt,claude"`, adapter.KeyPolicy{Models: []string{"gpt", "claude"}, ExpiresAt: &expires}},
		{"empty restriction", `"expired_time":-1,"model_limits_enabled":true,"model_limits":""`, adapter.KeyPolicy{Models: []string{}}},
		{"missing expiry", `"model_limits_enabled":false`, adapter.KeyPolicy{Models: []string{}}},
		{"missing restrictions", `"expired_time":-1`, adapter.KeyPolicy{Models: []string{}}},
		{"missing model list", `"expired_time":-1,"model_limits_enabled":true`, adapter.KeyPolicy{Models: []string{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.routes["GET /api/token/?p=1&page_size=100"] = answer(200, fmt.Sprintf(`{"success":true,"data":{"total":1,"items":[{"id":1,"status":1,%s}]}}`, tc.fields))
			keys, err := readKeys(t, open(t, f, now))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(&tc.want, keys[0].Policy); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestKeyPolicySurvivesListingResume(t *testing.T) {
	t.Parallel()
	limited := true
	f := funcClient(func(r adapter.Request) (adapter.Response, error) {
		if pageOf(t, r) == 1 {
			items := make([]string, keyPageSize)
			policies := []string{
				`"expired_time":1800000000,"model_limits_enabled":true,"model_limits":"gpt"`,
				`"expired_time":-1,"model_limits_enabled":false`,
				`"expired_time":-1,"model_limits_enabled":true,"model_limits":""`,
			}
			for i := range items {
				items[i] = fmt.Sprintf(`{"id":%d,"status":1,%s}`, i+1, policies[min(i, len(policies)-1)])
			}
			return answer(200, `{"success":true,"data":{"total":101,"items":[`+strings.Join(items, ",")+`]}}`), nil
		}
		if limited {
			return adapter.Response{}, adapter.Blocked{Until: now.Add(time.Minute)}
		}
		return answer(200, `{"success":true,"data":{"total":101,"items":[]}}`), nil
	})
	first, err := open(t, f, now).Keys(t.Context(), nil)
	if err != nil || first.Done {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	limited = false
	last, err := open(t, f, now).Keys(t.Context(), first.Next)
	if err != nil || !last.Done || len(last.Items) != keyPageSize {
		t.Fatalf("resume: %+v, %v", last, err)
	}
	want := &adapter.KeyPolicy{Models: []string{"gpt"}}
	expires := time.Unix(1800000000, 0)
	want.ExpiresAt = &expires
	for i, policy := range []*adapter.KeyPolicy{want, {}, {Models: []string{}}} {
		if diff := cmp.Diff(policy, last.Items[i].Policy); diff != "" {
			t.Fatal(diff)
		}
	}
}
