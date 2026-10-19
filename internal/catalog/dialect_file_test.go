package catalog

import (
	"os"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
)

// dialectFile is the data file a release applies (docs/ops/release.md).
const dialectFile = "../../deploy/catalog/dialects.json"

func readDialectFile(t *testing.T) DialectFile {
	t.Helper()
	data, err := os.ReadFile(dialectFile)
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseDialectFile(data)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

// specOfAModelThatReasons is the spec the catalog would save for a model with these keys and the abilities of a model
// that reasons.
func specOfAModelThatReasons(t *testing.T, keys DialectKeys) spec {
	t.Helper()
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
	return s
}

// TestTheDialectFileAnswersWhatTheCheckAsks: the file is read by the program that applies it, and a model it covers,
// given the abilities of a model that reasons, has every key `axiom catalog check` asks of its vendor's models and a spec
// the catalog accepts when it is saved. A model added to the file without a key the check wants fails here, not at a
// release.
func TestTheDialectFileAnswersWhatTheCheckAsks(t *testing.T) {
	t.Parallel()
	file := readDialectFile(t)
	if len(file.Models) == 0 {
		t.Fatal("the file covers no model")
	}
	for slug, keys := range file.Models {
		t.Run(slug, func(t *testing.T) {
			t.Parallel()
			author, _, _ := strings.Cut(slug, "/")
			if missing := dialectProblems(author, specOfAModelThatReasons(t, keys)); len(missing) > 0 {
				t.Errorf("the file leaves out %v", missing)
			}
		})
	}
}

// TestTheDialectFileCoversOnlyVendorsTheCheckAsksOf: a model of another vendor has no key the check asks for, so the file
// has nothing to say of it.
func TestTheDialectFileCoversOnlyVendorsTheCheckAsksOf(t *testing.T) {
	t.Parallel()
	for slug := range readDialectFile(t).Models {
		if author, _, _ := strings.Cut(slug, "/"); author != "anthropic" && author != "openai" {
			t.Errorf("%s: author %q has no dialect keys the check asks for", slug, author)
		}
	}
}
