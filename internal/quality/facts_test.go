package quality

import (
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
	if got := m.MeasuredFor(ep, at).Facts; got != (Facts{}) {
		t.Fatalf("before any measurement: %+v", got)
	}
	m.Measure(ep, Facts{
		Supported: protocol.Tools, Unsupported: protocol.Images,
		SupportedParameters: temperature, UnsupportedParameters: topP,
		Context: 32000, MaxOutput: 4096,
	})
	want := Facts{
		Supported: protocol.Tools, Unsupported: protocol.Images,
		SupportedParameters: temperature, UnsupportedParameters: topP,
		Context: 32000, MaxOutput: 4096,
	}
	if got := m.MeasuredFor(ep, at).Facts; got != want {
		t.Fatalf("measured facts = %+v, want %+v", got, want)
	}
	// A later check that finds images working and a shorter context replaces those fields and leaves the others alone.
	m.Measure(ep, Facts{Supported: protocol.Images, Context: 16000})
	want = Facts{
		Supported:           protocol.Tools | protocol.Images,
		SupportedParameters: temperature, UnsupportedParameters: topP,
		Context: 16000, MaxOutput: 4096,
	}
	if got := m.MeasuredFor(ep, at).Facts; got != want {
		t.Fatalf("after the second measurement = %+v, want %+v", got, want)
	}
	// A measurement that finds only top_p working lifts the old lack; a longer limit replaces the shorter one.
	m.Measure(ep, Facts{SupportedParameters: topP, Context: 64000})
	want = Facts{
		Supported:           protocol.Tools | protocol.Images,
		SupportedParameters: temperature | topP,
		Context:             64000, MaxOutput: 4096,
	}
	if got := m.MeasuredFor(ep, at).Facts; got != want {
		t.Fatalf("after the third measurement = %+v, want %+v", got, want)
	}
	// The same endpoint ID serving another upstream name is a different configuration: nothing carries over.
	changed := ep
	changed.Upstream = "up-b"
	if got := m.MeasuredFor(changed, at).Facts; got != (Facts{}) {
		t.Fatalf("a changed configuration inherited the facts: %+v", got)
	}
}

// TestMeasureBothWays: one measurement that names a bit both ways reads as working, and a measurement that states
// nothing the endpoint had measured before changes nothing.
func TestMeasureBothWays(t *testing.T) {
	m := New(nil)
	ep := suppliers.Routable{ID: suppliers.EndpointID{10}, Model: uuid.NewV7(), Upstream: "up", Formats: []protocol.Format{protocol.Messages}}
	m.Measure(ep, Facts{Unsupported: protocol.Tools})
	m.Measure(ep, Facts{Supported: protocol.Tools, Unsupported: protocol.Tools})
	if got := m.MeasuredFor(ep, t0).Facts; got.Supported != protocol.Tools || got.Unsupported != 0 {
		t.Fatalf("a support statement did not win: %+v", got)
	}
	m.Measure(ep, Facts{Context: 1000})
	m.Measure(ep, Facts{Context: 0})
	if got := m.MeasuredFor(ep, t0).Facts.Context; got != 1000 {
		t.Fatalf("an unstated context changed it to %d, want 1000", got)
	}
}
