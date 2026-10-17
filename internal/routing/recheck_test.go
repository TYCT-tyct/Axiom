package routing_test

import (
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// TestPriceLimitHoldsForTheLongestPrompt: the length that picks the context tier is billing's own bound, so images, files
// and the later samplings of a tool-using request count, not only the text.
func TestPriceLimitHoldsForTheLongestPrompt(t *testing.T) {
	t.Parallel()
	// Input costs 1e-6, and 5e-6 above 100000 tokens; the limit is 2e-6. A page takes up to 2000 tokens, a file 100 pages.
	multimodal := func(e *routing.Endpoint) {
		e.Price.Schedule = tiered(t, "0.000001", "0.000001", "0.000005")
		e.ImageTokens, e.PageTokens, e.FilePages = 20000, 2000, 100
	}
	preset := openPreset()
	preset.MaxPrice = limitOn(t, catalog.ItemInput, "0.000002")
	h := newHarness(t, routing.Config{}, preset, endpoint(t, 1, multimodal))
	cases := []struct {
		name    string
		request func(*routing.Input)
		fits    bool
	}{
		{"text only", func(*routing.Input) {}, true},
		{"two images stay under the tier", func(in *routing.Input) { in.Req.Images = 2 }, true},
		{"ten images cross it", func(in *routing.Input) { in.Req.Images = 10 }, false},
		{"one file may cross it", func(in *routing.Input) { in.Req.Documents = 1 }, false},
		{"a search lets later samplings reread the context", func(in *routing.Input) {
			in.Req.Needs, in.Req.WebSearches = protocol.WebSearch, 2
		}, false},
		{"code execution does too", func(in *routing.Input) {
			in.Req.Needs, in.Req.CodeExecutions = protocol.CodeExecution, 1
		}, false},
		{"compaction samples again and rereads the context", func(in *routing.Input) {
			in.Req.Compacts = true
		}, false},
	}
	for _, c := range cases {
		in := input()
		c.request(&in)
		if _, _, err := h.r.Plan(in); (err == nil) != c.fits {
			t.Errorf("%s: error %v, want fits %v", c.name, err, c.fits)
		}
	}
}

// TestPriceLimitWithMissingPrices: a tier without a price does not hide the prices that are known, and a token item
// without a price is held at the schedule's highest token price, which is what billing charges it.
func TestPriceLimitWithMissingPrices(t *testing.T) {
	t.Parallel()
	flexNoInput := func(standardInput string) catalog.Schedule {
		s, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"` + standardInput + `","output":"0.000004"},
			"service_tiers":{"flex":{"rates":{"output":"0.000001"}}}}`))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	flexPriced, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000004"},
		"service_tiers":{"flex":{"rates":{"input":"0.000001","output":"0.000001"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	offered := catalog.ServiceTierSet(1<<catalog.ServiceTierStandard | 1<<catalog.ServiceTierFlex)
	cases := []struct {
		name     string
		schedule catalog.Schedule
		tiers    catalog.ServiceTierSet
		limit    string
		fits     bool
	}{
		{"standard is priced and within the limit", flexNoInput("0.000001"), 1 << catalog.ServiceTierStandard, "0.000002", true},
		{"flex has no input price: the highest token price stands in", flexNoInput("0.000001"), 1 << catalog.ServiceTierFlex, "0.000002", false},
		{"the same, within a limit that covers it", flexNoInput("0.000001"), 1 << catalog.ServiceTierFlex, "0.000005", true},
		{"standard is charged too, as billing reserves it, though the request is for flex", flexPriced, 1 << catalog.ServiceTierFlex, "0.000002", false},
		{"a known price over the limit counts although another tier has none", flexNoInput("0.000003"), 1 << catalog.ServiceTierFlex, "0.000002", false},
	}
	for _, c := range cases {
		h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1, func(e *routing.Endpoint) {
			e.Price.Schedule, e.ServiceTiers = c.schedule, offered
		}))
		in := input()
		in.Req.ServiceTiers = c.tiers
		in.Override.MaxPrice = limitOn(t, catalog.ItemInput, c.limit)
		if _, _, err := h.r.Plan(in); (err == nil) != c.fits {
			t.Errorf("%s: error %v, want fits %v", c.name, err, c.fits)
		}
	}
	// A call item the schedule does not price has the site's default price, which routing does not know: not compared.
	h := newHarness(t, routing.Config{}, openPreset(), endpoint(t, 1))
	in := input()
	in.Override.MaxPrice = limitOn(t, catalog.ItemWebSearch, "0.000001")
	h.plan(in)
}

// TestSessionKeyOfALongStringInput: a string input cut off by the prefix still contributes its start, so conversations
// that begin differently get different keys, and ones that differ only after the prefix get the same.
func TestSessionKeyOfALongStringInput(t *testing.T) {
	t.Parallel()
	r, err := routing.New(routing.Config{SessionPrefix: 512})
	if err != nil {
		t.Fatal(err)
	}
	body := func(text string) string { return `{"model":"m","input":"` + text + `"}` }
	long := func(start string, tail string) string { return start + strings.Repeat("a", 800) + tail }
	if key(t, r, acct, "m", body(long("first ", ""))) == key(t, r, acct, "m", body(long("second ", ""))) {
		t.Error("two long inputs with different starts have one key")
	}
	if key(t, r, acct, "m", body(long("same ", "x"))) != key(t, r, acct, "m", body(long("same ", "y"))) {
		t.Error("two long inputs that differ only past the prefix have two keys")
	}
	// The same for a first message cut off inside an array.
	msg := func(start, tail string) string {
		return `{"messages":[{"role":"user","content":"` + long(start, tail) + `"}]}`
	}
	if key(t, r, acct, "m", msg("one ", "")) == key(t, r, acct, "m", msg("two ", "")) {
		t.Error("two long first messages with different starts have one key")
	}
}
