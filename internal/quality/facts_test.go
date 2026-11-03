package quality

import (
	"context"
	"testing"
	"time"
	"uuid"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// TestMeasureFacts: detection records the capabilities, parameters and ceilings it found; an unstated field stays
// unmeasured, a later statement flips the bit it names, and a configuration change inherits nothing.
func TestMeasureFacts(t *testing.T) {
	const (
		temperature = protocol.ParameterSet(1) << protocol.ParamTemperature
		topP        = protocol.ParameterSet(1) << protocol.ParamTopP
	)
	m := New(nil)
	ep := suppliers.Routable{
		ID: suppliers.EndpointID{9}, Model: uuid.NewV7(), Upstream: "up-a",
		Formats: []protocol.Format{protocol.Chat},
	}
	at := t0.Add(time.Hour)
	if err := m.Prepare(context.Background(), []suppliers.Routable{ep}); err != nil {
		t.Fatal(err)
	}
	if got := m.MeasuredFor(ep, at).Facts; len(got) != 0 {
		t.Fatalf("before any measurement: %+v", got)
	}
	if !m.Measure(ep, protocol.Chat, Facts{
		Supported: protocol.Tools, Unsupported: protocol.Images,
		SupportedParameters: temperature, UnsupportedParameters: topP,
		Context: 32000, MaxOutput: 4096,
	}) {
		t.Fatal("the current configuration's measurement was refused")
	}
	want := Facts{
		Supported: protocol.Tools, Unsupported: protocol.Images,
		SupportedParameters: temperature, UnsupportedParameters: topP,
		Context: 32000, MaxOutput: 4096,
	}
	if got := m.MeasuredFor(ep, at).Facts[protocol.Chat]; got != want {
		t.Fatalf("measured facts = %+v, want %+v", got, want)
	}
	// A later check that finds images working and a shorter context replaces those fields and leaves the others alone.
	if !m.Measure(ep, protocol.Chat, Facts{Supported: protocol.Images, Context: 16000}) {
		t.Fatal("the current configuration's second measurement was refused")
	}
	want = Facts{
		Supported:           protocol.Tools | protocol.Images,
		SupportedParameters: temperature, UnsupportedParameters: topP,
		Context: 16000, MaxOutput: 4096,
	}
	if got := m.MeasuredFor(ep, at).Facts[protocol.Chat]; got != want {
		t.Fatalf("after the second measurement = %+v, want %+v", got, want)
	}
	// A measurement that finds only top_p working lifts the old lack; a longer limit replaces the shorter one.
	if !m.Measure(ep, protocol.Chat, Facts{SupportedParameters: topP, Context: 64000}) {
		t.Fatal("the current configuration's third measurement was refused")
	}
	want = Facts{
		Supported:           protocol.Tools | protocol.Images,
		SupportedParameters: temperature | topP,
		Context:             64000, MaxOutput: 4096,
	}
	if got := m.MeasuredFor(ep, at).Facts[protocol.Chat]; got != want {
		t.Fatalf("after the third measurement = %+v, want %+v", got, want)
	}
	// The same endpoint ID serving another upstream name is a different configuration: nothing carries over.
	changed := ep
	changed.Upstream = "up-b"
	if got := m.MeasuredFor(changed, at).Facts; len(got) != 0 {
		t.Fatalf("a changed configuration inherited the facts: %+v", got)
	}
}

// TestMeasureBothWays: one measurement that names a bit both ways reads as working, and a measurement that states
// nothing the endpoint had measured before changes nothing.
func TestMeasureBothWays(t *testing.T) {
	m := New(nil)
	ep := suppliers.Routable{ID: suppliers.EndpointID{10}, Model: uuid.NewV7(), Upstream: "up", Formats: []protocol.Format{protocol.Messages}}
	if err := m.Prepare(context.Background(), []suppliers.Routable{ep}); err != nil {
		t.Fatal(err)
	}
	if !m.Measure(ep, protocol.Messages, Facts{Unsupported: protocol.Tools}) {
		t.Fatal("the current configuration's measurement was refused")
	}
	if !m.Measure(ep, protocol.Messages, Facts{Supported: protocol.Tools, Unsupported: protocol.Tools}) {
		t.Fatal("the current configuration's second measurement was refused")
	}
	if got := m.MeasuredFor(ep, t0).Facts[protocol.Messages]; got.Supported != protocol.Tools || got.Unsupported != 0 {
		t.Fatalf("a support statement did not win: %+v", got)
	}
	if !m.Measure(ep, protocol.Messages, Facts{Context: 1000}) {
		t.Fatal("the current configuration's third measurement was refused")
	}
	if !m.Measure(ep, protocol.Messages, Facts{Context: 0}) {
		t.Fatal("the current configuration's fourth measurement was refused")
	}
	if got := m.MeasuredFor(ep, t0).Facts[protocol.Messages].Context; got != 1000 {
		t.Fatalf("an unstated context changed it to %d, want 1000", got)
	}
}

// TestMeasureByFormat: a measurement belongs to the format it was made in. Chat and Responses keep separate facts, and
// a measurement of a format the endpoint does not serve is refused.
func TestMeasureByFormat(t *testing.T) {
	m := New(nil)
	ep := suppliers.Routable{
		ID: suppliers.EndpointID{11}, Model: uuid.NewV7(), Upstream: "up",
		Formats: []protocol.Format{protocol.Chat, protocol.Responses},
	}
	if err := m.Prepare(context.Background(), []suppliers.Routable{ep}); err != nil {
		t.Fatal(err)
	}
	if !m.Measure(ep, protocol.Chat, Facts{Context: 128_000, Unsupported: protocol.Tools}) {
		t.Fatal("the Chat measurement was refused")
	}
	if !m.Measure(ep, protocol.Responses, Facts{Context: 32_000, Supported: protocol.FileSearch}) {
		t.Fatal("the Responses measurement was refused")
	}
	if m.Measure(ep, protocol.Messages, Facts{Context: 1}) {
		t.Fatal("a format the endpoint does not serve was measured")
	}
	got := m.MeasuredFor(ep, t0).Facts
	if got[protocol.Chat].Context != 128_000 || got[protocol.Chat].Unsupported != protocol.Tools {
		t.Errorf("Chat facts = %+v", got[protocol.Chat])
	}
	if got[protocol.Responses].Context != 32_000 || got[protocol.Responses].Supported != protocol.FileSearch {
		t.Errorf("Responses facts = %+v", got[protocol.Responses])
	}
}

// measuredConfiguration prepares A, publishes B after the endpoint moves, and records B's lack of tools. It is the
// state a stale routing build or a late detection has to survive.
func measuredConfiguration(t *testing.T) (*Monitor, suppliers.Routable, suppliers.Routable) {
	t.Helper()
	m := New(nil)
	id := suppliers.EndpointID{12}
	a := suppliers.Routable{ID: id, Model: uuid.NewV7(), Upstream: "up-a", Formats: []protocol.Format{protocol.Chat}, Generation: 1}
	b := suppliers.Routable{ID: id, Model: uuid.NewV7(), Upstream: "up-b", Formats: []protocol.Format{protocol.Responses}, Generation: 2}
	ctx := context.Background()
	if err := m.Prepare(ctx, []suppliers.Routable{a}); err != nil {
		t.Fatal(err)
	}
	if err := m.Prepare(ctx, []suppliers.Routable{b}); err != nil {
		t.Fatal(err)
	}
	if !m.Measure(b, protocol.Responses, Facts{Unsupported: protocol.Tools}) {
		t.Fatal("the new configuration's measurement was refused")
	}
	return m, a, b
}

// TestMeasureStaleConfiguration: a result from a configuration that was current when detection started must not clear
// facts the endpoint's new configuration has already recorded. Prepare publishes the current configuration; Measure
// refuses an older one in the same critical section that would have written it.
func TestMeasureStaleConfiguration(t *testing.T) {
	m, a, b := measuredConfiguration(t)
	if m.Measure(a, protocol.Chat, Facts{Supported: protocol.Tools, Context: 1}) {
		t.Fatal("a stale measurement was accepted and overwrote the new configuration")
	}
	if got := m.MeasuredFor(b, t0).Facts; len(got) != 1 || got[protocol.Responses].Unsupported != protocol.Tools {
		t.Fatalf("the new configuration's facts = %+v, want the measured lack of tools", got)
	}
}

// TestPrepareRejectsOlderGeneration: the routing build that read A can resume after B and call Prepare(A). Its older
// generation must not republish A, which would clear B's facts and let A's late measurement through. Republishing B
// keeps the facts it already measured.
func TestPrepareRejectsOlderGeneration(t *testing.T) {
	m, a, b := measuredConfiguration(t)
	ctx := context.Background()
	if err := m.Prepare(ctx, []suppliers.Routable{a}); err != nil {
		t.Fatal(err)
	}
	if got := m.MeasuredFor(b, t0).Facts; len(got) != 1 || got[protocol.Responses].Unsupported != protocol.Tools {
		t.Fatalf("a stale Prepare(A) changed B's facts to %+v", got)
	}
	if m.Measure(a, protocol.Chat, Facts{Supported: protocol.Tools, Context: 1}) {
		t.Fatal("a measurement after stale Prepare(A) was accepted")
	}
	if err := m.Prepare(ctx, []suppliers.Routable{b}); err != nil {
		t.Fatal(err)
	}
	if got := m.MeasuredFor(b, t0).Facts; len(got) != 1 || got[protocol.Responses].Unsupported != protocol.Tools {
		t.Fatalf("republishing B lost its facts: %+v", got)
	}
	if m.Measure(a, protocol.Chat, Facts{Supported: protocol.Tools, Context: 1}) {
		t.Fatal("a measurement was accepted after B was republished")
	}
}

// TestPrepareFormatChangeClearsFacts: adding a format changes the configuration even though the model and upstream name
// stay, so the old per-format facts are not carried into the new set.
func TestPrepareFormatChangeClearsFacts(t *testing.T) {
	m, _, b := measuredConfiguration(t)
	c := b
	c.Generation = 3
	c.Formats = []protocol.Format{protocol.Chat, protocol.Responses}
	if err := m.Prepare(context.Background(), []suppliers.Routable{c}); err != nil {
		t.Fatal(err)
	}
	if got := m.MeasuredFor(c, t0).Facts; len(got) != 0 {
		t.Fatalf("a changed format set inherited facts: %+v", got)
	}
}
