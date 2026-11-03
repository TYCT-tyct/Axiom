package quality

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// classesWith gives the seeds a header rule on official and cloud, leaving relay the fallback.
func classesWith() []SourceClass {
	classes := DefaultSourceClasses()
	classes[0].Rules = Rules{Match: "all", Conditions: []Condition{
		{Evidence: "header", Name: "X-Official", Operator: "present"},
	}}
	classes[1].Rules = Rules{Match: "all", Conditions: []Condition{
		{Evidence: "header", Name: "Server", Operator: "prefix", Value: "cloud"},
	}}
	return classes
}

func observedClass(t *testing.T, m *Monitor, id suppliers.EndpointID, h http.Header, usage catalog.ItemSet) string {
	t.Helper()
	return observedClassAt(t, m, id, h, usage, EndpointConfig{})
}

func observedClassAt(t *testing.T, m *Monitor, id suppliers.EndpointID, h http.Header, usage catalog.ItemSet, config EndpointConfig) string {
	t.Helper()
	o := answered(id, t0, time.Second, time.Second, 100, 100)
	o.Headers, o.UsageItems, o.Config = h, usage, config
	m.Observe(context.Background(), o)
	return m.MeasuredFor(suppliers.Routable{ID: id}, t0.Add(time.Minute)).Class
}

// TestSourceClassClassification: rules are evaluated in position order, headers are compared by their first value, a
// usage item is the other evidence, and an endpoint with no successful response is the fallback.
func TestSourceClassClassification(t *testing.T) {
	m := New(nil)
	id := suppliers.EndpointID{1}
	if got := m.MeasuredFor(suppliers.Routable{ID: id}, t0).Class; got != "relay" {
		t.Fatalf("before a response: %q, want relay", got)
	}
	classes := classesWith()
	// A usage class between cloud and relay: it matches only when the response reported the item.
	classes = append(classes[:2], append([]SourceClass{{
		Slug: "pool", Name: "Pool", Position: 500,
		Rules: Rules{Match: "all", Conditions: []Condition{{Evidence: "usage", Item: "cache_read", Operator: "present"}}},
	}}, classes[2:]...)...)
	if err := m.SetSourceClasses(classes); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		h     http.Header
		usage catalog.ItemSet
		want  string
	}{
		{"official header", http.Header{"X-Official": {"1"}}, 0, "official"},
		{"cloud header", http.Header{"Server": {"cloudflare"}}, 0, "cloud"},
		{"header name is canonicalized", http.Header{"X-Official": {"1"}}, 0, "official"},
		{"value is compared exactly", http.Header{"Server": {"Cloudflare"}}, 0, "relay"},
		{"usage item", http.Header{}, 1 << catalog.ItemCacheRead, "pool"},
		{"nothing matches", http.Header{"Server": {"nginx"}}, 0, "relay"},
	} {
		if got := observedClass(t, m, id, tc.h, tc.usage); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// The official rule now needs a header the stored evidence does not have: the endpoint falls back rather than
	// keeping a class that no longer has evidence.
	classes[0].Rules.Conditions[0].Name = "X-New"
	if err := m.SetSourceClasses(classes); err != nil {
		t.Fatal(err)
	}
	if got := m.MeasuredFor(suppliers.Routable{ID: id}, t0.Add(time.Minute)).Class; got != "relay" {
		t.Fatalf("after a rule change without new evidence: %q, want relay", got)
	}
	if got := observedClass(t, m, id, http.Header{"X-New": {"1"}}, 0); got != "official" {
		t.Fatalf("after a response with the new header: %q, want official", got)
	}
}

// TestSourceClassNewHeaderIsUnknown: adding a rule for a header the old rules did not check must fall back for both
// present and absent; an unchecked header is unknown, not evidence that it was missing.
func TestSourceClassNewHeaderIsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, operator string
		header         http.Header
	}{
		{"present", "present", http.Header{"X-New": {"1"}}},
		{"absent", "absent", http.Header{}},
	} {
		t.Run(tc.name, func(t *testing.T) { testNewHeader(t, tc.operator, tc.header) })
	}
}

