package idem_test

import (
	"os"
	"testing"

	"axiom/internal/platform/idem"
	"axiom/internal/platform/jsonx"
)

// sameRequest is a pair of request bodies and whether one key may carry both. testdata/same-request.json is shared with
// the admin's end-to-end mock (web/e2e/fingerprint.spec.ts), so the mock's idea of the same request cannot drift from
// the server's. A case with an operation is fingerprinted by that handler's own identity and is checked in cmd/axiom.
type sameRequest struct {
	Case      string `json:"case"`
	Operation string `json:"operation"`
	First     string `json:"first"`
	Again     string `json:"again"`
	Same      bool   `json:"same"`
}

func TestSameRequest(t *testing.T) {
	data, err := os.ReadFile("testdata/same-request.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []sameRequest
	if err := jsonx.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.Operation != "" {
			continue
		}
		first, err := idem.Fingerprint(jsonx.Raw(c.First))
		if err != nil {
			t.Fatal(err)
		}
		again, err := idem.Fingerprint(jsonx.Raw(c.Again))
		if err != nil {
			t.Fatal(err)
		}
		if (first == again) != c.Same {
			t.Errorf("%s: same request is %t, want %t", c.Case, first == again, c.Same)
		}
	}
}
