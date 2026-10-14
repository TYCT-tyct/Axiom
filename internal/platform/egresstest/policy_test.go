package egresstest_test

import (
	"net/netip"
	"os/exec"
	"strings"
	"testing"

	"axiom/internal/platform/egresstest"
)

func TestOrdinaryBinaryCannotGrantLoopback(t *testing.T) {
	run := exec.CommandContext(t.Context(), "go", "run", "./testdata/production")
	output, err := run.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "loopback access requires a test binary") {
		t.Fatalf("ordinary binary granted test access: %v\n%s", err, output)
	}
}

func TestLoopbackAllowsOnlyPlainLoopback(t *testing.T) {
	loopback := egresstest.Loopback(t)
	for input, want := range map[string]bool{
		"127.0.0.1":        true,
		"::1":              true,
		"fe80::1%eth0":     false,
		"::1%eth0":         false,
		"::ffff:127.0.0.1": false,
		"10.0.0.1":         false,
		"fd00::1":          false,
		"169.254.169.254":  false,
	} {
		ip := netip.MustParseAddr(input)
		if got := loopback.Allows(ip); got != want {
			t.Errorf("Loopback.Allows(%s) = %v, want %v", input, got, want)
		}
		if (egresstest.Policy{}).Allows(ip) {
			t.Errorf("zero Policy allowed %s", input)
		}
	}
}
