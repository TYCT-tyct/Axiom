package catalog_test

import (
	"testing"

	"axiom/internal/catalog"
)

func TestNormalize(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"claude-sonnet-4-5-20250929":   "claude-sonnet-4-5-20250929",
		"Claude-Sonnet-4.5":            "claude-sonnet-4-5",
		"  GPT_4o  ":                   "gpt-4o",
		"anthropic / Claude  Sonnet":   "anthropic/claude-sonnet",
		"openai/-gpt--4":               "openai/gpt-4",
		"gpt-4o-2024-08-06":            "gpt-4o-20240806",
		"gemini-2.5-pro-preview-06-05": "gemini-2-5-pro-preview-06-05",
		"gemini-2.5-pro-preview-0605":  "gemini-2-5-pro-preview-0605",
		"model-2024-08-06-x":           "model-2024-08-06-x",
		"model-24-08-06":               "model-24-08-06",
		"":                             "",
		"Ünï-Çode":                     "Ünï-Çode", // only ASCII letters are folded
	} {
		if got := catalog.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzNormalize: digits are never added, removed or reordered, so versions that differ in a digit stay apart, and
// normalizing twice changes nothing more.
func FuzzNormalize(f *testing.F) {
	for _, seed := range []string{"gpt-4o-2024-08-06", "Claude 3.5 Sonnet", "a//b", "-", "x.-_ y"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got := catalog.Normalize(name)
		if a, b := digitsOf(name), digitsOf(got); a != b {
			t.Fatalf("Normalize(%q) = %q: digits %q became %q", name, got, a, b)
		}
		if again := catalog.Normalize(got); again != got {
			t.Fatalf("Normalize(%q) = %q, then %q", name, got, again)
		}
	})
}

func digitsOf(s string) string {
	var d []byte
	for i := range len(s) {
		if s[i] >= '0' && s[i] <= '9' {
			d = append(d, s[i])
		}
	}
	return string(d)
}