func testNewHeader(t *testing.T, operator string, header http.Header) {
	t.Helper()
	m := New(nil)
	if err := m.SetSourceClasses(classesWith()); err != nil {
		t.Fatal(err)
	}
	id := suppliers.EndpointID{3}
	if got := observedClass(t, m, id, http.Header{"X-Official": {"1"}}, 0); got != "official" {
		t.Fatalf("after a response: %q, want official", got)
	}
	classes := classesWith()
	classes[0].Rules = Rules{Match: "all", Conditions: []Condition{
		{Evidence: "header", Name: "X-New", Operator: operator},
	}}
	if err := m.SetSourceClasses(classes); err != nil {
		t.Fatal(err)
	}
	if got := m.MeasuredFor(suppliers.Routable{ID: id}, t0.Add(time.Minute)).Class; got != "relay" {
		t.Fatalf("after adding an unchecked header as %s: %q, want relay", operator, got)
	}
	if got := observedClass(t, m, id, header, 0); got != "official" {
		t.Fatalf("after a response that checked the header: %q, want official", got)
	}
}

// TestSourceClassOnlySuccessfulResponsesAreEvidence: a 2xx followed by an idle timeout, an empty body or a broken
// stream is a failed attempt and must not replace the class its last successful response set.
func TestSourceClassOnlySuccessfulResponsesAreEvidence(t *testing.T) {
	m := New(nil)
	if err := m.SetSourceClasses(classesWith()); err != nil {
		t.Fatal(err)
	}
	id := suppliers.EndpointID{4}
	if got := observedClass(t, m, id, http.Header{"X-Official": {"1"}}, 0); got != "official" {
		t.Fatalf("after a successful response: %q, want official", got)
	}
	for _, tc := range []struct {
		name string
		edit func(*Observation)
	}{
		{"idle timeout", func(o *Observation) { o.Done = false; o.Cut = Idle }},
		{"empty body", func(o *Observation) { o.Done = true; o.Empty = true }},
		{"broken stream", func(o *Observation) { o.Done = false; o.Fault = protocol.FaultBroken }},
	} {
		o := answered(id, t0, time.Second, time.Second, 100, 100)
		tc.edit(&o)
		o.Headers = http.Header{} // would fall back if this failed attempt were evidence
		m.Observe(context.Background(), o)
		if got := m.MeasuredFor(suppliers.Routable{ID: id}, t0.Add(time.Minute)).Class; got != "official" {
			t.Errorf("%s replaced the class with %q, want official", tc.name, got)
		}
	}
}

// TestSourceClassEvidenceBindsToConfig: another model or upstream must not inherit the response headers of the old
// one; Prepare clears the evidence that decided the class.
func TestSourceClassEvidenceBindsToConfig(t *testing.T) {
	m := New(nil)
	if err := m.SetSourceClasses(classesWith()); err != nil {
		t.Fatal(err)
	}
	id := suppliers.EndpointID{2}
	a := suppliers.Routable{ID: id, Model: [16]byte{1}, Upstream: "up-a", Formats: []protocol.Format{protocol.Chat}}
	b := a
	b.Upstream = "up-b"
	if err := m.Prepare(context.Background(), []suppliers.Routable{a}); err != nil {
		t.Fatal(err)
	}
	if got := observedClassAt(t, m, id, http.Header{"X-Official": {"1"}}, 0, configOf(a)); got != "official" {
		t.Fatalf("after a response: %q, want official", got)
	}
	if err := m.Prepare(context.Background(), []suppliers.Routable{b}); err != nil {
		t.Fatal(err)
	}
	if got := m.MeasuredFor(b, t0.Add(time.Minute)).Class; got != "relay" {
		t.Fatalf("after the upstream changed: %q, want relay", got)
	}
}

