package quality

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// identityVerdict is how one field of an answer compares with the claim.
type identityVerdict uint8

const (
	identityUnknown identityVerdict = iota
	identityMatched
	identityContradicted
)

// authorVerdict compares the answer's author with the catalog's: the claimed display name matches, another author's
// display name contradicts, and anything else says nothing.
func authorVerdict(answer string, claimed Claimed) identityVerdict {
	if containsName(answer, claimed.Author) {
		return identityMatched
	}
	if containsAnyName(answer, claimed.Others) {
		return identityContradicted
	}
	return identityUnknown
}

// modelVerdictAny compares an answer with every effective catalog name. A match wins only when no catalog name
// contradicts, and contradictory names are conclusive only when every non-empty claim contradicts. Conflicting or
// unfamiliar names therefore say nothing rather than turn a genuine answer into a fake.
func modelVerdictAny(answer string, claims ...string) identityVerdict {
	if answer == "" {
		return identityUnknown
	}
	var matched, contradicted, unknown bool
	for _, claim := range claims {
		if claim == "" {
			continue
		}
		switch modelVerdict(answer, claim) {
		case identityMatched:
			matched = true
		case identityContradicted:
			contradicted = true
		case identityUnknown:
			unknown = true
		}
	}
	switch {
	case matched && !contradicted:
		return identityMatched
	case contradicted && !matched && !unknown:
		return identityContradicted
	}
	return identityUnknown
}

// modelVerdict compares the model an answer names with a catalog model name. Equal keys (a date suffix or a recognized
// release number is not a different model) match. A different size or variant contradicts only when both names start
// with the same recognized family; an unknown alias or a different line says nothing.
func modelVerdict(answer, claim string) identityVerdict {
	if answer == "" || claim == "" {
		return identityUnknown
	}
	got, want := identityOf(answer), identityOf(claim)
	if got.key == "" || want.key == "" {
		return identityUnknown
	}
	if got.key == want.key {
		return identityMatched
	}
	if !got.sharesFamily(want) {
		return identityUnknown
	}
	if differentVariant(got, want) {
		return identityContradicted
	}
	return identityUnknown
}

// identity is a model name reduced to the part that identifies the model line and its variant: provider prefixes,
// dates and recognized release numbers are gone; size words, version numbers, and the leading family word remain.
type identity struct {
	key   string
	sizes map[string]struct{}
	line  string
}

// sharesFamily reports whether both names start with the same recognized model family. Without one, two names are not
// comparable: a suffix such as "mini" on an unknown alias is not evidence of a cheaper model of the claimed line.
func (id identity) sharesFamily(other identity) bool {
	return id.line != "" && id.line == other.line
}

// modelDate removes the date forms model names use: 2024-08-06, 20240806, 2024. A model line's date is a snapshot, not
// another model; a different size word or version number is not stripped.
var modelDate = regexp.MustCompile(`\b(19|20)\d{2}([-_.]?(0[1-9]|1[0-2])[-_.]?(0[1-9]|[12]\d|3[01]))?\b|\b\d{8}\b`)

// modelRelease removes a preview or release suffix and a month-year build: preview-06-05, 08-2024.
var modelRelease = regexp.MustCompile(`(?i)(preview|exp|experimental|rc|snapshot)[-_.]?\d{1,2}[-_.]?\d{1,2}|\b\d{1,2}[-_.](19|20)\d{2}\b`)

// identityOf normalizes a model name. It strips a provider prefix, a @date or :variant suffix, dates and recognized
// release numbers, then joins the remaining runs. Size words and the leading family word are kept separately so two
// lines of the same author can be told apart.
func identityOf(name string) identity {
	fields := nameRuns(cleanModelName(name))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if !dateField(f) {
			out = append(out, f)
		}
	}
	out = dropProvider(out)
	sizes := map[string]struct{}{}
	for _, f := range out {
		if modelSizes[f] || sizeField(f) {
			sizes[f] = struct{}{}
		}
	}
	line := ""
	if len(out) > 0 && modelFamily[out[0]] {
		line = out[0]
	}
	return identity{key: strings.Join(out, ""), sizes: sizes, line: line}
}

// cleanModelName lower-cases a name and removes a relay prefix, a @date or :variant suffix, a plus spelling and the
// date and release forms model names carry. It does not drop other suffixes: an unrecognized one must leave the name
// incomparable rather than make it equal to the base model.
func cleanModelName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:] // a relay's own prefix
	}
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		s = s[:i] // claude-opus-4-7@20260416
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		s = s[:i] // :free and similar provider variants
	}
	s = strings.ReplaceAll(s, "+", " plus ")
	s = modelRelease.ReplaceAllString(s, " ")
	return modelDate.ReplaceAllString(s, " ")
}

// dropProvider removes an organization word when a family word follows it.
func dropProvider(fields []string) []string {
	if len(fields) > 1 && providerPrefix[fields[0]] && modelFamily[fields[1]] {
		return fields[1:]
	}
	return fields
}

