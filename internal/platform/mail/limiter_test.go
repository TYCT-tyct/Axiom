package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
	"axiom/internal/platform/secret"
)

// stepClock is a controlled clock: a test advances it instead of waiting for the wall clock. The offset to the server
// keeps the client near its time while letting the test's advances move it (internal/platform/kv/README.md「时钟」).
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func newStepClock() *stepClock { return &stepClock{now: time.Now()} }

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// clientClock is the clock a test's cache client measures its offset from: the test's frozen value, moved up by how long
// the setup took, so that the client opens within the second the startup check allows.
func clientClock(c *stepClock) func() time.Time {
	offset := time.Since(c.Now())
	return func() time.Time { return c.Now().Add(offset) }
}

// testMasterKeys is the master-key snapshot the mail keys are derived from, the same on every node: one that differed
// would put one recipient in two buckets.
func testMasterKeys(t *testing.T) *secret.MasterKeys {
	t.Helper()
	return masterKeysFrom(t, "1:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", mailKeyLen))))
}

// masterKeysFrom writes version:base64 lines to a protected file and loads them as one snapshot.
func masterKeysFrom(t *testing.T, lines ...string) *secret.MasterKeys {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	master, err := secret.LoadMaster(path)
	if err != nil {
		t.Fatal(err)
	}
	return master
}

// testMailKey is the HMAC key every test's throttle shares, as two nodes of one deployment do.
func testMailKey(t *testing.T) [mailKeyLen]byte {
	t.Helper()
	key, err := deriveMailKey(testMasterKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestMailKeyUsesTheHighestDistributedVersion(t *testing.T) {
	t.Helper()
	key := func(fill byte) string {
		return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(fill), mailKeyLen)))
	}
	multi, err := deriveMailKey(masterKeysFrom(t, "1:"+key('a'), "7:"+key('b')))
	if err != nil {
		t.Fatal(err)
	}
	highest, err := deriveMailKey(masterKeysFrom(t, "7:"+key('b')))
	if err != nil {
		t.Fatal(err)
	}
	lowest, err := deriveMailKey(masterKeysFrom(t, "1:"+key('a')))
	if err != nil {
		t.Fatal(err)
	}
	if multi != highest {
		t.Fatal("the mail key is not the highest distributed version's")
	}
	if multi == lowest {
		t.Fatal("the mail key is the lowest version's")
	}
}

func sharedThrottle(cache *kv.Client, key [mailKeyLen]byte) *throttle {
	th := newThrottle()
	th.cache = cache
	th.key = key
	return th
}

// TestSharedRecipientThrottleAcrossNodes: two nodes admit one case-insensitive recipient together, and an address is
// independent of another's. The clock, not the wall, decides when the interval has passed.
func TestSharedRecipientThrottleAcrossNodes(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	namespace := kvtest.Namespace()
	clock := newStepClock()
	now := clientClock(clock)
	key := testMailKey(t)
	a := sharedThrottle(server.OpenIn(t, namespace, kv.Deps{Now: now}), key)
	b := sharedThrottle(server.OpenIn(t, namespace, kv.Deps{Now: now}), key)

	if !a.allow(t.Context(), "User@Example.com") {
		t.Fatal("the first message to a recipient was refused")
	}
	if b.allow(t.Context(), "user@example.com") {
		t.Error("another node admitted the same address in another case")
	}
	if !b.allow(t.Context(), "other@example.com") {
		t.Error("a message to another recipient was refused")
	}
	// A second of margin on either side of the interval: the two clients measured their own small offset from the
	// server's clock when they opened, and the test must not turn that measurement noise into a failure.
	clock.Add(rateInterval - time.Second)
	if b.allow(t.Context(), "USER@example.com") {
		t.Error("a message inside the interval was admitted")
	}
	clock.Add(2 * time.Second)
	if !b.allow(t.Context(), "user@example.com") {
		t.Error("a message after the interval was refused")
	}
}

// TestSharedRecipientThrottleConcurrentAdmission: many workers on two nodes ask for the same recipient at once, and
// exactly one admission is recorded. The script's check and write must be one step for this to hold.
func TestSharedRecipientThrottleConcurrentAdmission(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	namespace := kvtest.Namespace()
	key := testMailKey(t)
	nodes := []*throttle{
		sharedThrottle(server.OpenIn(t, namespace, kv.Deps{}), key),
		sharedThrottle(server.OpenIn(t, namespace, kv.Deps{}), key),
	}

	const callers = 32
	results := make(chan bool, callers)
	var wg sync.WaitGroup
	for i := range callers {
		node := nodes[i%len(nodes)]
		wg.Go(func() { results <- node.allow(t.Context(), "User@Example.com") })
	}
	wg.Wait()
	close(results)
	allowed := 0
	for ok := range results {
		if ok {
			allowed++
		}
	}
	if allowed != 1 {
		t.Fatalf("%d of %d concurrent admissions won; want exactly 1", allowed, callers)
	}
}

// TestRecipientThrottleFallsBackWhenSharedStoreIsDown: a node whose Valkey is away still sends, at most one message per
// recipient per interval, and two such nodes allow one each. That is the bounded degradation the README documents.
func TestRecipientThrottleFallsBackWhenSharedStoreIsDown(t *testing.T) {
	t.Parallel()
	clock := newStepClock()
	key := testMailKey(t)
	a := sharedThrottle(deadCache(t, clientClock(clock)), key)
	b := sharedThrottle(deadCache(t, clientClock(clock)), key)
	a.local.now, b.local.now = clock.Now, clock.Now

	for i, node := range []*throttle{a, b} {
		if !node.allow(t.Context(), "User@Example.com") {
			t.Fatalf("node %d refused its first message during the outage", i)
		}
		if node.allow(t.Context(), "user@example.com") {
			t.Errorf("node %d admitted the same recipient twice during the outage", i)
		}
	}
	clock.Add(rateInterval)
	if !a.allow(t.Context(), "user@example.com") {
		t.Error("the fallback did not free the recipient after the interval")
	}
}

