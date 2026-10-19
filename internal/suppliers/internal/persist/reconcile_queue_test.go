package persist_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
)

// reconcileLog synchronizes the queue's writer with the test's reader.
type reconcileLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *reconcileLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *reconcileLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A charge ingested by the real sync commit is processed by the registered River
// job. Its delisting and alert survive another sync until a different plan arrives.
func TestQueuedReconciliationSurvivesResync(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	c.model("gpt-4o", catalog.StatusListed)
	s := c.acquire(c.node("a"))
	groups := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
	}}}
	c.commitListing(s, 1, groups)
	endpoint := c.seedUnderEstimate()
	commit := listing(2, groups)
	commit.Charges = []adapter.Charge{{Ref: "under", Key: "test", Amount: "400", Funding: adapter.FundingWallet, OccurredAt: t0, Model: "gpt-4o"}}
	if err := s.Store().Commit(t.Context(), 1, commit); err != nil {
		t.Fatal(err)
	}
	logs := &reconcileLog{}
	c.runReconciliationQueue(logs)
	if text := logs.String(); !strings.Contains(text, "suppliers.reconcile.under_estimated") || !strings.Contains(text, endpoint.String()) || !strings.Contains(text, "level=ERROR") {
		t.Fatalf("missing endpoint alert: %s", text)
	}
	same(t, "delisted", c.column("endpoints", "cost_state || ' ' || cost_state_reason", "id"), []string{"unknown under_estimated"})
	c.commitListing(s, 3, groups)
	same(t, "same fingerprint", c.column("endpoints", "cost_state || ' ' || cost_state_reason", "id"), []string{"unknown under_estimated"})
	groups[0].models[0].plan = &plusVAT
	c.commitListing(s, 4, groups)
	same(t, "new fingerprint", c.column("endpoints", "cost_state || ' ' || coalesce(cost_state_reason, '-')", "id"), []string{"known -"})
}

func (c *cluster) seedUnderEstimate() uuid.UUID {
	c.t.Helper()
	var endpoint uuid.UUID
	err := c.open().Tx(c.t.Context(), platform, func(tx pg.Tx) error {
		var group, schedule uuid.UUID
		if err := tx.QueryRow(c.t.Context(), `SELECT id, supplier_group_id, cost_schedule_id FROM suppliers.endpoints
			WHERE supplier_id = $1`, c.supplier).Scan(&endpoint, &group, &schedule); err != nil {
			return err
		}
		key := uuid.NewV7()
		if err := store.New().InsertCredential(c.t.Context(), tx, store.InsertCredentialParams{
			ID: key, SupplierID: c.supplier, SupplierGroupID: group, UpstreamKeyRef: "test", Status: "binding", MaxConcurrency: 1,
		}); err != nil {
			return err
		}
		_, err := tx.Exec(c.t.Context(), `INSERT INTO suppliers.attempt_costs
			(request_id, attempt_seq, supplier_id, credential_id, endpoint_id, cost_schedule_id,
			 charge_ref, usage, priced_at, finished_at, cost_flag, estimated_native)
			VALUES ($1, 1, $2, $3, $4, $5, 'under', '{}', $6, $6, 'complete', 22.5)`,
			uuid.NewV7(), c.supplier, key, endpoint, schedule, t0)
		return err
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return endpoint
}

func (c *cluster) runReconciliationQueue(logs *reconcileLog) {
	c.t.Helper()
	db := c.open()
	q := db.NewQueue(slog.New(slog.DiscardHandler), platform, nil)
	if err := suppliers.RegisterReconcile(suppliers.ReconcileJobConfig{
		DB: db, Scope: platform, Queue: q, Logger: slog.New(slog.NewTextHandler(logs, nil)), Clock: c.clock.now,
	}); err != nil {
		c.t.Fatal(err)
	}
	if err := q.Start(c.t.Context()); err != nil {
		c.t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.t.Context()), 10*time.Second)
		defer cancel()
		if err := q.Stop(ctx); err != nil {
			c.t.Errorf("stop queue: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(c.t.Context(), 30*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var completed bool
		err := db.ReadTx(ctx, platform, func(tx pg.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM river.river_job
				WHERE kind = 'suppliers.reconcile' AND state = 'completed')`).Scan(&completed)
		})
		if err != nil {
			c.t.Fatal(err)
		}
		if completed {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			c.t.Fatalf("reconciliation never completed: %s", logs.String())
		}
	}
}
