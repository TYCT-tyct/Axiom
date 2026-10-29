package suppliers

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// TestParseChargeUsage: billing reads the counts, the items the supplier stated, and the physical input from the
// charge's stored usage; an item counted but not stated stays unreported, so it cannot correct an estimate alone.
func TestParseChargeUsage(t *testing.T) {
	u, reported, err := parseChargeUsage([]byte(`{"items":{"input":50,"output":2,"cache_read":10},` +
		`"reported":["input","output"],"unknown":[{"field":"usage.new_tokens","value":"3"}],` +
		`"tier":"standard","physical_input":55}`))
	if err != nil {
		t.Fatal(err)
	}
	if u.Count(catalog.ItemInput) != 50 || u.Count(catalog.ItemOutput) != 2 || u.Count(catalog.ItemCacheRead) != 10 {
		t.Errorf("counts = input %d output %d cache %d", u.Count(catalog.ItemInput), u.Count(catalog.ItemOutput), u.Count(catalog.ItemCacheRead))
	}
	if !reported.Has(catalog.ItemInput) || !reported.Has(catalog.ItemOutput) || reported.Has(catalog.ItemCacheRead) {
		t.Errorf("reported = %v", reported)
	}
	if n, ok := u.PhysicalInput(); !ok || n != 55 {
		t.Errorf("physical input = %d, %v; want 55", n, ok)
	}
	if len(u.Unknown()) != 1 || u.Unknown()[0].Field != "usage.new_tokens" {
		t.Errorf("unknown = %+v", u.Unknown())
	}
	if u.ServiceTier() != catalog.ServiceTierStandard {
		t.Errorf("tier = %v", u.ServiceTier())
	}
}

func TestParseChargeUsageRefusesWhatItCannotPrice(t *testing.T) {
	for _, body := range []string{
		`{"items":{"not_an_item":1},"tier":"standard"}`,
		`{"items":{"input":1},"reported":["not_an_item"],"tier":"standard"}`,
		`{"items":{"input":1},"tier":"not_a_tier"}`,
	} {
		if _, _, err := parseChargeUsage([]byte(body)); err == nil {
			t.Errorf("%s parsed", body)
		}
	}
}

// TestChargeEventUsesTheRecordedAttempt: the event's payload may predate a reattribution; the charge record's attempt
// sequence is then the authority, so a correction still matches the reservation that was billed.
func TestChargeEventKeepsTheRecordsUsageWhateverTheAttemptsCostFlag(t *testing.T) {
	id, charge, request := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	ev, err := chargeEvent(id, charge, 1, time.Now(), []byte(`{"cost_flag":"no_usage","attempt_seq":1}`),
		[]byte(`{"items":{"input":50},"reported":["input"],"tier":"standard"}`), &request, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ev.CostFlag != CostNoUsage.String() || ev.Usage.Count(catalog.ItemInput) != 50 || !ev.Reported.Has(catalog.ItemInput) {
		t.Errorf("event = %+v, want the record's 50 input and no_usage cost flag", ev)
	}
}

func TestChargeEventUsesTheRecordedAttempt(t *testing.T) {
	id, charge, request := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	ev, err := chargeEvent(id, charge, 2, time.Now(), []byte(`{"cost_flag":"complete","attempt_seq":2}`),
		[]byte(`{"items":{"input":1},"reported":["input"],"tier":"standard"}`), &request, 3)
	if err != nil {
		t.Fatal(err)
	}
	if ev.AttemptSeq != platform.AttemptSeq(3) {
		t.Errorf("attempt = %d, want the recorded 3", ev.AttemptSeq)
	}
}