// TestSourceClassLateObservationOfOldConfig: an attempt sent to up-a can finish after Prepare publishes up-b; its
// headers must not classify up-b.
func TestSourceClassLateObservationOfOldConfig(t *testing.T) {
	m := New(nil)
	if err := m.SetSourceClasses(classesWith()); err != nil {
		t.Fatal(err)
	}
	id := suppliers.EndpointID{5}
	a := suppliers.Routable{ID: id, Model: [16]byte{1}, Upstream: "up-a", Formats: []protocol.Format{protocol.Chat}}
	b := a
	b.Upstream = "up-b"
	if err := m.Prepare(context.Background(), []suppliers.Routable{a}); err != nil {
		t.Fatal(err)
	}
	o := answered(id, t0, time.Second, time.Second, 100, 100)
	o.Headers, o.Config = http.Header{"X-Official": {"1"}}, configOf(a)
	if err := m.Prepare(context.Background(), []suppliers.Routable{b}); err != nil {
		t.Fatal(err)
	}
	m.Observe(context.Background(), o)
	if got := m.MeasuredFor(b, t0.Add(time.Minute)).Class; got != "relay" {
		t.Fatalf("a late up-a response classified up-b as %q, want relay", got)
	}
}

// TestSourceClassValidation covers the boundaries an administrator can reach. Each case names the field the API's 400
// points at.
func TestSourceClassValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func([]SourceClass) []SourceClass
		field string
	}{
		{"slug case", func(c []SourceClass) []SourceClass { c[0].Slug = "Official"; return c }, "slug"},
		{"duplicate position", func(c []SourceClass) []SourceClass { c[1].Position = 0; return c }, "position"},
		{"fallback with rules", func(c []SourceClass) []SourceClass {
			c[2].Rules.Conditions = []Condition{{Evidence: "header", Name: "X", Operator: "present"}}
			return c
		}, "rules"},
		{"two fallbacks", func(c []SourceClass) []SourceClass { c[1].Fallback = true; return c }, "fallback"},
		{"no fallback", func(c []SourceClass) []SourceClass { c[2].Fallback = false; return c }, "fallback"},
		{"fallback not last", func(c []SourceClass) []SourceClass { c[0].Position = 2000; return c }, "position"},
		{"bad operator", func(c []SourceClass) []SourceClass {
			c[0].Rules.Conditions = []Condition{{Evidence: "header", Name: "X", Operator: "regex"}}
			return c
		}, "rules/conditions/0/operator"},
		{"header with an item", func(c []SourceClass) []SourceClass {
			c[0].Rules.Conditions = []Condition{{Evidence: "header", Name: "X", Item: "cache_read", Operator: "present"}}
			return c
		}, "rules/conditions/0/item"},
		{"usage compares", func(c []SourceClass) []SourceClass {
			c[0].Rules.Conditions = []Condition{{Evidence: "usage", Item: "cache_read", Operator: "equals", Value: "1"}}
			return c
		}, "rules/conditions/0/operator"},
		{"too many conditions", func(c []SourceClass) []SourceClass {
			for i := range MaxConditions + 1 {
				c[0].Rules.Conditions = append(c[0].Rules.Conditions,
					Condition{Evidence: "header", Name: "X-" + string(rune('a'+i)), Operator: "present"})
			}
			return c
		}, "rules/conditions"},
		{"too many headers", func(c []SourceClass) []SourceClass {
			// Two classes already read X-Official and Server; seventeen more with two each take the total over the
			// limit without reaching the class-count limit.
			for i := range 17 {
				c = append(c, SourceClass{Slug: "c" + string(rune('a'+i)), Name: "C", Position: 100 + i, Rules: Rules{
					Match: "all",
					Conditions: []Condition{
						{Evidence: "header", Name: "X-A-" + string(rune('a'+i)), Operator: "present"},
						{Evidence: "header", Name: "X-B-" + string(rune('a'+i)), Operator: "present"},
					},
				}})
			}
			return c
		}, "rules"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compileClasses(tc.edit(DefaultSourceClasses()))
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error %v, want field %q", err, tc.field)
			}
		})
	}
}
