package quality

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// SourceClass is one administrator-defined class of endpoint supply, with the fingerprint rules that recognize it
// (docs/domains/quality.md「来源类别与指纹」). Slug is the stable name presets and call records use; Name is what the admin
// shows. The seeded classes are official, cloud and relay; relay is the fallback.
type SourceClass struct {
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	NameZh      string    `json:"name_zh"`
	Description string    `json:"description"`
	Position    int       `json:"position"`
	Fallback    bool      `json:"fallback"`
	System      bool      `json:"system"`
	Rules       Rules     `json:"rules"`
	Revision    int64     `json:"revision"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Rules are one class's recognition conditions: all of them or any one of them must hold. A class with no conditions
// never matches, except the fallback class, which is chosen when no class matches.
type Rules struct {
	Match      string      `json:"match"`
	Conditions []Condition `json:"conditions"`
}

// Condition is one fingerprint check over response evidence. Evidence is "header" (then Name names the header) or
// "usage" (then Item names the billing item). The operators are listed in validOperators.
type Condition struct {
	Evidence string `json:"evidence"`
	Name     string `json:"name,omitempty"`
	Item     string `json:"item,omitempty"`
	Operator string `json:"operator"`
	Value    string `json:"value,omitempty"`
}

// The bounds of the class data. They keep every endpoint's fingerprint evidence at a fixed size and stop the rules
// from becoming a query language.
const (
	MaxSourceClasses  = 32
	MaxConditions     = 8
	MaxFingerprintKey = 32 // distinct response headers referenced by all classes together
	maxName           = 100
	maxDescription    = 500
	maxConditionValue = 256
	maxPosition       = 1_000_000
)

// DefaultSourceClasses are the seeds the migration writes: official direct APIs, cloud hosting, and relays. They have
// no rules, so every endpoint is relay until an administrator adds fingerprints, exactly the behaviour before classes
// became data. Relay is the fallback and therefore has no rules of its own. Production starts with relay only and reads
// the migration's rows at Start; this function is the in-memory test fixture and the shape the migration must keep.
func DefaultSourceClasses() []SourceClass {
	return []SourceClass{
		{Slug: "official", Name: "Official", NameZh: "官方直连", Position: 0, System: true, Rules: Rules{Match: "all"}},
		{Slug: "cloud", Name: "Cloud", NameZh: "云厂商", Position: 10, System: true, Rules: Rules{Match: "all"}},
		{Slug: "relay", Name: "Relay", NameZh: "中转站", Position: 1000, System: true, Fallback: true, Rules: Rules{Match: "all"}},
	}
}

// ClassError is a rule or class field an administrator must fix; Field is where, for the API's 400.
type ClassError struct{ Field, Reason string }

func (e *ClassError) Error() string { return "source class: " + e.Field + ": " + e.Reason }

// The refusals a class write can meet beyond a bad field.
var (
	ErrClassNotFound = errors.New("quality: no such source class")
	ErrClassExists   = errors.New("quality: that slug is already taken")
	ErrClassStale    = errors.New("quality: the source class changed since it was read")
	ErrClassSystem   = errors.New("quality: a seeded source class cannot be deleted")
)

func classErr(field, reason string) error { return &ClassError{Field: field, Reason: reason} }

// Check validates a class as stored or about to be stored. The database constraints repeat the important ones so a
// row written by another path cannot slip through; this function is what gives the API a field to name.
func (c SourceClass) Check() error {
	if !validSlug(c.Slug) {
		return classErr("slug", "must start with a lower-case letter, then lower-case letters, digits or hyphens, at most 32 characters")
	}
	if utf8.RuneCountInString(c.Name) < 1 || utf8.RuneCountInString(c.Name) > maxName {
		return classErr("name", "must be 1 to 100 characters")
	}
	if utf8.RuneCountInString(c.NameZh) > maxName {
		return classErr("name_zh", "must be at most 100 characters")
	}
	if utf8.RuneCountInString(c.Description) > maxDescription {
		return classErr("description", "must be at most 500 characters")
	}
	if c.Position < 0 || c.Position > maxPosition {
		return classErr("position", "must be 0 to 1000000")
	}
	if c.Fallback && len(c.Rules.Conditions) != 0 {
		return classErr("rules", "the fallback class recognizes the endpoints no rule matches and has no conditions")
	}
	return c.Rules.Check()
}

// Normalized returns the rules with header names in canonical form, which is how they are stored and shown. A header
// name is case-insensitive, so two administrators writing X-Pool and x-pool must mean the same rule.
func (r Rules) Normalized() Rules {
	out := r
	out.Conditions = make([]Condition, len(r.Conditions))
	copy(out.Conditions, r.Conditions)
	for i := range out.Conditions {
		if out.Conditions[i].Evidence == "header" {
			out.Conditions[i].Name = textproto.CanonicalMIMEHeaderKey(out.Conditions[i].Name)
		}
	}
	return out
}

// Check validates the rule list itself.
func (r Rules) Check() error {
	switch r.Match {
	case "all", "any":
	case "":
		if len(r.Conditions) != 0 {
			return classErr("rules/match", "must be all or any")
		}
	default:
		return classErr("rules/match", "must be all or any")
	}
	if len(r.Conditions) > MaxConditions {
		return classErr("rules/conditions", fmt.Sprintf("must be at most %d conditions", MaxConditions))
	}
	for i, c := range r.Conditions {
		if err := c.check(fmt.Sprintf("rules/conditions/%d", i)); err != nil {
			return err
		}
	}
	return nil
}

func (c Condition) check(prefix string) error {
	switch c.Evidence {
	case "header":
		return c.checkHeader(prefix)
	case "usage":
		return c.checkUsage(prefix)
	default:
		return classErr(prefix+"/evidence", "must be header or usage")
	}
}

func (c Condition) checkHeader(prefix string) error {
	if c.Item != "" {
		return classErr(prefix+"/item", "only usage conditions name an item")
	}
	if c.Name == "" || !httpguts.ValidHeaderFieldName(c.Name) {
		return classErr(prefix+"/name", "must be a valid response header name")
	}
	switch c.Operator {
	case "present", "absent":
		if c.Value != "" {
			return classErr(prefix+"/value", "must be empty for present or absent")
		}
	case "equals", "prefix", "suffix", "contains":
		if c.Value == "" || len(c.Value) > maxConditionValue {
			return classErr(prefix+"/value", "must be 1 to 256 bytes")
		}
	default:
		return classErr(prefix+"/operator", "must be present, absent, equals, prefix, suffix or contains")
	}
	return nil
}

func (c Condition) checkUsage(prefix string) error {
	if c.Name != "" {
		return classErr(prefix+"/name", "only header conditions name a header")
	}
	if _, err := catalog.ParseItem(c.Item); err != nil {
		return classErr(prefix+"/item", "must be a billing item name")
	}
	switch c.Operator {
	case "present", "absent":
		if c.Value != "" {
			return classErr(prefix+"/value", "must be empty for present or absent")
		}
	default:
		return classErr(prefix+"/operator", "must be present or absent for a usage condition")
	}
	return nil
}

func validSlug(s string) bool {
	if len(s) < 1 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s[1:] {
		if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// compiledClasses is the immutable rule set the call path reads: classes in evaluation order, the fallback, and the
// header names whose values are kept as evidence. Version changes when anything that affects matching or display does,
// so a reload can tell whether it has work to do.
type compiledClasses struct {
	version  string
	classes  []compiledClass
	fallback string
	headers  []string
}

type compiledClass struct {
	slug       string
	all        bool
	conditions []compiledCondition
}

type compiledCondition struct {
	header string
	item   catalog.Item
	op     string
	value  string
}

// compileClasses validates and freezes the administrator's classes for the call path. The fallback must exist exactly
// once and come last; a class that is not the fallback with no conditions simply never matches.
func compileClasses(classes []SourceClass) (*compiledClasses, error) {
	ordered, err := orderedClasses(classes)
	if err != nil {
		return nil, err
	}
	fallbackAt, err := checkClassOrder(ordered)
	if err != nil {
		return nil, err
	}
	out, names, err := compileOrdered(ordered, fallbackAt)
	if err != nil {
		return nil, err
	}
	if len(names) > MaxFingerprintKey {
		return nil, classErr("rules", fmt.Sprintf("the classes together may read at most %d different response headers", MaxFingerprintKey))
	}
	for name := range names {
		out.headers = append(out.headers, name)
	}
	slices.Sort(out.headers)
	data, err := jsonx.MarshalCanonical(ordered)
	if err != nil {
		return nil, fmt.Errorf("quality: encode the source classes: %w", err)
	}
	out.version = fmt.Sprintf("%x", sha256.Sum256(data))
	return out, nil
}

// orderedClasses bounds and sorts the administrator's list; every later check reads this order.
func orderedClasses(classes []SourceClass) ([]SourceClass, error) {
	if len(classes) == 0 {
		return nil, classErr("classes", "at least the fallback class is required")
	}
	if len(classes) > MaxSourceClasses {
		return nil, classErr("classes", fmt.Sprintf("must be at most %d classes", MaxSourceClasses))
	}
	ordered := slices.Clone(classes)
	slices.SortFunc(ordered, func(a, b SourceClass) int {
		if a.Position != b.Position {
			return a.Position - b.Position
		}
		return strings.Compare(a.Slug, b.Slug)
	})
	return ordered, nil
}

// checkClassOrder validates each class and the one fallback's place; it returns the fallback's index.
func checkClassOrder(ordered []SourceClass) (int, error) {
	seen := map[string]bool{}
	fallbacks := 0
	fallbackAt := -1
	for i, c := range ordered {
		if err := checkClass(c, i, ordered, seen); err != nil {
			return 0, err
		}
		if c.Fallback {
			fallbacks++
			if fallbacks > 1 {
				return 0, classErr("fallback", "only one class can be the fallback")
			}
			fallbackAt = i
		}
	}
	if fallbacks != 1 {
		return 0, classErr("fallback", "exactly one class must be the fallback")
	}
	if fallbackAt != len(ordered)-1 {
		return 0, classErr("position", "the fallback must come after every other class")
	}
	return fallbackAt, nil
}

func checkClass(c SourceClass, i int, ordered []SourceClass, seen map[string]bool) error {
	if err := c.Check(); err != nil {
		return err
	}
	if i > 0 && ordered[i-1].Position == c.Position {
		return classErr("position", "is used twice")
	}
	if seen[c.Slug] {
		return classErr("slug", "is used twice")
	}
	seen[c.Slug] = true
	return nil
}

// compileOrdered compiles every class but the fallback; names collects the headers the rules read as evidence.
func compileOrdered(ordered []SourceClass, fallbackAt int) (*compiledClasses, map[string]bool, error) {
	out := &compiledClasses{}
	names := map[string]bool{}
	for i, c := range ordered {
		if i == fallbackAt {
			out.fallback = c.Slug
			continue
		}
		compiled, err := compileClass(c, names)
		if err != nil {
			return nil, nil, err
		}
		out.classes = append(out.classes, compiled)
	}
	return out, names, nil
}

func compileClass(c SourceClass, names map[string]bool) (compiledClass, error) {
	compiled := compiledClass{slug: c.Slug, all: c.Rules.Match != "any"}
	for i, cond := range c.Rules.Conditions {
		cc, err := compileCondition(cond, i, names)
		if err != nil {
			return compiledClass{}, err
		}
		compiled.conditions = append(compiled.conditions, cc)
	}
	return compiled, nil
}

func compileCondition(cond Condition, index int, names map[string]bool) (compiledCondition, error) {
	cc := compiledCondition{op: cond.Operator, value: cond.Value}
	if cond.Evidence == "header" {
		cc.header = textproto.CanonicalMIMEHeaderKey(cond.Name)
		names[cc.header] = true
		return cc, nil
	}
	item, err := catalog.ParseItem(cond.Item)
	if err != nil {
		return compiledCondition{}, classErr(fmt.Sprintf("rules/conditions/%d/item", index), "must be a billing item name")
	}
	cc.item = item
	return cc, nil
}

// classEvidence is what one successful response said about its source, as far as the rules that were live then cared:
// every header those rules named (present or not) and the billing items the response reported. Its fixed array is why
// Observe stays allocation-free; headers longer than 256 bytes are cut when they are stored. A header that was not
// named by the rules then is unknown, not absent: a later rule that reads it must wait for a new response.
type classEvidence struct {
	headers [MaxFingerprintKey]fingerprintHeader
	n       int
	usage   catalog.ItemSet
	at      time.Time
}

type fingerprintHeader struct {
	name, value string
	present     bool
	cut         bool // value is the first maxEvidenceValue bytes of a longer one
}

const maxEvidenceValue = maxConditionValue // at least every rule value, so a cut keeps whole any prefix a rule names

// observeClass records the response's evidence for the endpoint and classifies it. The response headers are the
// upstream's own; a header the rules do not name is neither read nor kept.
func (e *State) observeClass(c *compiledClasses, h http.Header, usage catalog.ItemSet, at time.Time) {
	e.evidence.n = 0
	for _, name := range c.headers {
		value, present := headerValue(h, name)
		e.evidence.headers[e.evidence.n] = fingerprintHeader{name: name, value: clip(value), present: present, cut: len(value) > maxEvidenceValue}
		e.evidence.n++
	}
	e.evidence.usage, e.evidence.at = usage, at
	e.class = c.classify(&e.evidence)
}

// headerValue returns the first value of a canonical header name; an empty value still says the header is present.
func headerValue(h http.Header, name string) (string, bool) {
	values := h[name]
	if len(values) == 0 {
		return "", false
	}
	return values[0], true
}

// clip bounds a stored header value without copying the short values, which are every value a rule is likely to name.
func clip(s string) string {
	if len(s) <= maxEvidenceValue {
		return s
	}
	return strings.Clone(s[:maxEvidenceValue])
}

// header returns a checked header as stored, or false for a header the rules in force when the response was observed
// did not name; that is not evidence of absence.
func (e *classEvidence) header(name string) (fingerprintHeader, bool) {
	for i := 0; i < e.n; i++ {
		if e.headers[i].name == name {
			return e.headers[i], true
		}
	}
	return fingerprintHeader{}, false
}

// classify returns the first class whose conditions hold, or the fallback. No evidence yet means the fallback: an
// endpoint that never answered successfully has no fingerprint to trust.
func (c *compiledClasses) classify(e *classEvidence) string {
	if e.at.IsZero() {
		return c.fallback
	}
	for _, class := range c.classes {
		if class.matches(e) {
			return class.slug
		}
	}
	return c.fallback
}

func (c compiledClass) matches(e *classEvidence) bool {
	if len(c.conditions) == 0 {
		return false
	}
	for _, cond := range c.conditions {
		ok := cond.matches(e)
		if c.all && !ok {
			return false
		}
		if !c.all && ok {
			return true
		}
	}
	return c.all
}

//nolint:cyclop // one branch per fixed operator; the operators are a closed set
func (c compiledCondition) matches(e *classEvidence) bool {
	if c.header != "" {
		h, known := e.header(c.header)
		if !known {
			return false // a header the evidence never checked is unknown, not absent
		}
		// A cut value is a true prefix of the real one and a rule value is at most as long as the cut, so prefix and a
		// found substring are exact; equality and a suffix are about the lost tail, and what cannot be decided is no
		// match. A value longer than every rule value equals none of them anyway.
		switch c.op {
		case "present":
			return h.present
		case "absent":
			return !h.present
		case "equals":
			return h.present && !h.cut && h.value == c.value
		case "prefix":
			return h.present && strings.HasPrefix(h.value, c.value)
		case "suffix":
			return h.present && !h.cut && strings.HasSuffix(h.value, c.value)
		case "contains":
			return h.present && strings.Contains(h.value, c.value)
		}
		return false
	}
	present := e.usage.Has(c.item)
	if c.op == "absent" {
		return !present
	}
	return present
}

// SetSourceClasses installs a new class table and re-evaluates every endpoint from the evidence its last successful
// response left. The call path reads the classes under the endpoint's lock, so storing the new set before taking each
// lock keeps a late Observe from classifying with the old rules after the re-evaluation. A compile error leaves the
// old set in place: a node never routes on half-read administrator data.
func (m *Monitor) SetSourceClasses(classes []SourceClass) error {
	c, err := compileClasses(classes)
	if err != nil {
		return err
	}
	if old := m.classes.Load(); old != nil && old.version == c.version {
		return nil
	}
	m.classes.Store(c)
	m.mu.RLock()
	states := make([]*State, 0, len(m.endpoints))
	for _, e := range m.endpoints {
		states = append(states, e)
	}
	m.mu.RUnlock()
	for _, e := range states {
		e.mu.Lock()
		e.class = c.classify(&e.evidence)
		e.mu.Unlock()
	}
	return nil
}
