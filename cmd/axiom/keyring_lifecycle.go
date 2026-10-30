package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/platform/telemetry"
)

// keyringRewrapBatch is how many envelopes one lifecycle step rewraps. A step runs under the lifecycle lock and a
// bounded batch keeps one node's pass from holding it while other nodes' heartbeats wait.
const keyringRewrapBatch = 100

// openNodeCache opens the shared cache, makes the ring current, and then starts the key ring lifecycle. The order
// matters twice: opening the cache writes this node's first heartbeat, and activation and retirement read those reports,
// so the refresh runs after that row exists and before anything can seal. Together with the lifecycle lock the refresh
// takes, it means a retirement that starts afterwards sees this node's report, and a retirement that already committed
// is installed here. The returned stop function ends the lifecycle before closing the cache.
func openNodeCache(ctx context.Context, syncCtx context.Context, cfg nodeSettings, ring *secret.Keyring, db *pg.DB,
	tel *telemetry.Telemetry, logger *slog.Logger,
) (*kv.Client, func(), error) {
	cfg.valkey.Keys = keyReporter(ring)
	cache, err := openCache(ctx, cfg.valkey, cfg.startupTimeout, db, tel, logger)
	if err != nil {
		return nil, nil, err
	}
	rotation := keyRotation(db, cfg.masterKeyFile)
	refreshCtx, cancel := context.WithTimeout(ctx, cfg.startupTimeout)
	defer cancel()
	if _, err := rotation.Refresh(refreshCtx, ring); err != nil {
		closeCache(ctx, cache, cfg.shutdownTimeout, logger)
		return nil, nil, fmt.Errorf("key ring after registering this node:\n%w", err)
	}
	stop := startKeyringLifecycle(syncCtx, rotation, ring, logger)
	return cache, func() {
		stop()
		closeCache(ctx, cache, cfg.shutdownTimeout, logger)
	}, nil
}

// keyReporter is the heartbeat's view of the live ring: the closure reads the current snapshot, so a reload is visible
// on the next beat without a restart. Versions and the write version come from one snapshot, so a beat never reports a
// pair the node never held. It reports identifiers only.
func keyReporter(ring *secret.Keyring) func() kv.KeyReport {
	return func() kv.KeyReport {
		versions, active := ring.Report()
		return kv.KeyReport{Versions: versions, Active: active}
	}
}

// keyringLifecycle keeps a node's ring current and moves a rotation forward: it reloads the distribution file, activates
// a version every live node reports, rewraps toward the write version, and retires versions no node writes and no
// envelope references. Every step is serialized by the lifecycle lock, so all nodes may run it; each one converges.
type keyringLifecycle struct {
	rotation secret.Rotation
	ring     *secret.Keyring
	logger   *slog.Logger
	cursor   secret.RewrapCursor
}

// startKeyringLifecycle starts the loop and returns a stop function that waits for the current step. It runs until the
// context ends or the stop function is called; a failed step is reported and retried on the next tick.
func startKeyringLifecycle(ctx context.Context, rotation secret.Rotation, ring *secret.Keyring, logger *slog.Logger) (stop func()) {
	loop, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	lifecycle := &keyringLifecycle{rotation: rotation, ring: ring, logger: logger}
	go func() {
		defer close(done)
		lifecycle.run(loop)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (l *keyringLifecycle) run(ctx context.Context) {
	ticker := time.NewTicker(kv.BeatEvery)
	defer ticker.Stop()
	for {
		if err := l.step(ctx); err != nil && ctx.Err() == nil {
			l.logger.Error("keyring: lifecycle step failed; retrying next tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *keyringLifecycle) step(ctx context.Context) error {
	status, err := l.rotation.Refresh(ctx, l.ring)
	if err != nil {
		return err
	}
	if newest := newestReadable(status); newest > status.Active {
		// Activation is a wait, not a failure, until every live node has reloaded the distributed version.
		err := l.rotation.Activate(ctx, newest)
		switch {
		case errors.Is(err, secret.ErrNotDistributed):
			return nil
		case err != nil:
			return fmt.Errorf("activate version %d: %w", newest, err)
		}
		l.logger.Info("keyring: activated a distributed version", "version", newest, "previous", status.Active)
		return nil // The ring reloads the new write version on the next tick before anything is rewrapped.
	}
	// One readable version is the write version: no envelope can be at another one, so there is nothing to rewrap or
	// retire. Skipping the scan keeps the steady state at one metadata read per heartbeat instead of a pass over every
	// envelope table.
	if len(status.Readable) <= 1 {
		return nil
	}
	count, cursor, err := l.rotation.RewrapBatch(ctx, l.cursor, keyringRewrapBatch)
	if err != nil {
		return fmt.Errorf("rewrap: %w", err)
	}
	l.cursor = cursor
	if count != 0 {
		l.logger.Info("keyring: rewrapped envelopes", "count", count, "version", status.Active)
		return nil // References may have dropped; retirement is tried once a pass finds none.
	}
	return l.retire(ctx, status)
}

// retire removes versions that no envelope references and no live node still writes. Both conditions are the point:
// a version has to become unused everywhere before its material is destroyed.
func (l *keyringLifecycle) retire(ctx context.Context, status secret.KeyStatus) error {
	for _, version := range status.Readable {
		if version == status.Active {
			continue
		}
		err := l.rotation.Retire(ctx, version)
		switch {
		case errors.Is(err, secret.ErrReferenced), errors.Is(err, secret.ErrQuiet), errors.Is(err, secret.ErrNotDistributed):
			// Rewrap or the next heartbeat has to finish first.
		case err != nil:
			return fmt.Errorf("retire version %d: %w", version, err)
		default:
			l.logger.Info("keyring: retired an unreferenced version", "version", version, "active", status.Active)
		}
	}
	return nil
}

// newestReadable is the highest readable version, which is the one a distribution is moving to; adding a version only
// registers it, and activation moves the whole rotation forward once every live node can read it.
func newestReadable(status secret.KeyStatus) uint32 {
	var newest uint32
	for _, version := range status.Readable {
		newest = max(newest, version)
	}
	return newest
}
