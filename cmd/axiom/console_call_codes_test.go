package main

import (
	"fmt"
	"log/slog"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/observability"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
)

func TestCallDetailResolvesLiveAndRetiredCodenames(t *testing.T) {
	t.Parallel()
	f := newPublicationView(t)
	if len(f.endpoints) < 2 {
		t.Fatal("need two routable endpoints")
	}
	live, retired := f.endpoints[0], f.endpoints[1]
	console := startNode(f.world.commandLine, &testClock{now: time.Now()})
	session := console.signIn("rich@example.com", ownerPassword)
	account, _ := console.account(session)
	rec := newCall(account, time.Now().UTC().Truncate(time.Millisecond), "model", "cli")
	route, err := jsonx.MarshalCanonical(map[string]any{"route": []string{live.ID.String(), retired.ID.String()}})
	if err != nil {
		t.Fatal(err)
	}
	rec.Route = route
	rec.Attempts = []observability.AttemptRecord{
		{Seq: 1, Endpoint: live.ID, Supplier: live.Supplier, SourceClass: "relay", StartedAt: rec.At, FinishedAt: rec.At.Add(time.Second), Outcome: observability.AttemptFailed, ErrorKind: "timeout"},
		{Seq: 2, Endpoint: retired.ID, Supplier: retired.Supplier, SourceClass: "relay", StartedAt: rec.At.Add(time.Second), FinishedAt: rec.At.Add(2 * time.Second), Outcome: observability.AttemptCommitted},
	}
	console.record(rec)
	var liveBody consoleapi.Call
	api := consoleAPI{upstreams: f.node.upstreams}
	api.callRoutingBody(observability.OwnCallDetail{Attempts: []observability.OwnAttempt{
		{Endpoint: live.ID.String()}, {Endpoint: retired.ID.String()},
	}, Route: &observability.OwnRoute{SelectedEndpoint: retired.ID.String()}}, &liveBody, true)
	if diff := cmp.Diff([]*string{new(live.Codename), new(retired.Codename), new(retired.Codename)},
		[]*string{(*liveBody.Attempts)[0].EndpointCode, (*liveBody.Attempts)[1].EndpointCode, liveBody.Route.SelectedEndpoint}); diff != "" {
		t.Fatalf("two live endpoints (-want +got): %s", diff)
	}
	if err := pgtest.Run(t.Context(), f.world.database.Superuser, fmt.Sprintf("UPDATE suppliers.endpoints SET enabled=false WHERE id='%s'", retired.ID.String())); err != nil {
		t.Fatal(err)
	}
	if err := f.node.upstreams.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	var detail observability.OwnCallDetail
	if err := console.db.ReadTx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{account}), func(tx pg.Tx) (err error) {
		call, err := observability.GetOwn(t.Context(), tx, rec.ID)
		if err != nil {
			return err
		}
		detail, err = observability.ReadOwnDetail(t.Context(), tx, call, slog.New(slog.DiscardHandler))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	body := consoleapi.Call{}
	(consoleAPI{upstreams: f.node.upstreams}).callRoutingBody(detail, &body, true)
	if body.Attempts == nil || len(*body.Attempts) != 2 || body.Route == nil {
		t.Fatalf("missing detail: %+v", body)
	}
	got := []*string{(*body.Attempts)[0].EndpointCode, (*body.Attempts)[1].EndpointCode, body.Route.SelectedEndpoint}
	want := []*string{new(live.Codename), nil, new(live.Codename)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("public codenames (-want +got): %s", diff)
	}
}

func TestCallRoutingCostDoesNotGrowWithPlatform(t *testing.T) {
	f := newPublicationView(t)
	f.world.addFailureCredential(uuid.MustParse(f.world.query("SELECT supplier_group_id::text FROM suppliers.credentials LIMIT 1")), "scale-original")
	api := consoleAPI{upstreams: f.node.upstreams}
	call := observability.OwnCallDetail{
		Attempts: []observability.OwnAttempt{{Endpoint: f.endpoints[0].ID.String()}, {Endpoint: f.endpoints[1].ID.String()}},
		Route:    &observability.OwnRoute{SelectedEndpoint: f.endpoints[1].ID.String()},
	}
	var body consoleapi.Call
	var small float64
	count := len(f.endpoints)
	for _, size := range []int{10, 10000} {
		growCallCodeSnapshot(t, f, count, size)
		count = size
		if err := f.node.upstreams.Reload(t.Context()); err != nil {
			t.Fatal(err)
		}
		endpoints, err := f.node.upstreams.Routable()
		if err != nil || len(endpoints) != size {
			t.Fatalf("snapshot size = %d, want %d: %v", len(endpoints), size, err)
		}
		if got := f.world.query("SELECT count(*)::text FROM suppliers.credentials"); got != fmt.Sprint(size) {
			t.Fatalf("credentials = %s, want %d", got, size)
		}
		allocs := testing.AllocsPerRun(20, func() {
			api.callRoutingBody(call, &body, true)
		})
		t.Logf("endpoints=%d credentials=%d: %.0f allocs/detail", size, size, allocs)
		if size == 10 {
			small = allocs
		} else if allocs != small {
			t.Fatalf("detail allocations grew: small %.0f, large %.0f", small, allocs)
		}
	}
	if diff := cmp.Diff([]*string{new(f.endpoints[0].Codename), new(f.endpoints[1].Codename)},
		[]*string{(*body.Attempts)[0].EndpointCode, (*body.Attempts)[1].EndpointCode}); diff != "" {
		t.Fatalf("scaled detail (-want +got): %s", diff)
	}
}

func growCallCodeSnapshot(t *testing.T, f publicationViewFixture, from, to int) {
	t.Helper()
	keys := masterKeys(t)
	if err := f.world.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		for i := from; i < to; i++ {
			group, credential, endpoint := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
			code := fmt.Sprintf("scale-%d", i)
			sealed := keys.Seal(secret.New("scale-key"), secret.Location{Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key"})
			_, err := tx.Exec(t.Context(), `INSERT INTO suppliers.supplier_groups
(id, supplier_id, upstream_ref, name, ratio, selected, codename)
VALUES ($1, $2, $3, $3, 1, true, $3)`, group, f.endpoints[0].Supplier, code)
			if err != nil {
				return err
			}
			_, err = tx.Exec(t.Context(), `INSERT INTO suppliers.credentials
(id, supplier_id, supplier_group_id, upstream_key_ref, ciphertext, nonce, wrapped_dek, key_version, status, max_concurrency)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'active', 4)`, credential, f.endpoints[0].Supplier, group, code,
				sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, int64(sealed.KeyVersion))
			if err != nil {
				return err
			}
			_, err = tx.Exec(t.Context(), `INSERT INTO suppliers.endpoints
(id, supplier_id, supplier_group_id, model_id, upstream_name, formats)
SELECT $3, supplier_id, $1, model_id, upstream_name, formats FROM suppliers.endpoints WHERE id=$2`, group, f.endpoints[0].ID, endpoint)
			if err != nil {
				return err
			}
			_, err = tx.Exec(t.Context(), `WITH cost AS (
INSERT INTO suppliers.cost_schedules (supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
SELECT c.supplier_id, $1, c.native_unit, c.schedule, c.basis, c.fingerprint
FROM suppliers.cost_schedules c JOIN suppliers.endpoints e ON e.cost_schedule_id=c.id WHERE e.id=$2
RETURNING id)
UPDATE suppliers.endpoints SET cost_state='known', cost_schedule_id=cost.id FROM cost WHERE endpoints.id=$1`, endpoint, f.endpoints[0].ID)
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
