package alert

import (
	"testing"
)

func TestBoundedSignalOwnsFields(t *testing.T) {
	b := &Bus{queue: make(chan pending, 1)}
	fields := map[string]string{"value": "1"}
	if err := b.Raise("test.kind", "key", "warning", fields); err != nil {
		t.Fatal(err)
	}
	fields["value"] = "changed"
	if got := (<-b.queue).signal.Fields["value"]; got != "1" {
		t.Fatal("caller mutated queued signal")
	}
	if err := b.Resolve("test.kind", "key"); err != nil {
		t.Fatal(err)
	}
	if err := b.Raise("test.kind", "key", "warning", nil); err == nil {
		t.Fatal("full buffer accepted signal")
	}
	b.closed = true
	if err := b.Resolve("test.kind", "key"); err == nil {
		t.Fatal("closed buffer accepted signal")
	}
	for _, s := range []Signal{{}, {Kind: "test", Key: "key", Severity: "unknown"}, {Kind: "test", Key: "key", Severity: "info", Fields: map[string]string{"value": "\x00"}}} {
		if err := validate(s); err == nil {
			t.Fatal("accepted invalid signal", s)
		}
	}
}
