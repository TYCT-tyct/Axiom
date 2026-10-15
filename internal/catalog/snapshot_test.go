package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/pgtest"
)

// current refreshes the catalog and returns its snapshot.
func (s *store) current() *catalog.Snapshot {
	s.t.Helper()
	if err := s.c.Refresh(s.t.Context()); err != nil {
		s.t.Fatal(err)
	}
	snap, err := s.c.Current()
	if err != nil {
		s.t.Fatal(err)
	}
	return snap
}

// TestSnapshot: only listed and retired models are in it, with the current price of each currency of the listed
// ones; a draft, its names and its prices are not.
func TestSnapshot(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := t.Context()
	if _, err := s.c.Current(); !errors.Is(err, catalog.ErrStale) || s.c.Ready() {
		t.Fatalf("before the first load: %v, ready %v", err, s.c.Ready())
	}
	listed, usd := s.seed("acme/listed", basicSpec, basic)
	retired, _ := s.seed("acme/retired", basicSpec, basic)
	draft, _ := s.seed("acme/draft", basicSpec, basic)
	s.list(listed)
	s.list(retired)
	s.must(s.c.Retire(ctx, catalog.ModelRequest{Op: op(), Model: retired, Rev: 1}))
	cheaper := `{"v":1,"currency":"USD","rates":{"input":"0.0000005","output":"0.000002"}}`
	s.must(s.c.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: listed, Base: &usd, Source: catalog.SourceManual, Schedule: raw(cheaper), Evidence: raw(evidence),
	}))
	snap := s.current()
	if !s.c.Ready() {
		t.Error("not ready after a load")
	}
	var models []catalog.ModelID
	for m := range snap.Models() {
		models = append(models, m.ID)
	}
	if diff := cmp.Diff([]catalog.ModelID{listed}, models); diff != "" {
		t.Errorf("Models() (-want +got):\n%s", diff)
	}
	status := func(id catalog.ModelID) catalog.Status {
		m, _ := snap.Model(id) // the zero Model, with no status, when absent
		return m.Status
	}
	if got := [3]catalog.Status{status(listed), status(retired), status(draft)}; got != [3]catalog.Status{catalog.StatusListed, catalog.StatusRetired, ""} {
		t.Errorf("statuses of the listed, retired and draft models in the snapshot: %q", got)
	}
	priced := func(id catalog.ModelID) uuid.UUID {
		p, _ := snap.ReferencePrice(id, "USD") // the zero price, with no id, when absent
		return p.ID
	}
	if got := [2]uuid.UUID{priced(listed), priced(retired)}; got != [2]uuid.UUID{s.head(listed, "USD"), {}} {
		t.Errorf("prices of the listed and retired models: %v, want the newest version of the first only", got)
	}
	if snap.Revision() != 4 { // list, list, retire, and the price of a listed model
		t.Errorf("revision %d", snap.Revision())
	}
}

// namesFixture lists a dated model with three names and a floating one, and returns the store with them.
func namesFixture(t *testing.T) (s *store, dated, floating catalog.ModelID) {
	t.Helper()
	s = newStore(t)
	ctx := t.Context()
	dated, _ = s.seed("acme/claude-sonnet-4.5@20250929", basicSpec, basic)
	for _, n := range []struct {
		name string
		v    catalog.Visibility
	}{
		{"claude-sonnet-4-5-20250929", catalog.VisibilityPublic},
		{"claude-sonnet-4-5", catalog.VisibilityPublic},
		{"claude-sonnet-4-5-latest", catalog.VisibilityInternal},
	} {
		s.must(s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: dated, Name: n.name, Visibility: n.v}))
	}
	s.must(s.c.SaveAuthor(ctx, catalog.SaveAuthorRequest{Op: op(), Slug: "deepseek", Name: "DeepSeek"}))
	floating = s.model(s.must(s.c.CreateModel(ctx, catalog.CreateModelRequest{
		Op: op(), Slug: "deepseek/v4-flash", VersionBasis: catalog.VersionFloating, PriceSource: catalog.SourceManual, Spec: raw(basicSpec),
	})))
	s.must(s.c.AddName(ctx, catalog.AddNameRequest{Op: op(), Model: floating, Name: "deepseek/v4-flash", Visibility: catalog.VisibilityPublic}))
	s.must(s.c.PublishPrice(ctx, catalog.PublishPriceRequest{
		Op: op(), Model: floating, Source: catalog.SourceManual, Schedule: raw(basic), Evidence: raw(evidence),
	}))
	s.list(dated)
	s.list(floating)
	return s, dated, floating
}

