package suppliers

import (
	"testing"
)

// TestKeyState: a group's own row says failed, pending or unavailable; a group the supplier no longer lists, or one a
// person unselected, is unavailable whatever an older attempt left behind. A group the sync has not selected yet has no
// code name, so its pending or failed key is shown while it is being onboarded.
func TestKeyState(t *testing.T) {
	t.Parallel()
	reason := "refused"
	codename := "gabc"
	cases := []struct {
		name                             string
		present, selected, chosen, needs bool
		keyError                         *string
		state, why                       string
	}{
		{"needs a key", true, true, true, true, nil, "pending", ""},
		{"has no need", true, true, true, false, nil, "unavailable", ""},
		{"an attempt failed", true, true, true, true, &reason, "failed", "refused"},
		{"never chosen, still onboarding", true, false, false, true, nil, "pending", ""},
		{"never chosen and failed", true, false, false, true, &reason, "failed", "refused"},
		{"the supplier no longer lists it", false, true, true, true, &reason, "unavailable", ""},
		{"a person unselected it", true, false, true, true, &reason, "unavailable", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			state, why := keyStateOf(c.present, c.selected, c.chosen, c.needs, c.keyError)
			if state != c.state || why != c.why {
				t.Fatalf("keyStateOf = %q, %q; want %q, %q", state, why, c.state, c.why)
			}
			var name *string
			if c.chosen {
				name = &codename
			}
			g, err := groupOf(groupFacts{Present: c.present, Selected: c.selected, Codename: name, KeyEligible: c.needs, KeyError: c.keyError, Facts: []byte("{}")})
			if err != nil || g.KeyState != c.state || g.KeyReason != c.why {
				t.Errorf("groupOf = %+v, %v", g, err)
			}
		})
	}
}
