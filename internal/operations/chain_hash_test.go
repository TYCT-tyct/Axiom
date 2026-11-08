package operations

import (
	"encoding/hex"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
)

// TestLinkHash pins the encoding a verifier outside Axiom recomputes: the hashes were computed apart from this code,
// with Python's json (compact, UTF-8) and hashlib, from the same fields; the time is written in UTC whatever its zone.
func TestLinkHash(t *testing.T) {
	site, actor := uuid.MustParse("019a0000-0000-7000-8000-0000000000bb"), "019a0000-0000-7000-8000-0000000000aa"
	e := audit.Entry{
		ID: uuid.MustParse("019a0000-0000-7000-8000-000000000001"), At: time.Date(2026, 11, 8, 9, 2, 3, 456789000, time.FixedZone("", 8*3600)),
		ActorKind: "operations_agent", ActorID: actor, Action: "price.publish", Module: "catalog", TargetKind: "model",
		TargetID: "m1", Site: &site, Reason: `say "why"`, Detail: []byte(`{"a": 1, "b": "é"}`),
	}
	first, err := linkHash(genesis, e)
	if err != nil {
		t.Fatal(err)
	}
	second, err := linkHash(first, e)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(first); got != "d3a2bb6a812484e7c97e4abcb758a4820b7b161701b5e44813f34bee6e30c7b9" {
		t.Errorf("after genesis: %s", got)
	}
	if got := hex.EncodeToString(second); got != "d7a728b389ac1d0958fde2c365ca3a121554ee2db2f2c7e5afaa72411aedaee5" {
		t.Errorf("after the first: %s", got)
	}
}
