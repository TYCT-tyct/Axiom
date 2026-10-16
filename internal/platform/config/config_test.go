package config

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestEnv(t *testing.T) {
	const (
		wantAddr     = "AXIOM_ADDR: want an IP and a port such as 127.0.0.1:8081, or :8081 for every address"
		wantDuration = "AXIOM_TIMEOUT: want a positive duration such as 30s"
	)
	type values struct {
		URL, Addr string
		Timeout   time.Duration
	}
	defaults := values{"x", "127.0.0.1:1", time.Second}
	url := "AXIOM_URL=x"
	cases := []struct {
		name     string
		environ  []string
		want     values
		problems []string
	}{
		{"defaults", []string{url}, defaults, nil},
		{
			"set",
			[]string{"AXIOM_URL=postgres://h/db?sslmode=require", "AXIOM_ADDR=[::1]:2", "AXIOM_TIMEOUT=1m30s"},
			values{"postgres://h/db?sslmode=require", "[::1]:2", 90 * time.Second},
			nil,
		},
		{"a required variable", nil, values{"", "127.0.0.1:1", time.Second}, []string{"AXIOM_URL: required"}},
		{"a bare port listens on every address", []string{url, "AXIOM_ADDR=:2"}, values{"x", ":2", time.Second}, nil},
		{"an empty value is unset, as Compose passes an unset ${VAR}", []string{url, "AXIOM_ADDR=", "AXIOM_TIMEOUT="}, defaults, nil},
		{"other variables are not ours", []string{url, "PATH=/bin", "axiom_addr=:3", "=C:=C:\\"}, defaults, nil},
		{
			"every problem at once, never quoting a value, which may be a misplaced secret",
			[]string{url, "AXIOM_TIMEOUT=planted-value", "AXIOM_ADDR=planted-value", "AXIOM_ADRR=planted-value", "AXIOM_EXTRA=x"},
			defaults,
			[]string{wantAddr, wantDuration, "AXIOM_ADRR: unknown variable", "AXIOM_EXTRA: unknown variable"},
		},
		{"a duration needs a unit", []string{url, "AXIOM_TIMEOUT=30"}, defaults, []string{wantDuration}},
		{"a negative duration", []string{url, "AXIOM_TIMEOUT=-1s"}, defaults, []string{wantDuration}},
		{"an address needs a port", []string{url, "AXIOM_ADDR=127.0.0.1"}, defaults, []string{wantAddr}},
		{"a port fits in 16 bits", []string{url, "AXIOM_ADDR=:65536"}, defaults, []string{wantAddr}},
		{"no host names, so listening never waits on a lookup", []string{url, "AXIOM_ADDR=localhost:2"}, defaults, []string{wantAddr}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := New(c.environ)
			got := values{env.Required("AXIOM_URL"), env.Addr("AXIOM_ADDR", "127.0.0.1:1"), env.Duration("AXIOM_TIMEOUT", time.Second)}
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("values (-want +got):\n%s", diff)
			}
			var problems []string
			if err := env.Err(); err != nil {
				problems = strings.Split(err.Error(), "\n")
			}
			if diff := cmp.Diff(c.problems, problems); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
		})
	}
}

// An accepted address is an IP or nothing, and a 16-bit port, so listening never waits on a lookup; a refused one is
// reported and reads as the default.
func FuzzAddr(f *testing.F) {
	for _, seed := range []string{"127.0.0.1:8081", "[::1]:8081", ":8081", "localhost:1", "1.2.3.4", ":65536", "[::1%eth0]:1", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		const def = "127.0.0.1:1"
		env := New([]string{"AXIOM_ADDR=" + value})
		got := env.Addr("AXIOM_ADDR", def)
		if env.Err() != nil {
			if got != def {
				t.Fatalf("%q was refused but gave %q", value, got)
			}
			return
		}
		host, port, err := net.SplitHostPort(got)
		if _, portErr := strconv.ParseUint(port, 10, 16); err != nil || portErr != nil {
			t.Fatalf("%q accepted but is not host:port with a 16-bit port", got)
		}
		if _, ipErr := netip.ParseAddr(host); host != "" && ipErr != nil {
			t.Fatalf("%q accepted with a host that is not an IP", got)
		}
	})
}

// An accepted duration is positive; a refused one is reported and reads as the default.
func FuzzDuration(f *testing.F) {
	for _, seed := range []string{"30s", "1m30s", "-1s", "0", "30", "9223372036854775807h", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		const def = time.Second
		env := New([]string{"AXIOM_TIMEOUT=" + value})
		got := env.Duration("AXIOM_TIMEOUT", def)
		if got <= 0 {
			t.Fatalf("%q gave %v", value, got)
		}
		if err := env.Err(); err != nil && got != def {
			t.Fatalf("%q was refused but gave %v", value, got)
		}
	})
}
