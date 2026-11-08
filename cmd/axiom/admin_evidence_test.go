package main

import (
	"testing"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
)

// TestEvidenceFingerprintsAsReleased: a publication's Idempotency-Key fingerprint covers its evidence, so a retry that
// reaches a node of another release must encode the same evidence to the same bytes. Released nodes decode evidence
// into a map, whose numbers are float64: an integer past 2⁵³ is rounded before the fingerprint, and must stay so.
func TestEvidenceFingerprintsAsReleased(t *testing.T) {
	t.Parallel()
	const evidence, released = `{"record_id":9007199254740993}`, `{"record_id":9007199254740992}`
	var publish adminapi.PricePublish
	if err := jsonx.Unmarshal([]byte(`{"evidence":`+evidence+`}`), &publish); err != nil {
		t.Fatal(err)
	}
	var switched adminapi.PriceSourceSelect
	if err := jsonx.Unmarshal([]byte(`{"prices":[{"evidence":`+evidence+`}]}`), &switched); err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]adminapi.Evidence{"publish": publish.Evidence, "switch": switched.Prices[0].Evidence} {
		encoded, err := jsonx.MarshalCanonical(e)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != released {
			t.Errorf("%s: evidence fingerprints as %s, released nodes as %s", name, encoded, released)
		}
	}
}
