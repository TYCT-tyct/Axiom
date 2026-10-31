package suppliers

import (
	"fmt"
	"log/slog"
	"os"
	"time"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/schedule"
)

// SyncConfig is what a node needs to keep the suppliers up to date.
type SyncConfig struct {
	DB *pg.DB
	// Scope sees every supplier: the platform scope, which only cmd/ may build.
	Scope  pg.Scope
	Keys   *secret.Keyring
	Egress *egress.Client
	// Queue is the job queue the sync runs on; call RegisterSync before the queue starts.
	Queue  *pg.Queue
	Logger *slog.Logger
	// Registry is the adapters of the program; nil is registry.Default.
	Registry *registry.Registry
	// Clock is the time; nil is time.Now.
	Clock func() time.Time
	// SweepEvery is how often the suppliers that are due are looked for; zero is 15 seconds.
	SweepEvery time.Duration
	// DirectFormats lets syncing write embeddings and rerank. It stays false until every node that may read the
	// tables is upgraded: an older node drops the whole supplier when it meets an unknown format.
	DirectFormats bool
}

// RegisterSync makes the node sync the suppliers: it adds the workers and the periodic sweep to the queue, which then
// runs a round of the engine for each supplier that is due, one node at a time for each (README「调度」).
func RegisterSync(cfg SyncConfig) error {
	if cfg.Registry == nil {
		r, err := registry.Default()
		if err != nil {
			return fmt.Errorf("adapters: %w", err)
		}
		cfg.Registry = r
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.SweepEvery == 0 {
		cfg.SweepEvery = 15 * time.Second
	}
	if err := RegisterSyncAlerts(cfg); err != nil {
		return err
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	_, err = schedule.New(schedule.Config{
		DB: cfg.DB, Scope: cfg.Scope, Keys: cfg.Keys, Egress: cfg.Egress, Queue: cfg.Queue, Registry: cfg.Registry,
		Holder: host + "/" + uuid.NewV7().String(), SweepEvery: cfg.SweepEvery, Now: cfg.Clock, Logger: cfg.Logger,
		DirectFormats: cfg.DirectFormats,
	})
	return err
}
