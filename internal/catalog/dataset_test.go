package catalog

import (
	"embed"
	"errors"
	"slices"
	"strings"
	"testing"
)

// The samples in testdata are trimmed copies of LiteLLM's model_prices_and_context_window.json (BerriAI/litellm, main)
// and OpenRouter's /api/v1/models, both fetched 2026-10-15, with the entries of a few models and some invented
// entries (named *-bad-*) whose numbers a parser must refuse.
//
//go:embed testdata
var samples embed.FS

func sample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := samples.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func entryOf(t *testing.T, entries []Entry, key string) Entry {
	t.Helper()
	i := slices.IndexFunc(entries, func(e Entry) bool { return e.Key == key })
	if i < 0 {
		t.Fatalf("no entry %q in %v", key, keysOf(entries))
	}
	return entries[i]
}

func keysOf(entries []Entry) []string {
	var keys []string
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	return keys
}

func scheduleOf(t *testing.T, e Entry, current *Schedule) string {
	t.Helper()
	s, err := e.schedule(current)
	if err != nil {
		t.Fatalf("%s: %v", e.Key, err)
	}
	b, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// geminiCurrent is a stored price with Google's one threshold and basis (README.md「条件」).
func geminiCurrent(t *testing.T) *Schedule {
	t.Helper()
	s, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.00000125","output":"0.00001"},
		"context_tiers":{"basis":["input","cache_read","audio_in","image_in"],"method":"whole","tiers":[{"above":200000,"rates":{"input":"0.0000025"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	return &s
}

func TestParseLiteLLM(t *testing.T) {
	t.Parallel()
	entries, err := ParseLiteLLM(sample(t, "litellm.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Only the vendors' own text models: not sample_spec, a cloud's copy of a model (vertex_ai), or an image model.
	want := []string{
		"claude-haiku-5-5", "claude-sonnet-4-5", "claude-sonnet-4-5-20250929", "deepseek-chat", "deepseek/deepseek-chat",
		"gemini/gemini-2.5-pro", "gpt-4o", "gpt-4o-2024-08-06", "gpt-bad-decimals", "gpt-bad-exponent", "gpt-bad-negative",
	}
	if got := keysOf(entries); !slices.Equal(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for key, want := range map[string]string{
		// priority is a service tier with its own prices; the _batches prices are not read.
		"gpt-4o": `{"currency":"USD","rates":{"cache_read":"0.00000125","input":"0.0000025","output":"0.00001"},` +
			`"service_tiers":{"priority":{"rates":{"cache_read":"0.000002125","input":"0.00000425","output":"0.000017"}}},"v":1}`,
		// the 1-hour write is its own item and a search costs one price whatever the context size
		"claude-sonnet-4-5-20250929": `{"currency":"USD","rates":{"cache_read":"0.0000003","cache_write":"0.00000375","cache_write_1h":"0.000006",` +
			`"input":"0.000003","output":"0.000015","web_search":"0.01"},"v":1}`,
		// 2.8e-08 is read from its digits; the duplicate spelling input_cost_per_token_cache_hit is not a second price
		"deepseek/deepseek-chat": `{"currency":"USD","rates":{"cache_read":"0.000000028","cache_write":"0","input":"0.00000028","output":"0.00000042"},"v":1}`,
	} {
		if got := scheduleOf(t, entryOf(t, entries, key), nil); got != want {
			t.Errorf("%s:\n got %s\nwant %s", key, got, want)
		}
	}
	for _, key := range []string{"gpt-bad-negative", "gpt-bad-exponent", "gpt-bad-decimals"} {
		if e := entryOf(t, entries, key); e.Err == nil {
			t.Errorf("%s: no error", key)
		}
	}
	if e := entryOf(t, entries, "gemini/gemini-2.5-pro"); !slices.Equal(e.Names, []string{"gemini-2.5-pro"}) || !e.Text {
		t.Errorf("gemini names %v text %v", e.Names, e.Text)
	}
}

// TestLiteLLMTiers: the thresholds come with the entry but the basis does not, so tiers are taken only against a
// current price with the same thresholds, and then in every service tier at the same thresholds.
func TestLiteLLMTiers(t *testing.T) {
	t.Parallel()
	entries, err := ParseLiteLLM(sample(t, "litellm.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := entryOf(t, entries, "gemini/gemini-2.5-pro")
	if _, err := e.schedule(nil); !errors.Is(err, ErrDatasetTiers) {
		t.Errorf("without a current price: %v", err)
	}
	other, err := ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"1"},"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":100000,"rates":{}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.schedule(&other); !errors.Is(err, ErrDatasetTiers) {
		t.Errorf("with other thresholds: %v", err)
	}
	got := scheduleOf(t, e, geminiCurrent(t))
	for _, want := range []string{
		`"context_tiers":{"basis":["input","cache_read","audio_in","image_in"],"method":"whole","tiers":[{"above":200000,"rates":{"cache_read":"0.00000025","input":"0.0000025","output":"0.000015"}}]}`,
		`"service_tiers":{"flex":{"context_tiers":[{"above":200000,"rates":{}}],"rates":{"cache_read":"0.000000125","input":"0.000000625","output":"0.000005"}},` +
			`"priority":{"context_tiers":[{"above":200000,"rates":{"cache_read":"0.00000045","input":"0.0000045","output":"0.000027"}}],` +
			`"rates":{"cache_read":"0.000000225","input":"0.00000225","output":"0.000018"}}}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("schedule lacks %s:\n%s", want, got)
		}
	}
}

func TestParseOpenRouter(t *testing.T) {
	t.Parallel()
	entries, err := ParseOpenRouter(sample(t, "openrouter.json"))
	if err != nil {
		t.Fatal(err)
	}
	// :batch is a supplier's alias for the model, not another model.
	for _, e := range entries {
		if strings.Contains(e.Key, ":") {
			t.Errorf("entry %q is a variant", e.Key)
		}
	}
	gpt := entryOf(t, entries, "openai/gpt-4o")
	if got := scheduleOf(t, gpt, nil); got != `{"currency":"USD","rates":{"cache_read":"0.00000125","input":"0.0000025","output":"0.00001"},"v":1}` {
		t.Errorf("gpt-4o: %s", got)
	}
	// both names, for the dated one is the version evidence
	if sonnet := entryOf(t, entries, "anthropic/claude-sonnet-4.5"); !slices.Equal(sonnet.Names, []string{"anthropic/claude-sonnet-4.5", "anthropic/claude-4.5-sonnet-20250929"}) {
		t.Errorf("names %v", sonnet.Names)
	}
	if !gpt.Text || entryOf(t, entries, "google/gemini-nano-banana-2.1").Text {
		t.Error("an image-output model is not a text model")
	}
}

func TestParseOpenRouterRefuses(t *testing.T) {
	t.Parallel()
	entries, err := ParseOpenRouter(sample(t, "openrouter.json"))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"anthropic/claude-sonnet-4.5": errOverrides.Error(), // tiers whose basis the dataset does not say
		"openai/gpt-bad-negative":     "not a sign",
		// 0.0000000416666666666667 has 16 decimals: refused, not rounded
		"google/gemini-3.6-flash": "15 decimals",
	} {
		if e := entryOf(t, entries, key); e.Err == nil || !strings.Contains(e.Err.Error(), want) {
			t.Errorf("%s: err %v, want %q", key, e.Err, want)
		}
	}
}

func TestParseDatasetRejects(t *testing.T) {
	t.Parallel()
	for name, parse := range map[string]func([]byte) ([]Entry, error){"litellm": ParseLiteLLM, "openrouter": ParseOpenRouter} {
		for _, body := range []string{``, `[]`, `{}`, `{"data":[]}`, `{"a":1,"a":2}`, `not json`} {
			if entries, err := parse([]byte(body)); err == nil {
				t.Errorf("%s accepted %q: %v", name, body, entries)
			}
		}
	}
}

// TestPickEntry: LiteLLM keeps DeepSeek's chat model under two keys, one of which also prices the cache write (at zero).
// The entry that says everything the other says is the model's; entries that differ in a price are ambiguous.
func TestPickEntry(t *testing.T) {
	t.Parallel()
	entries, err := ParseLiteLLM(sample(t, "litellm.json"))
	if err != nil {
		t.Fatal(err)
	}
	index := indexEntries(entries)
	floating := func(names ...string) syncModel {
		m := syncModel{row: CatalogModel{VersionBasis: string(VersionFloating)}}
		for _, n := range names {
			m.names = append(m.names, CatalogName{Name: n, Norm: Normalize(n)})
		}
		return m
	}
	if e, err := pickEntry(index, floating("deepseek-chat")); err != nil || e.Key != "deepseek/deepseek-chat" {
		t.Errorf("deepseek-chat: %v, %v", e, err)
	}
	bad := floating("gpt-bad-negative", "gpt-4o-2024-08-06")
	if _, err := pickEntry(index, bad); err == nil || !strings.Contains(err.Error(), "different prices") {
		t.Errorf("an unusable entry beside a usable one: %v", err)
	}
}