// TestResolve: users reach public names only, in any spelling; a ':' never reaches a base model.
func TestResolve(t *testing.T) {
	t.Parallel()
	s, dated, floating := namesFixture(t)
	snap := s.current()
	resolved := []struct {
		name  string
		model catalog.ModelID
		float bool
	}{
		{"acme/claude-sonnet-4.5", dated, false},
		{"ACME/Claude-Sonnet-4.5", dated, false},
		{"claude-sonnet-4-5", dated, false},
		{"claude-sonnet-4.5-20250929", dated, false},
		{"claude-sonnet-4-5-2025-09-29", dated, false},
		{"deepseek/v4-flash", floating, true},
	}
	for _, c := range resolved {
		if got, err := snap.ResolvePublic(c.name); err != nil || got != (catalog.Resolution{Model: c.model, Floating: c.float}) {
			t.Errorf("%q: %+v, %v", c.name, got, err)
		}
	}
	for _, name := range []string{"claude-sonnet-4-5-latest", "claude-sonnet-4-5:free", "acme/claude-sonnet-4.5:batch", "nothing", "", "~x"} {
		if _, err := snap.ResolvePublic(name); !errors.Is(err, catalog.ErrModelNotFound) {
			t.Errorf("%q: %v, want not found", name, err)
		}
	}
}

// TestRecognize: internal names are recognized too; a name is evidence of the version when it carries a date or the
// model's version is the name; a retired model's names answer "retired" and are still recognized.
func TestRecognize(t *testing.T) {
	t.Parallel()
	s, dated, _ := namesFixture(t)
	snap := s.current()
	recognized := []struct {
		name  string
		exact bool
	}{
		{"claude-sonnet-4-5-20250929", true}, // a date
		{"claude-sonnet-4-5", false},         // may have moved to another version
		{"claude-sonnet-4-5-latest", false},  // internal names are recognized too
		{"deepseek/v4-flash", true},          // the model is the name
	}
	for _, c := range recognized {
		if got, ok := snap.Recognize(c.name); !ok || got.Exact != c.exact {
			t.Errorf("Recognize(%q) = %+v, %v, want exact %v", c.name, got, ok, c.exact)
		}
	}
	if _, ok := snap.Recognize("claude-sonnet-4-5:free"); ok {
		t.Error("a suffixed name was recognized")
	}
	s.must(s.c.Retire(t.Context(), catalog.ModelRequest{Op: op(), Model: dated, Rev: 1}))
	snap = s.current()
	if _, err := snap.ResolvePublic("claude-sonnet-4-5"); !errors.Is(err, catalog.ErrRetired) {
		t.Errorf("a retired model's name: %v", err)
	}
	if got, ok := snap.Recognize("claude-sonnet-4-5"); !ok || got.Model != dated {
		t.Errorf("a retired model's name is not recognized: %+v, %v", got, ok)
	}
}

// TestResolveDoesNotAllocate: the call path of every request.
func TestResolveDoesNotAllocate(t *testing.T) {
	s := newStore(t)
	id, _ := s.seed("acme/claude-sonnet-4.5", basicSpec, basic)
	s.list(id)
	snap := s.current()
	for name, want := range map[string]error{
		"acme/claude-sonnet-4.5": nil, "ACME/Claude-Sonnet-4.5": nil, "acme/nothing:free": catalog.ErrModelNotFound,
	} {
		var err error
		if n := testing.AllocsPerRun(100, func() { _, err = snap.ResolvePublic(name) }); n != 0 {
			t.Errorf("ResolvePublic(%q) allocates %v times", name, n)
		}
		if !errors.Is(err, want) {
			t.Errorf("ResolvePublic(%q): %v, want %v", name, err, want)
		}
	}
}

