package store_test

import (
	"testing"

	"axiom/internal/platform/pg"
)

func openApp(t *testing.T, url string) *pg.DB {
	t.Helper()
	db, err := pg.Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
