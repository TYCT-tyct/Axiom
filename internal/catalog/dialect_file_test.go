package catalog

import (
	"os"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
)

// dialectFile is the data file a release applies (docs/ops/release.md).
const dialectFile = "../../deploy/catalog/dialects.json"

// TestTheDialectFileAnswersWhatTheCheckAsks: the file is read by the program that applies it, and a model it covers,
// given the abilities of a model that reasons, has every key `axiom catalog check` asks of its vendor's models and a spec
// the catalog accepts when it is saved. A model added to the file without a key the check wants fails here, not at a
// release.
func TestTheDialectFileAnswersWhatTheCheckAsks(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(dialectFile)
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseDialectFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Models) == 0 {
		t.Fatal("the file covers no model")
	}
	for slug, keys := range file.Models {
		t.Run(slug, func(t *testing.T) {
			t.Parallel()
			members, err := keys.members()
			if err != nil {
				t.Fatal(err)
			}
			members["abilities"] = jsonx.Raw(`["reasoning"]`)
			raw, err := jsonx.MarshalCanonical(members)
			if err != nil {
				t.Fatal(err)
			}
			s, err := parseSavedSpec(raw)
			if err != nil {
				t.Fatalf("the catalog would refuse the spec: %v", err)
			}
			author, _, _ := strings.Cut(slug, "/")
			if author != "anthropic" && author != "openai" {
				t.Fatalf("author %q has no dialect keys the check asks for", author)
			}
			if missing := dialectProblems(author, s); len(missing) > 0 {
				t.Errorf("the file leaves out %v", missing)
			}
		})
	}
}
