package egresstest_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"math/big"
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

func TestTestRootsAreImmutableAndAbsentByDefault(t *testing.T) {
	if roots := (egresstest.Policy{}).RootCAs(); roots != nil {
		t.Fatal("zero policy changes TLS trust")
	}
	if roots := egresstest.Loopback(t).RootCAs(); roots != nil {
		t.Fatal("loopback changes system trust without a test certificate")
	}
	// Distinct test servers share httptest's certificate, so mint a different trust anchor explicitly.
	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	policy := egresstest.Loopback(t, cert)
	clone := policy.RootCAs()
	if clone == policy.RootCAs() {
		t.Fatal("test roots were shared with the caller")
	}
	clone.AddCert(&x509.Certificate{Raw: []byte("another root")})
	if clone.Equal(policy.RootCAs()) {
		t.Fatal("mutating the copied roots changed the policy")
	}
}
