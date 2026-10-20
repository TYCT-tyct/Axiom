package kv

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestParseMemory: the three fields that matter, and a policy that must be there: without it the server would tell us
// nothing about what it does at its limit.
func TestParseMemory(t *testing.T) {
	t.Parallel()
	info := "# Memory\r\nused_memory:1024\r\nused_memory_human:1.00K\r\nmaxmemory:268435456\r\nmaxmemory_policy:noeviction\r\n"
	m, err := parseMemory(info)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m != (memory{used: 1024, max: 268435456, policy: "noeviction"}) {
		t.Fatalf("memory = %+v", m)
	}
	if _, err := parseMemory("# Memory\r\nused_memory:1024\r\n"); err == nil {
		t.Fatal("a server that gave no policy parsed")
	}
}

// TestPolicyProblem: no limit, and a policy that evicts, are the two deployment mistakes the start refuses over.
func TestPolicyProblem(t *testing.T) {
	t.Parallel()
	if err := (memory{}).policyProblem(); err == nil || !strings.Contains(err.Error(), "maxmemory") {
		t.Fatalf("no limit: %v", err)
	}
	if err := (memory{max: 1 << 20, policy: "allkeys-lru"}).policyProblem(); err == nil || !strings.Contains(err.Error(), "noeviction") {
		t.Fatalf("an evicting policy: %v", err)
	}
	if err := (memory{max: 1 << 20, policy: "noeviction"}).policyProblem(); err != nil {
		t.Fatalf("noeviction with a limit: %v", err)
	}
}

// TestServerOffset: the server read its clock half of the round trip into the call, which is what the middle corrects.
func TestServerOffset(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	ended := started.Add(10 * time.Millisecond)
	// The server is a second ahead of the moment in the middle of the call.
	server := ended.Add(-5 * time.Millisecond).Add(time.Second)
	skew, err := serverOffset([]int64{server.Unix(), int64(server.Nanosecond()) / 1000}, started, ended)
	if err != nil {
		t.Fatalf("offset: %v", err)
	}
	if skew != time.Second {
		t.Fatalf("skew = %v, want 1s", skew)
	}
	if err := skewProblem(skew); err != nil {
		t.Fatalf("a second is the bound: %v", err)
	}
	if err := skewProblem(skew + time.Millisecond); err == nil {
		t.Fatal("a second and a millisecond passed the clock check")
	}
	if _, err := serverOffset(nil, started, ended); err == nil {
		t.Fatal("a reply without the two fields gave an offset")
	}
}

// TestSinceReportsOnce: a condition that keeps holding is one report, not one per beat, and a rolling deploy's overlap
// never reaches one.
func TestSinceReportsOnce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	var s since
	if s.holding(now) {
		t.Fatal("a condition reported before it had held")
	}
	if s.holding(now.Add(overFor - time.Second)) {
		t.Fatal("a condition reported inside its period")
	}
	if !s.holding(now.Add(overFor)) {
		t.Fatal("a condition that held for its period did not report")
	}
	if s.holding(now.Add(overFor + time.Minute)) {
		t.Fatal("the same condition reported twice")
	}
	s.released()
	if s.holding(now.Add(2 * overFor)) {
		t.Fatal("a cleared condition reported before it had held again")
	}
}

// TestSkewProblemNamesTheVariableOnly: the error says how far the clock is off and never quotes the URL or the password.
func TestSkewProblemNamesTheVariableOnly(t *testing.T) {
	t.Parallel()
	err := skewProblem(90 * time.Minute)
	if err == nil || !strings.Contains(err.Error(), URLVar) || !strings.Contains(err.Error(), "1h30m0s") {
		t.Fatalf("skewProblem = %v, want it to name %s and the distance", err, URLVar)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatal("a clock that is off is not an outage")
	}
}
