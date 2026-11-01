package websearch

import (
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/secret"
)

func TestEngineKey(t *testing.T) {
	key := func(s string) *string { return &s }
	saved := Engine{URL: "https://search.example.com", Kind: KindSearXNG, Key: secret.New("saved")}
	for name, tc := range map[string]struct {
		settings EngineSettings
		url      string
		old      Engine
		found    bool
		kind     Kind
		want     string
		err      bool
	}{
		"same backend, key omitted":    {EngineSettings{}, saved.URL, saved, true, KindSearXNG, "saved", false},
		"same backend, key given":      {EngineSettings{Key: key("new")}, saved.URL, saved, true, KindSearXNG, "new", false},
		"same backend, no saved key":   {EngineSettings{}, saved.URL, Engine{URL: saved.URL, Kind: KindSearXNG}, true, KindSearXNG, "", false},
		"new kind, key omitted":        {EngineSettings{}, saved.URL, saved, true, KindFirecrawl, "", true},
		"new url, key omitted":         {EngineSettings{}, "https://other.example.com", saved, true, KindSearXNG, "", false},
		"new url, key given":           {EngineSettings{Key: key("new")}, "https://other.example.com", saved, true, KindSearXNG, "new", false},
		"new engine, searxng no key":   {EngineSettings{}, "https://other.example.com", Engine{}, false, KindSearXNG, "", false},
		"new engine, firecrawl no key": {EngineSettings{}, "https://api.firecrawl.dev/v2", Engine{}, false, KindFirecrawl, "", true},
		"cleared":                      {EngineSettings{Key: key("left")}, "https://search.example.com", saved, true, KindSearXNG, "left", false},
		"firecrawl explicit empty key": {EngineSettings{Key: key("")}, firecrawlBase, Engine{}, false, KindFirecrawl, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := engineKey(tc.settings, tc.url, tc.old, tc.found, tc.kind)
			if (err != nil) != tc.err {
				t.Fatalf("engineKey() error = %v, want an error: %v", err, tc.err)
			}
			if !tc.err && got != tc.want {
				t.Fatalf("engineKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrepareEngineZeroIDKeepsLegacyKey(t *testing.T) {
	legacy := Engine{URL: "https://search.example.com", Kind: KindSearXNG, Key: secret.New("legacy")}
	s := EngineSettings{
		ID: uuid.Nil().String(), Name: "searxng", Kind: "searxng", URL: legacy.URL,
		Enabled: true, Default: true, CostSearch: "0", CostResult: "0", Markup: "0",
	}
	e, err := prepareEngine(s, nil, []Engine{legacy})
	if err != nil {
		t.Fatalf("prepareEngine() error = %v", err)
	}
	if e.ID == uuid.Nil() {
		t.Fatal("a zero UUID was saved as the engine id")
	}
	if e.Key.Reveal() != "legacy" {
		t.Fatalf("key = %q, want the legacy key retained", e.Key.Reveal())
	}
}

func TestEnginePriceAddsEachResultAfterTheFirst(t *testing.T) {
	price := func(s string) (p platform.UnitPrice) {
		p, err := platform.ParseUnitPrice(s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	e := Engine{PriceSearch: price("0.005"), PriceResult: price("0.0125")}
	for _, tc := range []struct {
		results int
		want    string
	}{
		{1, "0.005"}, {3, "0.03"}, {5, "0.055"}, {0, "0.055"},
	} {
		got, err := e.Price(tc.results)
		if err != nil {
			t.Fatalf("Price(%d) error = %v", tc.results, err)
		}
		if got.String() != tc.want {
			t.Errorf("Price(%d) = %s, want %s", tc.results, got, tc.want)
		}
	}
	if got, err := (Engine{}).Price(5); err != nil || !got.IsZero() {
		t.Errorf("a zero published price = %s, %v; want zero and no error", got, err)
	}
}

func TestOpenEnginesRejectsAnUnusableList(t *testing.T) {
	client := egress.NewForTesting(egresstest.Loopback(t))
	e := func(name string) Engine {
		return Engine{
			Name: name, Kind: KindSearXNG, URL: "https://search.example.com", Enabled: true, Default: true,
			CostSearch: platform.UnitPrice{}, CostResult: platform.UnitPrice{}, Markup: "0",
			PriceSearch: platform.UnitPrice{}, PriceResult: platform.UnitPrice{},
		}
	}
	for name, tc := range map[string]struct {
		list []Engine
		want string
	}{
		"one default":           {[]Engine{e("one")}, ""},
		"no default":            {[]Engine{func() Engine { x := e("one"); x.Default = false; return x }()}, "want exactly one"},
		"two defaults":          {[]Engine{e("one"), e("two")}, "want exactly one"},
		"duplicate name":        {[]Engine{e("one"), e("one")}, "configured twice"},
		"native is reserved":    {[]Engine{e("native")}, "not a search engine name"},
		"unknown kind":          {[]Engine{func() Engine { x := e("one"); x.Kind = "other"; return x }()}, "not a search backend kind"},
		"firecrawl needs a key": {[]Engine{{Name: "one", Kind: KindFirecrawl, URL: firecrawlBase, Enabled: true, Default: true, Markup: "0"}}, "needs an API key"},
		"disabled is left out":  {[]Engine{{Name: "one", Kind: KindSearXNG, URL: "https://search.example.com", Enabled: false, Default: false}}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := OpenEngines(tc.list, client)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("OpenEngines() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("OpenEngines() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPrepareEnginePricesAtMarkup(t *testing.T) {
	key := "fc-key"
	s := EngineSettings{
		Name: "paid", Kind: "firecrawl", URL: "https://api.firecrawl.dev/v2", Key: &key,
		Enabled: true, Default: true, CostSearch: "0.005", CostResult: "0.0125", Markup: "0.2",
	}
	e, err := prepareEngine(s, nil, nil)
	if err != nil {
		t.Fatalf("prepareEngine() error = %v", err)
	}
	if e.PriceSearch.String() != "0.006" || e.PriceResult.String() != "0.015" {
		t.Fatalf("published price = %s / %s, want 0.006 / 0.015", e.PriceSearch, e.PriceResult)
	}
	if e.PriceVersion != 1 {
		t.Fatalf("price version = %d, want 1", e.PriceVersion)
	}
	s.ID = e.ID.String()
	previous := map[uuid.UUID]Engine{e.ID: e}
	again, err := prepareEngine(s, previous, nil)
	if err != nil || again.PriceVersion != 1 {
		t.Fatalf("an unchanged save = %d, %v; want the same price version", again.PriceVersion, err)
	}
	s.CostSearch = "0.006"
	raised, err := prepareEngine(s, previous, nil)
	if err != nil {
		t.Fatal(err)
	}
	if raised.PriceVersion != 2 || raised.PriceSearch.String() != "0.0072" {
		t.Fatalf("a cost change = %d / %s, want version 2 at 0.0072", raised.PriceVersion, raised.PriceSearch)
	}
}
