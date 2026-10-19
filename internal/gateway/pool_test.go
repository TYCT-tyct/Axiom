package gateway

import (
	"context"
	"testing"
)

func acquire(ctx context.Context, p *Pool, c Class) func() (func(), error) {
	return func() (func(), error) { return p.Acquire(ctx, c) }
}

// Admission may use N-16 connections, never the last 16; completion may use all of them. The boundary is exact: the
// admission waiter runs when the pool falls to N-17 in use, not before.
func TestPoolKeepsTheLastConnectionsForCompletion(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		cfg := DefaultConfig()
		bp, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		pool := bp.Pool
		var admissions, completions []func()
		for range cfg.PoolSize - cfg.PoolReserve {
			admissions = append(admissions, mustAcquire(t, acquire(ctx, pool, Admission)))
		}
		waitingAdmission := start(acquire(ctx, pool, Admission))
		waitingAdmission.mustWait(t)

		for range cfg.PoolReserve {
			completions = append(completions, mustAcquire(t, acquire(ctx, pool, Completion)))
		}
		waitingCompletion := start(acquire(ctx, pool, Completion))
		waitingCompletion.mustWait(t)

		completions[0]()
		waitingCompletion.mustGranted(t)
		waitingAdmission.mustWait(t)

		for i := range cfg.PoolReserve {
			admissions[i]()
			waitingAdmission.mustWait(t) // 64 - 16 in use is still not below the admission ceiling of 48
		}
		admissions[cfg.PoolReserve]()
		waitingAdmission.mustGranted(t)
	})
}

// A batch writer holds at most BatchConnections.
func TestPoolBatchWritersHaveTheirOwnLimit(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		bp, err := New(DefaultConfig(), nil)
		if err != nil {
			t.Fatal(err)
		}
		pool := bp.Pool
		records := []func(){mustAcquire(t, acquire(ctx, pool, CallRecords)), mustAcquire(t, acquire(ctx, pool, CallRecords))}
		third := start(acquire(ctx, pool, CallRecords))
		third.mustWait(t)
		records[0]()
		third.mustGranted(t)
	})
}

// A batch writer is background work: it may not eat into the connections reserved for completion either.
func TestPoolBatchWritersStayOutOfTheReserve(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		cfg := DefaultConfig()
		bp, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		var admissions []func()
		for range cfg.PoolSize - cfg.PoolReserve {
			admissions = append(admissions, mustAcquire(t, acquire(ctx, bp.Pool, Admission)))
		}
		write := start(acquire(ctx, bp.Pool, CallRecords))
		write.mustWait(t)
		admissions[0]()
		write.mustGranted(t)
	})
}

// A context that has ended never keeps a connection, whether it ended before or while the call waited.
func TestPoolDeadContextKeepsNothing(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		cfg := DefaultConfig()
		bp, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		dead, cancel := context.WithCancel(ctx)
		cancel()
		for range 100 {
			if release, err := bp.Pool.Acquire(dead, Completion); err == nil {
				release()
				t.Fatal("a cancelled context got a connection")
			}
		}
		for range cfg.PoolSize {
			mustAcquire(t, acquire(ctx, bp.Pool, Completion))
		}
	})
}
