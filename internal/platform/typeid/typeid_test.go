package typeid_test

import (
	"embed"
	"errors"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/typeid"
)

// The vectors in testdata are the TypeID specification's own, version 0.3.0
// (https://github.com/jetify-com/typeid/tree/main/spec, valid.json and invalid.json at commit cb20c6e, fetched
// 2026-10-15), unchanged.
//
//go:embed testdata
var testdata embed.FS

type vector struct {
	TypeID string `json:"typeid"`
	Prefix string `json:"prefix"`
	UUID   string `json:"uuid"`
}

func vectors(t *testing.T, file string) []vector {
	t.Helper()
	data, err := testdata.ReadFile("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var all []vector
	if err := jsonx.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	return all
}

// TestSpecValid: every valid vector decodes to its UUID under its prefix, and the UUID encodes back to the vector.
func TestSpecValid(t *testing.T) {
	t.Parallel()
	for _, v := range vectors(t, "valid.json") {
		prefix, err := typeid.ParsePrefix(v.Prefix)
		if err != nil {
			t.Fatalf("%q: %v", v.TypeID, err)
		}
		want := uuid.MustParse(v.UUID)
		if got, err := prefix.Decode(v.TypeID); err != nil || got != want {
			t.Errorf("Decode(%q) = %v, %v; want %v", v.TypeID, got, err, want)
		}
		if got := prefix.Encode(want); got != v.TypeID {
			t.Errorf("Encode(%q, %v) = %q; want %q", v.Prefix, want, got, v.TypeID)
		}
	}
}

// split cuts s where an expected prefix would end: at its last underscore, which is the separator in every
// well-formed TypeID.
func split(s string) string {
	if prefix, _, found := strings.CutLast(s, "_"); found {
		return prefix
	}
	return ""
}

// TestSpecInvalid: no invalid vector decodes under the prefix it carries. Either that prefix is not a prefix, or
// the rest of the string is refused.
func TestSpecInvalid(t *testing.T) {
	t.Parallel()
	for _, v := range vectors(t, "invalid.json") {
		prefix, err := typeid.ParsePrefix(split(v.TypeID))
		if err == nil {
			_, err = prefix.Decode(v.TypeID)
		}
		if err == nil {
			t.Errorf("%q is accepted", v.TypeID)
		}
	}
}

func TestParsePrefix(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		prefix string
		valid  bool
	}{
		{"", true}, // the spec allows no prefix; the modules define theirs
		{"a", true},
		{"site", true},
		{"my_type", true},
		{"my__type", true},
		{strings.Repeat("a", 63), true},
		{strings.Repeat("a", 64), false},
		{"_a", false},
		{"a_", false},
		{"_", false},
		{"Site", false},
		{"site1", false},
		{"si1te", false},
		{"siTe", false},
		{"si-te", false},
		{"sité", false},
		{" site", false},
	} {
		_, err := typeid.ParsePrefix(tt.prefix)
		if (err == nil) != tt.valid {
			t.Errorf("ParsePrefix(%q) = %v; valid should be %v", tt.prefix, err, tt.valid)
		}
	}
}

func TestMustPrefixPanicsOnInvalid(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("MustPrefix(\"Site\") did not panic")
		}
	}()
	typeid.MustPrefix("Site")
}

// TestDecodeRefuses: the cases a router meets besides the spec's vectors: another entity's ID, a prefix that only
// starts like the expected one, and suffixes at the edge of the 128-bit limit.
func TestDecodeRefuses(t *testing.T) {
	t.Parallel()
	const suffix = "01h455vb4pex5vsknk084sn02q"
	site := typeid.MustPrefix("site")
	for name, s := range map[string]string{
		"another prefix":       "acct_" + suffix,
		"longer prefix":        "sites_" + suffix,
		"shorter prefix":       "sit_" + suffix,
		"no prefix":            suffix,
		"no separator":         "site" + suffix + "x",
		"wrong separator":      "site-" + suffix,
		"trailing character":   "site_" + suffix + "0",
		"upper case":           "site_" + strings.ToUpper(suffix),
		"first character 8":    "site_80000000000000000000000000",
		"first character z":    "site_z0000000000000000000000000",
		"NUL in the suffix":    "site_0000000000000000000000000\x00",
		"suffix of 25":         "site_" + suffix[1:],
		"multi-byte character": "site_0000000000000000000000000é",
	} {
		if _, err := site.Decode(s); !errors.Is(err, typeid.ErrInvalid) {
			t.Errorf("%s: Decode(%q) = %v; want ErrInvalid", name, s, err)
		}
	}
	if got, err := site.Decode("site_7zzzzzzzzzzzzzzzzzzzzzzzzz"); err != nil || got != uuid.Max() {
		t.Errorf("the largest suffix = %v, %v; want %v", got, err, uuid.Max())
	}
}

// FuzzRoundTrip: whatever UUID and valid prefix, Decode undoes Encode.
func FuzzRoundTrip(f *testing.F) {
	f.Add("site", make([]byte, 16))
	f.Add("", []byte("0123456789abcdef"))
	f.Add("my__type", []byte("\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff"))
	f.Fuzz(func(t *testing.T, prefix string, raw []byte) {
		p, err := typeid.ParsePrefix(prefix)
		if err != nil || len(raw) != len(uuid.UUID{}) {
			t.Skip()
		}
		id := uuid.UUID(raw)
		s := p.Encode(id)
		if got, err := p.Decode(s); err != nil || got != id {
			t.Fatalf("Decode(%q) = %v, %v; want %v", s, got, err, id)
		}
	})
}

// FuzzDecode: Decode accepts only the one canonical spelling of each ID, so that two strings never name the same
// entity: whatever it accepts, Encode writes back unchanged.
func FuzzDecode(f *testing.F) {
	f.Add("site", "site_01h455vb4pex5vsknk084sn02q")
	f.Add("", "7zzzzzzzzzzzzzzzzzzzzzzzzz")
	f.Add("site", "site_01H455VB4PEX5VSKNK084SN02Q")
	f.Add("site", "site_8zzzzzzzzzzzzzzzzzzzzzzzzz")
	f.Fuzz(func(t *testing.T, prefix, s string) {
		p, err := typeid.ParsePrefix(prefix)
		if err != nil {
			t.Skip()
		}
		id, err := p.Decode(s)
		if err != nil {
			t.Skip()
		}
		if back := p.Encode(id); back != s {
			t.Fatalf("Decode(%q) = %v, which encodes as %q", s, id, back)
		}
	})
}

// The request path decodes an ID from every URL that has one, so Decode must not allocate.
func BenchmarkDecode(b *testing.B) {
	site := typeid.MustPrefix("site")
	s := site.Encode(uuid.NewV7())
	b.ReportAllocs()
	for b.Loop() {
		if _, err := site.Decode(s); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncode(b *testing.B) {
	site := typeid.MustPrefix("site")
	id := uuid.NewV7()
	b.ReportAllocs()
	for b.Loop() {
		_ = site.Encode(id)
	}
}