// TestStaleness: a snapshot the database has not confirmed for longer than the limit stops resolving. Only a read of the
// same revision, or a successful load of a newer one, confirms it; a failed load does not.
func TestStaleness(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	s.c.SetClock(func() time.Time { return now })
	id, _ := s.seed("acme/alpha", basicSpec, basic)
	s.list(id)
	s.current()
	now = now.Add(29 * time.Second)
	if !s.c.Ready() {
		t.Fatal("not ready within the limit")
	}
	now = now.Add(2 * time.Second)
	if s.c.Ready() {
		t.Fatal("still ready 31 seconds after the last confirmation")
	}
	// The same revision, confirmed again: fresh.
	if err := s.c.Refresh(t.Context()); err != nil || !s.c.Ready() {
		t.Fatalf("after a confirming read: %v, ready %v", err, s.c.Ready())
	}
	// A higher revision whose load fails does not renew the old snapshot.
	super := s.database.Superuser
	run := func(sql ...string) {
		t.Helper()
		if err := pgtest.Run(t.Context(), super, sql...); err != nil {
			t.Fatal(err)
		}
	}
	run("UPDATE catalog.revision SET n = n + 1", "ALTER TABLE catalog.name RENAME TO name_away")
	now = now.Add(20 * time.Second)
	if err := s.c.Refresh(t.Context()); err == nil {
		t.Fatal("the load did not fail")
	}
	if !s.c.Ready() {
		t.Fatal("not ready 20 seconds in, within the limit")
	}
	now = now.Add(11 * time.Second)
	if s.c.Ready() {
		t.Fatal("a snapshot whose successor cannot be loaded stayed ready past the limit")
	}
	run("ALTER TABLE catalog.name_away RENAME TO name")
	if err := s.c.Refresh(t.Context()); err != nil || !s.c.Ready() {
		t.Fatalf("after recovery: %v, ready %v", err, s.c.Ready())
	}
}

// TestOlderSnapshotNeverReplacesNewer: two builds that finish in the opposite order of their revisions leave the newer.
func TestOlderSnapshotNeverReplacesNewer(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, _ := s.seed("acme/alpha", basicSpec, basic)
	older := s.current()
	s.list(id)
	newer := s.current()
	if older.Revision() >= newer.Revision() {
		t.Fatalf("revisions %d, %d", older.Revision(), newer.Revision())
	}
	s.c.Publish(older, time.Now().Add(time.Hour))
	got, err := s.c.Current()
	if err != nil || got != newer {
		t.Errorf("the older snapshot replaced the newer: %v, %v", got, err)
	}
}

func TestOpenRefusesAStalenessLimitBelowThePollingPeriod(t *testing.T) {
	t.Parallel()
	_, err := catalog.Open(nil, platformScope, catalog.Config{MaxStale: time.Second, PollEvery: 5 * time.Second})
	wantErr(t, err, "must exceed")
}

// TestRunFollowsOtherNodes: a node that only polls sees what another node listed, within a few polls, and Run returns
// when its context ends.
func TestRunFollowsOtherNodes(t *testing.T) {
	t.Parallel()
	writer := newStore(t)
	watcher, err := catalog.Open(writer.db, platformScope, catalog.Config{PollEvery: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx) }()
	waitFor(t, func() bool { return watcher.Ready() })
	id, _ := writer.seed("acme/alpha", basicSpec, basic)
	writer.list(id)
	waitFor(t, func() bool {
		snap, err := watcher.Current()
		if err != nil {
			return false
		}
		_, ok := snap.Model(id)
		return ok
	})
	stop()
	if err := <-done; err != nil {
		t.Errorf("Run returned %v", err)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
