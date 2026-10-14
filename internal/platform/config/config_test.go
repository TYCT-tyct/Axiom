package config

import (
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
	defaults := values{"", "127.0.0.1:1", time.Second}
	cases := []struct {
		name     string
		environ  []string
		want     values
		problems []string
	}{
		{"defaults", nil, defaults, nil},
		{
			"set",
			[]string{"AXIOM_URL=postgres://h/db?sslmode=require", "AXIOM_ADDR=[::1]:2", "AXIOM_TIMEOUT=1m30s"},
			values{"postgres://h/db?sslmode=require", "[::1]:2", 90 * time.Second},
			nil,
		},
		{"a bare port listens on every address", []string{"AXIOM_ADDR=:2"}, values{"", ":2", time.Second}, nil},
		{"an empty value is unset, as Compose passes an unset ${VAR}", []string{"AXIOM_ADDR=", "AXIOM_TIMEOUT="}, defaults, nil},
		{"other variables are not ours", []string{"PATH=/bin", "axiom_addr=:3", "=C:=C:\\"}, defaults, nil},
		{
			"every problem at once, never quoting a value, which may be a misplaced secret",
			[]string{"AXIOM_TIMEOUT=planted-value", "AXIOM_ADDR=planted-value", "AXIOM_ADRR=planted-value", "AXIOM_EXTRA=x"},
			defaults,
			[]string{wantAddr, wantDuration, "AXIOM_ADRR: unknown variable", "AXIOM_EXTRA: unknown variable"},
		},
		{"a duration needs a unit", []string{"AXIOM_TIMEOUT=30"}, defaults, []string{wantDuration}},
		{"a negative duration", []string{"AXIOM_TIMEOUT=-1s"}, defaults, []string{wantDuration}},
		{"an address needs a port", []string{"AXIOM_ADDR=127.0.0.1"}, defaults, []string{wantAddr}},
		{"a port fits in 16 bits", []string{"AXIOM_ADDR=:65536"}, defaults, []string{wantAddr}},
		{"no host names, so listening never waits on a lookup", []string{"AXIOM_ADDR=localhost:2"}, defaults, []string{wantAddr}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := New(c.environ)
			got := values{env.String("AXIOM_URL", ""), env.Addr("AXIOM_ADDR", "127.0.0.1:1"), env.Duration("AXIOM_TIMEOUT", time.Second)}
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
