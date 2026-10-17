package idem_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/idem"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var platform = pg.NewScope(true, nil, nil)

// response stands for an operation's response: what is stored must read back as what was answered.
type response struct {
	ID     string   `json:"id"`
	Amount string   `json:"amount"`
	Tags   []string `json:"tags,omitempty"`
}

func open(t *testing.T) *pg.DB {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func claim(t *testing.T, key uuid.UUID, actor, operation string, request any) idem.Claim {
	t.Helper()
	fp, err := idem.Fingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	return idem.Claim{Key: key, Actor: audit.System(actor), Operation: operation, Fingerprint: fp}
}

// once runs idem.Once for c in its own platform transaction; done counts the calls of the change.
func once(t *testing.T, db *pg.DB, c idem.Claim, done *atomic.Int32, answer response) (response, bool, error) {
	t.Helper()
	var got response
	var replayed bool
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) (err error) {
		got, replayed, err = idem.Once(t.Context(), tx, c, func() (response, error) {
			done.Add(1)
			return answer, nil
		})
		return err
	})
	return got, replayed, err
}

// TestOnceReplaysTheFirstResponse: the first request with a key does the change and stores its response; the same
// request again gets that response and does nothing, whatever the change would answer now.
func TestOnceReplaysTheFirstResponse(t *testing.T) {
	t.Parallel()
	db := open(t)
	var done atomic.Int32
	c := claim(t, uuid.NewV7(), "tester", "grantCredit", map[string]any{"amount": "5", "reason": "r"})
	first := response{ID: "a", Amount: "5", Tags: []string{"x", "y"}}
	got, replayed, err := once(t, db, c, &done, first)
	if err != nil || replayed || got.ID != "a" {
		t.Fatalf("first: %+v %v %v", got, replayed, err)
	}
	got, replayed, err = once(t, db, c, &done, response{ID: "b"})
	if err != nil || !replayed || got.ID != "a" || got.Amount != "5" || len(got.Tags) != 2 {
		t.Errorf("again: %+v replayed=%v %v, want the first response replayed", got, replayed, err)
	}
	if n := done.Load(); n != 1 {
		t.Errorf("the change ran %d times, want 1", n)
	}
}

// TestOnceRefusesAKeyUsedForAnotherRequest: a key is bound to its actor, its operation and its request, so a replay
// cannot be had by another person, nor a different change under the same key; the refusal changes nothing, and the
// first request still replays.
func TestOnceRefusesAKeyUsedForAnotherRequest(t *testing.T) {
	t.Parallel()
	db := open(t)
	var done atomic.Int32
	key := uuid.NewV7()
	request := map[string]any{"amount": "5"}
	if _, _, err := once(t, db, claim(t, key, "alice", "grantCredit", request), &done, response{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]idem.Claim{
		"another actor":     claim(t, key, "bob", "grantCredit", request),
		"another operation": claim(t, key, "alice", "createSupplier", request),
		"another request":   claim(t, key, "alice", "grantCredit", map[string]any{"amount": "6"}),
	} {
		if _, _, err := once(t, db, c, &done, response{ID: "z"}); !errors.Is(err, idem.ErrReused) {
			t.Errorf("%s: %v, want ErrReused", name, err)
		}
	}
	got, replayed, err := once(t, db, claim(t, key, "alice", "grantCredit", request), &done, response{})
	if err != nil || !replayed || got.ID != "a" || done.Load() != 1 {
		t.Errorf("the first request again: %+v %v %v after %d runs", got, replayed, err, done.Load())
	}
}

// TestFingerprintIgnoresSpelling: the same content is the same request however it was ordered.
func TestFingerprintIgnoresSpelling(t *testing.T) {
	t.Parallel()
	fingerprint := func(parts ...any) [32]byte {
		t.Helper()
		fp, err := idem.Fingerprint(parts...)
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}
	a := fingerprint("p", map[string]any{"a": 1, "b": []int{1, 2}})
	b := fingerprint("p", map[string]any{"b": []int{1, 2}, "a": 1})
	c := fingerprint("p", map[string]any{"a": 1, "b": []int{2, 1}})
	if a != b || a == c {
		t.Errorf("fingerprints: same content %x vs %x, other content %x", a, b, c)
	}
}

// TestAFailedChangeLeavesNoClaim: when the change fails its transaction rolls back with the claim, so the caller may
// send the key again, corrected, and it is done then.
func TestAFailedChangeLeavesNoClaim(t *testing.T) {
	t.Parallel()
	db := open(t)
	c := claim(t, uuid.NewV7(), "tester", "grantCredit", "r")
	failure := errors.New("refused")
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, _, err := idem.Once(t.Context(), tx, c, func() (response, error) { return response{}, failure })
		return err
	})
	if !errors.Is(err, failure) {
		t.Fatalf("the failing change: %v", err)
	}
	var done atomic.Int32
	if got, replayed, err := once(t, db, c, &done, response{ID: "ok"}); err != nil || replayed || got.ID != "ok" || done.Load() != 1 {
		t.Errorf("after the failure: %+v replayed=%v %v after %d runs", got, replayed, err, done.Load())
	}
}

// TestConcurrentRequestsWithOneKeyRunOnce: requests that arrive together wait on the claim, and all get the one
// response while the change runs once.
func TestConcurrentRequestsWithOneKeyRunOnce(t *testing.T) {
	t.Parallel()
	db := open(t)
	c := claim(t, uuid.NewV7(), "tester", "grantCredit", "r")
	var done atomic.Int32
	const requests = 8
	var wg sync.WaitGroup
	replays := make(chan bool, requests)
	for range requests {
		wg.Go(func() {
			got, replayed, err := once(t, db, c, &done, response{ID: "same"})
			if err != nil || got.ID != "same" {
				t.Errorf("a request: %+v %v", got, err)
			}
			replays <- replayed
		})
	}
	wg.Wait()
	close(replays)
	var firsts int
	for replayed := range replays {
		if !replayed {
			firsts++
		}
	}
	if done.Load() != 1 || firsts != 1 {
		t.Errorf("the change ran %d times and %d requests were not replays, want 1 each", done.Load(), firsts)
	}
}

// TestOnlyThePlatformScopeSeesClaims: the table is the platform's; a request in any other scope can neither claim nor
// read a key.
func TestOnlyThePlatformScopeSeesClaims(t *testing.T) {
	t.Parallel()
	db := open(t)
	var done atomic.Int32
	c := claim(t, uuid.NewV7(), "tester", "grantCredit", "r")
	if _, _, err := once(t, db, c, &done, response{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	tenant := pg.NewScope(false, []uuid.UUID{uuid.NewV7()}, []uuid.UUID{uuid.NewV7()})
	err := db.Tx(t.Context(), tenant, func(tx pg.Tx) error {
		_, _, err := idem.Once(t.Context(), tx, claim(t, uuid.NewV7(), "tester", "grantCredit", "r"), func() (response, error) { return response{}, nil })
		return err
	})
	if err == nil {
		t.Error("a claim in a tenant's scope was accepted")
	}
	var n int
	if err := db.Tx(t.Context(), tenant, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM platform.idempotency").Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("a tenant's scope sees %d claims (%v), want none", n, err)
	}
}
