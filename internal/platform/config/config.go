// Package config reads the process configuration from its only source, the AXIOM_* environment variables. Every
// problem is collected and reported at once, naming the variable, so a misconfigured node fails at startup with the
// whole list. A set AXIOM_* variable that nothing reads is a problem too: a misspelled name would otherwise leave its
// setting at the default without a word. Problems never quote a value: a secret assigned to the wrong variable must
// not reach the logs.
package config

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Env is the AXIOM_* part of an environment. Read every variable, then call Err once.
type Env struct {
	values map[string]string
	read   map[string]bool
	errs   []error
}

// New takes the environment in the form os.Environ returns. An empty value counts as unset, because Docker Compose
// passes a ${VAR} whose VAR is unset as an empty string.
func New(environ []string) *Env {
	env := &Env{values: map[string]string{}, read: map[string]bool{}}
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "AXIOM_") && value != "" {
			env.values[name] = value
		}
	}
	return env
}

// String returns the variable's value, or def when it is unset.
func (e *Env) String(name, def string) string {
	e.read[name] = true
	if value, ok := e.values[name]; ok {
		return value
	}
	return def
}

// Duration returns the variable as a positive duration in Go's notation, such as 30s or 1m30s, or def when it is unset.
func (e *Env) Duration(name string, def time.Duration) time.Duration {
	value := e.String(name, "")
	if value == "" {
		return def
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		e.errs = append(e.errs, fmt.Errorf("%s: want a positive duration such as 30s", name))
		return def
	}
	return d
}

// Addr returns the variable as an address to listen on, an IP and a port such as 127.0.0.1:8081 or [::1]:8081, or a
// bare port such as :8081 for every address; def when it is unset. Host names are not accepted, so listening never
// waits on a name lookup.
func (e *Env) Addr(name, def string) string {
	value := e.String(name, "")
	if value == "" {
		return def
	}
	host, port, err := net.SplitHostPort(value)
	if _, portErr := strconv.ParseUint(port, 10, 16); err != nil || portErr != nil || host != "" && !isIP(host) {
		e.errs = append(e.errs, fmt.Errorf("%s: want an IP and a port such as 127.0.0.1:8081, or :8081 for every address", name))
		return def
	}
	return value
}

func isIP(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}

// Err reports every invalid variable read so far and every set variable that was not read, one per line.
func (e *Env) Err() error {
	errs := slices.Clone(e.errs)
	for _, name := range slices.Sorted(maps.Keys(e.values)) {
		if !e.read[name] {
			errs = append(errs, fmt.Errorf("%s: unknown variable", name))
		}
	}
	return errors.Join(errs...)
}