// deadCache is a client of a server that is not there: the node starts through the outage, and every access fails into
// the caller's fallback.
func deadCache(t *testing.T, now func() time.Time) *kv.Client {
	t.Helper()
	file := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(file, []byte(strings.Repeat("p", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A refused connection exercises the startup fallback without racing a healthy server against the production budget.
	cache, err := kv.Open(t.Context(), kv.Config{URL: "valkey://axiom@127.0.0.1:1", PasswordFile: file, Nodes: 1},
		kvtest.Deps(kv.Deps{Now: now}))
	if err != nil {
		t.Fatalf("a node must start with its shared store away: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		if err := cache.Close(ctx); err != nil {
			t.Errorf("close the cache: %v", err)
		}
	})
	return cache
}

// TestRecipientKeyHidesTheAddressAndExpires: the shared key is a hash, has the one-minute lifetime, and lists no
// address even to a reader with every permission. A server of the test's own is needed for that reader.
func TestRecipientKeyHidesTheAddressAndExpires(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	cache := server.Open(t, kv.Deps{})
	node := sharedThrottle(cache, testMailKey(t))
	const recipient = "User@Example.com"
	key := node.keyFor(recipient)
	if strings.Contains(key, "@") || strings.Contains(key, strings.ToLower(recipient)) {
		t.Fatalf("the key %q carries the address", key)
	}
	if !node.allow(t.Context(), recipient) {
		t.Fatal("the first message was refused")
	}
	keys, err := server.Do(t, t.Context(), server.Admin.B().Keys().Pattern(cache.Key(kv.MailSpace, uuid.Nil(), "*")).Build())[0].AsStrSlice()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != key {
		t.Fatalf("the shared store holds %v; want only %q", keys, key)
	}
	life, err := server.Do(t, t.Context(), server.Admin.B().Pttl().Key(key).Build())[0].AsInt64()
	if err != nil || life <= 0 || life > rateInterval.Milliseconds() {
		t.Fatalf("the key lives %d ms, err %v; want at most one interval", life, err)
	}
}

// TestSendRetriesAfterFailure: a send that fails still spends the recipient's admission, so an immediate retry is
// refused without opening a connection, and the retry after the interval is admitted again.
func TestSendRetriesAfterFailure(t *testing.T) {
	t.Parallel()
	server := startTLSServer(t, func(s *fakeServer) {
		s.rejectStep, s.rejectCode = "rcpt", 451
	})
	sd := server.openSender(t, Config{Host: "127.0.0.1", Port: 587})
	clock := newStepClock()
	sd.throttle = sharedThrottle(kvtest.New(t).Open(t, kv.Deps{Now: clientClock(clock)}), testMailKey(t))

	if _, err := sd.Send(t.Context(), testMessage); err == nil || errors.Is(err, ErrRateLimited) {
		t.Fatalf("first Send() error = %v; want the server's temporary refusal", err)
	}
	if _, err := sd.Send(t.Context(), testMessage); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("retry inside the interval: error = %v; want ErrRateLimited", err)
	}
	clock.Add(rateInterval + time.Second)
	if _, err := sd.Send(t.Context(), testMessage); errors.Is(err, ErrRateLimited) {
		t.Error("the retry after the interval was still rate limited")
	}
}

// TestSharedAdmissionSurvivesTheStoreGoingAway: an admission the shared store granted is also held in this node's
// bucket, so a send or a failed send followed by an outage still waits out the interval. The local clock moves, not the
// wall, and the retry is refused before it opens a connection.
func TestSharedAdmissionSurvivesTheStoreGoingAway(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		rejectStep string
	}{
		{"successful send", ""},
		{"failed send", "rcpt"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := startTLSServer(t, func(s *fakeServer) {
				s.rejectStep, s.rejectCode = tt.rejectStep, 451
			})
			sd := server.openSender(t, Config{Host: "127.0.0.1", Port: 587})
			clock := newStepClock()
			sd.throttle = sharedThrottle(kvtest.New(t).Open(t, kv.Deps{Now: clientClock(clock)}), testMailKey(t))
			sd.throttle.local.now = clock.Now

			_, err := sd.Send(t.Context(), testMessage)
			wantErr := tt.rejectStep != ""
			if (err != nil) != wantErr || errors.Is(err, ErrRateLimited) {
				t.Fatalf("first Send() error = %v; want SMTP failure = %t", err, wantErr)
			}

			// The store goes away between the send and the retry: the node must keep the shared admission it just made.
			sd.throttle.cache = deadCache(t, clientClock(clock))
			if _, err := sd.Send(t.Context(), testMessage); !errors.Is(err, ErrRateLimited) {
				t.Fatalf("retry after the store went away: error = %v; want ErrRateLimited", err)
			}
			clock.Add(rateInterval - time.Second)
			if _, err := sd.Send(t.Context(), testMessage); !errors.Is(err, ErrRateLimited) {
				t.Errorf("retry inside the interval after the store went away: error = %v; want ErrRateLimited", err)
			}
			clock.Add(2 * time.Second)
			if _, err := sd.Send(t.Context(), testMessage); errors.Is(err, ErrRateLimited) {
				t.Error("the retry after the interval was still rate limited")
			}
		})
	}
}
