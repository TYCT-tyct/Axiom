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