// dateField says a run is a date or build number its separators did not reveal: 0613, 0324, 002, 20241022. A
// three-digit build must start with zero; four digits must be a valid month and day. Anything else is an unrecognized
// suffix and stays in the name, so it cannot pass for the base model by accident.
func dateField(f string) bool {
	if !allDigits(f) {
		return false
	}
	switch len(f) {
	case 3:
		return f[0] == '0'
	case 4:
		month := int(f[0]-'0')*10 + int(f[1]-'0')
		day := int(f[2]-'0')*10 + int(f[3]-'0')
		return month >= 1 && month <= 12 && day >= 1 && day <= 31
	case 8:
		return true
	default:
		return false
	}
}

// sizeField reports a parameter-count run such as 7b, 70b or 405b.
func sizeField(f string) bool {
	return len(f) >= 2 && f[len(f)-1] == 'b' && allDigits(f[:len(f)-1])
}

// allDigits reports whether s is one or more decimal digits.
func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// differentVariant says the answer names a different size or variant of the claimed line. A claim with no size word
// only contradicts a smaller answer ("mini", "haiku", "flash"): a bare parameter count could be the same model's size,
// and a version number such as gpt-4.1 is unknown until a reference says whether it is a different line.
func differentVariant(got, want identity) bool {
	switch {
	case len(want.sizes) == 0:
		return anySmaller(got.sizes)
	case len(got.sizes) == 0:
		return false
	default:
		return !sameSizes(got.sizes, want.sizes)
	}
}

// anySmaller reports a variant that is clearly a cheaper tier.
func anySmaller(sizes map[string]struct{}) bool {
	for size := range sizes {
		if smallerVariant[size] {
			return true
		}
	}
	return false
}

// sameSizes reports whether two variant sets name the same tier.
func sameSizes(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for size := range a {
		if _, ok := b[size]; !ok {
			return false
		}
	}
	return true
}

// providerPrefix are organization words a model name may carry before its family.
var providerPrefix = map[string]bool{
	"openai": true, "anthropic": true, "google": true, "meta": true, "xai": true, "alibaba": true,
	"cohere": true, "ai21": true, "amazon": true, "azure": true, "bedrock": true, "vertex": true,
	"mistralai": true, "moonshot": true, "zhipu": true, "baidu": true, "bytedance": true, "tencent": true,
}

// modelFamily are the family words a model name may carry.
var modelFamily = map[string]bool{
	"gpt": true, "chatgpt": true, "o1": true, "o3": true, "o4": true, "claude": true, "gemini": true,
	"gemma": true, "llama": true, "qwen": true, "deepseek": true, "mistral": true, "mixtral": true,
	"codestral": true, "grok": true, "command": true, "phi": true, "yi": true, "glm": true, "kimi": true,
	"minimax": true, "ernie": true, "nova": true, "titan": true, "jamba": true, "reka": true, "sonar": true,
	"dbrx": true, "falcon": true, "mpt": true, "olmo": true, "granite": true, "nemotron": true, "hermes": true,
	"wizard": true, "vicuna": true, "zephyr": true, "palm": true, "bison": true, "gecko": true, "medlm": true,
	"learnlm": true,
}

// modelSizes are the words that distinguish variants of one line. They are kept, never treated as noise.
var modelSizes = map[string]bool{
	"mini": true, "nano": true, "micro": true, "tiny": true, "small": true, "lite": true, "light": true,
	"flash": true, "haiku": true, "sonnet": true, "opus": true, "pro": true, "max": true, "ultra": true,
	"plus": true, "turbo": true, "large": true, "medium": true, "xl": true, "2xl": true, "reasoner": true,
	"thinking": true, "r1": true, "r2": true,
}

// smallerVariant are the variants that are clearly a cheaper tier when the claim names no size.
var smallerVariant = map[string]bool{
	"mini": true, "nano": true, "micro": true, "tiny": true, "small": true, "lite": true, "light": true,
	"flash": true, "haiku": true,
}

// containsName reports whether s names n. Both sides are compared as runs of letters and digits, lower-cased, so
// "Mistral AI", "Mistral" and "mistral.ai" all name "Mistral AI", while "metadata" does not name "Meta". A run may
// also be a whole word of a longer name ("Meta" for "Meta Platforms, Inc."), a shortened form when it is at least five
// characters ("Mistral" for "Mistral AI"), and up to three adjacent runs are joined so a name split across punctuation
// or spacing still matches.
func containsName(s, name string) bool {
	wanted := nameRuns(name)
	want := strings.Join(wanted, "")
	if len(want) < 3 {
		return false
	}
	runs := nameRuns(s)
	for i := range runs {
		if runs[i] == want || len(runs[i]) >= 5 && strings.HasPrefix(want, runs[i]) {
			return true
		}
		if len(runs[i]) >= 3 && slices.Contains(wanted, runs[i]) {
			return true
		}
		for j, joined := i+1, runs[i]; j < len(runs) && j < i+3; j++ {
			joined += runs[j]
			if joined == want {
				return true
			}
		}
	}
	return false
}

func containsAnyName(s string, names []string) bool {
	for _, n := range names {
		if containsName(s, n) {
			return true
		}
	}
	return false
}

// nameRuns splits s into its lower-cased runs of letters and digits.
func nameRuns(s string) []string {
	var runs []string
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		if b.Len() > 0 {
			runs = append(runs, b.String())
			b.Reset()
		}
	}
	if b.Len() > 0 {
		runs = append(runs, b.String())
	}
	return runs
}
