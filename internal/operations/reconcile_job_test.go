package operations

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestReconcileWork: a run of the job that fails is logged and not retried, and one that succeeds logs nothing; each
// step of the reconciliation hands a failure of its database to its caller.
func TestReconcileWork(t *testing.T) {
	t.Parallel()
	db, err := pg.Open(t.Context(), pgtest.New(t, Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	var logs bytes.Buffer
	work := reconcileWork(db, pg.PlatformScope(), slog.New(slog.NewTextHandler(&logs, nil)))
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := work(t.Context(), nil); err != nil || logs.Len() != 0 {
		t.Errorf("a run that succeeds: %v %s", err, logs.String())
	}
	if err := work(canceled, nil); err != nil || !strings.Contains(logs.String(), "reconciliation of the cap counters stopped") {
		t.Errorf("a run that fails: %v %s", err, logs.String())
	}
	for name, step := range map[string]func(tx pg.Tx) error{
		"driftOf":      func(tx pg.Tx) error { _, err := driftOf(canceled, tx, CapDriftParams{Every: true}); return err },
		"pauseDrifted": func(tx pg.Tx) error { _, err := pauseDrifted(canceled, tx, CategoryTopUp, nil); return err },
	} {
		if err := db.Tx(t.Context(), pg.PlatformScope(), step); !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a failing database: %v", name, err)
		}
	}
}
