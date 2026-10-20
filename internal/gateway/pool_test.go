package gateway

import (
	"context"
	"errors"
	"testing"
)

func acquire(ctx context.Context, p *Pool, c Class) func() (func(), error) {
	return func() (func(), error) { return p.Acquire(ctx, c) }
}

// Saturating either physical pool must leave the other class's entire permit budget available.
func TestPoolClassesDoNotConsumeEachOthersCapacity(t *testing.T) {
	for name, first := range map[string]Class{"completions first": Completion, "admissions first": Admission} {
		t.Run(name, func(t *testing.T) {
			run(t, func(t *testing.T, ctx context.Context) {
				cfg := DefaultConfig()
				pool := newPool(cfg.PoolSize, cfg.PoolReserve)
				capacity := [classCount]int{Completion: cfg.PoolReserve, Admission: cfg.PoolSize - cfg.PoolReserve}
				other := Admission
				if first == Admission {
					other = Completion
				}
				var holders []func()
				for range capacity[first] {
					holders = append(holders, mustAcquire(t, acquire(ctx, pool, first)))
				}
				waiting := start(acquire(ctx, pool, first))
				waiting.mustWait(t)
				next := start(acquire(ctx, pool, first))
				next.mustWait(t)
				var others []func()
				for range capacity[other] {
					others = append(others, mustAcquire(t, acquire(ctx, pool, other)))
				}
				waitingOther := start(acquire(ctx, pool, other))
				waitingOther.mustWait(t)

				holders[0]()
				waiting.mustGranted(t)
				next.mustWait(t)
				waitingOther.mustWait(t)
				holders[0]() // releasing twice cannot grant another permit
				next.mustWait(t)
				waiting.release()
				next.mustGranted(t)
				others[0]()
				waitingOther.mustGranted(t)
			})
		})
	}
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
		for class, capacity := range [classCount]int{Completion: cfg.PoolReserve, Admission: cfg.PoolSize - cfg.PoolReserve} {
			for range 100 {
				if release, err := bp.Pool.Acquire(dead, Class(class)); err == nil {
					release()
					t.Fatal("a cancelled context got a connection")
				}
			}
			var holders []func()
			for range capacity {
				holders = append(holders, mustAcquire(t, acquire(ctx, bp.Pool, Class(class))))
			}
			waitingCtx, stop := context.WithCancel(ctx)
			waiting := start(acquire(waitingCtx, bp.Pool, Class(class)))
			waiting.mustWait(t)
			stop()
			if !waiting.finished() || !errors.Is(waiting.err, context.Canceled) {
				t.Fatalf("cancelled waiter: finished=%v err=%v", waiting.finished(), waiting.err)
			}
			for _, release := range holders {
				release()
			}
			for range capacity {
				mustAcquire(t, acquire(ctx, bp.Pool, Class(class)))
			}
		}
	})
}

// admitFunds is an admission's wait for the gate and a connection, as a call that start can run.
func admitFunds(ctx context.Context, f *Funds, account string) func() (func(), error) {
	return func() (func(), error) {
		_, release, err := f.Admit(ctx, account)
		return release, err
	}
}
