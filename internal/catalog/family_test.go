package catalog

import "testing"

// TestFamilyOf: an imported model's family is its name without the author, a date in any form hasDate knows, -latest or
// :free; a name that is no family's form gives none.
func TestFamilyOf(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"openai/gpt-6.1":                 "gpt-6.1",
		"gpt-4o-2024-08-06":              "gpt-4o",
		"claude-3-5-sonnet-20241022":     "claude-3-5-sonnet",
		"gpt-4-0613":                     "gpt-4",
		"gemini-2.5-pro-preview-06-05":   "gemini-2.5-pro-preview",
		"meta-llama/llama-3-8b:free":     "llama-3-8b",
		"anthropic/claude-sonnet-latest": "claude-sonnet",
		"Mistral_Large":                  "",
		"deepseek/":                      "",
	} {
		got := ""
		if f := familyOf(name); f != nil {
			got = *f
		}
		if got != want {
			t.Errorf("familyOf(%q) = %q, want %q", name, got, want)
		}
	}
}
