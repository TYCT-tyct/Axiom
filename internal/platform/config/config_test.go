package config

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestEnv(t *testing.T) {
	cases := []struct {
		name     string
		environ  []string
		addr     string
		timeout  time.Duration
		problems []string
	}{
		{"defaults", nil, "127.0.0.1:1", time.Second, nil},
		{"set", []string{"AXIOM_ADDR=:2", "AXIOM_TIMEOUT=1m30s"}, ":2", 90 * time.Second, nil},
		{
			"an empty value is unset, as Compose passes an unset ${VAR}",
			[]string{"AXIOM_ADDR=", "AXIOM_TIMEOUT="},
			"127.0.0.1:1", time.Second, nil,
		},
		{"other variables are not ours", []string{"PATH=/bin", "axiom_addr=:3", "=C:=C:\\"}, "127.0.0.1:1", time.Second, nil},
		{"a value may contain =", []string{"AXIOM_ADDR=a=b"}, "a=b", time.Second, nil},
		{
			"every problem at once; an unknown name without its value, which may be a misspelled secret",
			[]string{"AXIOM_TIMEOUT=0", "AXIOM_ADRR=secret-looking", "AXIOM_EXTRA=x"},
			"127.0.0.1:1", time.Second,
			[]string{`AXIOM_TIMEOUT="0": want a positive duration such as 30s`, "AXIOM_ADRR: unknown variable", "AXIOM_EXTRA: unknown variable"},
		},
		{
			"a duration needs a unit",
			[]string{"AXIOM_TIMEOUT=30"},
			"127.0.0.1:1", time.Second,
			[]string{`AXIOM_TIMEOUT="30": want a positive duration such as 30s`},
		},
		{
			"a negative duration",
			[]string{"AXIOM_TIMEOUT=-1s"},
			"127.0.0.1:1", time.Second,
			[]string{`AXIOM_TIMEOUT="-1s": want a positive duration such as 30s`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := New(c.environ)
			if addr := env.String("AXIOM_ADDR", "127.0.0.1:1"); addr != c.addr {
				t.Errorf("AXIOM_ADDR = %q, want %q", addr, c.addr)
			}
			if timeout := env.Duration("AXIOM_TIMEOUT", time.Second); timeout != c.timeout {
				t.Errorf("AXIOM_TIMEOUT = %v, want %v", timeout, c.timeout)
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
