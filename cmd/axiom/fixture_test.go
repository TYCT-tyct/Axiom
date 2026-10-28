package main

import (
	"net/url"
	"strings"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/platform/pgtest"
)

// fixtureDatabase is a database prepared once for a family of tests and copied for each: the rows they all start from
// are written once, and a copy keeps every test's writes to itself, so the family can still run in parallel.
type fixtureDatabase[T any] struct {
	once   sync.Once
	source pgtest.Database
	value  T
}

// get returns a fresh copy of the fixture's database. build runs once, on the first caller's test; it must close every
// connection it opened, so that a copy can be made while the other tests run.
func (f *fixtureDatabase[T]) get(t *testing.T, build func(*testing.T) (pgtest.Database, T)) (commandLine, T) {
	t.Helper()
	f.once.Do(func() { f.source, f.value = build(t) })
	if f.source.App == "" {
		t.Fatal("the fixture database was not built")
	}
	return commandLineAt(t, copyDatabase(t, f.source)), f.value
}

// copyDatabase creates a database from source, so the copy has its rows and neither database sees the other's writes.
func copyDatabase(t *testing.T, source pgtest.Database) pgtest.Database {
	t.Helper()
	name := "test_fixture_" + strings.ReplaceAll(uuid.NewV7().String(), "-", "")
	if err := pgtest.Run(t.Context(), databaseURL(t, source.Superuser, "postgres"),
		"CREATE DATABASE "+name+" TEMPLATE "+databaseName(t, source.Superuser)); err != nil {
		t.Fatal(err)
	}
	return pgtest.Database{
		App:       databaseURL(t, source.App, name),
		Migrator:  databaseURL(t, source.Migrator, name),
		Superuser: databaseURL(t, source.Superuser, name),
	}
}

// databaseURL is raw with the database it names replaced by name.
func databaseURL(t *testing.T, raw, name string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	return parsed.String()
}

// databaseName is the database raw names.
func databaseName(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(parsed.Path, "/")
}
