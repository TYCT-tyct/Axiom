package gateway

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/routing"
)

// TestModelListsFollowTheKeysPreset: a key's model lists hold what its routing preset lets it call
// (docs/domains/routing.md「请求覆盖与组合」). alpha-1, the cheaper of alpha's endpoints, and claude-1, claude's only one,
// are relays; the key bound to an official-only preset sees alpha at alpha-2's price and no claude, in both shapes, in
// the count and as a single model, while a key without a preset sees both at the cheapest price. The endpoint list is
// the public projection and still names both of alpha's endpoints. A key bound to a preset this node cannot route by
// lists nothing: it is refused as its calls are.
func TestModelListsFollowTheKeysPreset(t *testing.T) {
	t.Parallel()
	r, official := officialPresetRig(t)
	bound, unbound, lost := r.newAccount(1e9), r.newAccount(1e9), r.newAccount(1e9)
	bindPreset(r, bound, official)
	bindPreset(r, lost, routing.PresetID(uuid.NewV7()))
	checkKeyLists(t, r, bound, []string{"acme/alpha", "acme/beta"}, "0.000004")
	checkKeyLists(t, r, unbound, []string{"acme/alpha", "acme/beta", "anthropic/claude-live"}, "0.000002")
	resp, body := r.do(http.MethodGet, orListPath+"/acme/alpha/endpoints", bound.key, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"tag":"alpha-1"`) || !strings.Contains(body, `"tag":"alpha-2"`) {
		t.Errorf("the endpoint list under the preset: %d %s", resp.StatusCode, body)
	}
	for _, path := range []string{orListPath, orListPath + "/count", orListPath + "/acme/alpha", "/v1/models"} {
		resp, body := r.do(http.MethodGet, path, lost.key, nil)
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusServiceUnavailable || code != "preset_unavailable" {
			t.Errorf("%s for a key whose preset is gone: %d %s", path, resp.StatusCode, body)
		}
	}
}

// officialPresetRig is the rig with alpha-1 and claude-1 relays, alpha priced per endpoint (alpha-1 the cheaper), and
// a snapshot holding the open zero-ID preset and an official-only platform preset, whose ID it returns.
func officialPresetRig(t *testing.T) (*rig, routing.PresetID) {
	t.Helper()
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(name string, e *routing.Endpoint) {
			if name == "alpha-1" || name == "claude-1" {
				e.Class = routing.ClassRelay
			}
		}
	})
	dear, err := catalog.ParseSchedule([]byte(dear))
	if err != nil {
		t.Fatal(err)
	}
	r.sold[r.alpha] = billing.Sold{Version: uuid.NewV7(), Schedule: dear, Endpoints: billing.EndpointPrices{
		uuid.UUID(r.endpoints["alpha-1"].ID): r.endpoints["alpha-1"].Price.Schedule,
		uuid.UUID(r.endpoints["alpha-2"].ID): dear,
	}}
	official := routing.Preset{ID: routing.PresetID(uuid.NewV7()), Scope: routing.ScopePlatform}
	official.Classes, official.Policies = routing.ClassSet{routing.ClassOfficial}, routing.AllPolicies
	open := routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}
	snap, err := r.router.NewSnapshot(time.Now(), r.routes, []routing.Preset{open, official})
	if err != nil {
		t.Fatal(err)
	}
	r.router.Replace(snap)
	r.publish()
	return r, official.ID
}

func bindPreset(r *rig, a account, p routing.PresetID) {
	r.sql("UPDATE accounts.api_keys SET preset_id = '" + uuid.UUID(p).String() + "' WHERE id = '" + uuid.UUID(a.keyID).String() + "'")
}

// checkKeyLists checks every model list of the key: the OpenRouter list holds ids in order at alpha's completion price,
// the single model and the count agree with it, and /v1/models lists claude exactly when ids does.
func checkKeyLists(t *testing.T, r *rig, a account, ids []string, completion string) {
	t.Helper()
	list := r.orList(a, orListPath)
	got := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		got = append(got, m.ID)
	}
	if !slices.Equal(got, ids) || list.TotalCount != len(ids) {
		t.Errorf("%s lists %v of %d, want %v", orListPath, got, list.TotalCount, ids)
	}
	if price := r.orModel(a, "acme/alpha").Pricing["completion"]; price != completion {
		t.Errorf("alpha's completion is %s in the list, want %s", price, completion)
	}
	resp, body := r.do(http.MethodGet, orListPath+"/acme/alpha", a.key, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"completion":"`+completion+`"`) {
		t.Errorf("alpha by itself: %d %s", resp.StatusCode, body)
	}
	resp, body = r.do(http.MethodGet, orListPath+"/count", a.key, nil)
	if resp.StatusCode != http.StatusOK || body != `{"data":{"count":`+itoa(int64(len(ids)))+`}}` {
		t.Errorf("the count: %d %s", resp.StatusCode, body)
	}
	claude := slices.Contains(ids, "anthropic/claude-live")
	if listed := slices.Contains(r.listIDs(a), "anthropic/claude-live"); listed != claude {
		t.Errorf("/v1/models lists claude: %v, want %v", listed, claude)
	}
	resp, body = r.do(http.MethodGet, "/v1/models/anthropic/claude-live", a.key, nil)
	if (resp.StatusCode == http.StatusOK) != claude {
		t.Errorf("claude by itself: %d %s", resp.StatusCode, body)
	}
}
