package main

import (
	"log/slog"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/observability"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/pg"
)

type writerSignals []string

func (s *writerSignals) RaiseOnce(_, kind, key, severity string, fields map[string]string) error {
	return s.Raise(kind, key, severity, fields)
}

func (s *writerSignals) Raise(string, string, string, map[string]string) error {
	*s = append(*s, "raise")
	return nil
}
func (s *writerSignals) Resolve(string, string) error { *s = append(*s, "resolve"); return nil }

func TestWriterAlertDebouncesEmptySamples(t *testing.T) {
	var sample alertSample
	var sink writerSignals
	for _, depth := range []int{1, 0, 1, 0, 0} {
		if err := sample.writer(&sink, "node", depth); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink) != 3 || sink[0] != "raise" || sink[1] != "raise" || sink[2] != "resolve" {
		t.Fatalf("unexpected transitions: %v", sink)
	}
}

func TestAlertGracefulStopResolvesDrainedWriter(t *testing.T) {
	f := newAdminFixture(t)
	logger := slog.New(slog.DiscardHandler)
	recorder := observability.NewRecorder(t.Context(), f.db, pg.PlatformScope(), callRecords, logger)
	t.Cleanup(func() {
		if err := recorder.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	console := consoleParts{cfg: nodeSettings{shutdownTimeout: 5 * time.Second}}
	console.cfg.valkey.NodeID = "drained-node"
	stopAlerts := startAlerts(t.Context(), f.db, recorder, &console, logger)
	stop := sync.OnceFunc(stopAlerts)
	t.Cleanup(stop)
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return alert.Persist(t.Context(), tx, uuid.NewV7(), alert.Signal{Kind: "observability.write_lag", Key: "drained-node", Severity: "warning", Fields: map[string]string{"value": "4000"}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	stop()
	var resolved bool
	if err := f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT coalesce((body->>'resolved')::boolean,false) FROM platform.alert_signals WHERE body->>'kind'='observability.write_lag' AND body->>'key'='drained-node' ORDER BY seq DESC LIMIT 1`).Scan(&resolved)
	}); err != nil {
		t.Fatal(err)
	}
	if !resolved {
		t.Fatal("graceful drain left a firing writer alert")
	}
}
