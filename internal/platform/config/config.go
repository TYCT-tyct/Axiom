// Package config reads the process configuration from its only source, the AXIOM_* environment variables. Every
// problem is collected and reported at once, naming the variable, so a misconfigured node fails at startup with the
// whole list. A set AXIOM_* variable that nothing reads is a problem too: a misspelled name would otherwise leave its
// setting at the default without a word.
package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
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
		e.errs = append(e.errs, fmt.Errorf("%s=%q: want a positive duration such as 30s", name, value))
		return def
	}
	return d
}

// Err reports every invalid variable read so far and every set variable that was not read, one per line. An unknown
// variable is named without its value, which may be a secret under a misspelled name.
func (e *Env) Err() error {
	errs := slices.Clone(e.errs)
	for _, name := range slices.Sorted(maps.Keys(e.values)) {
		if !e.read[name] {
			errs = append(errs, fmt.Errorf("%s: unknown variable", name))
		}
	}
	return errors.Join(errs...)
}
