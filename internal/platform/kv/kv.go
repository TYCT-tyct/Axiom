// Package kv is the node's access to the shared state in Valkey: the deadline and the breaker around every access, the
// clock correction, the heartbeat that makes a wrong node count visible, and the metrics. It holds no business rule: a
// module keeps its script and builds its keys here (README.md).
package kv

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"github.com/valkey-io/valkey-go"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/semaphore"

	"axiom/internal/platform/pg"
)

// Deps are what a client needs from the process it runs in, besides its settings.
type Deps struct {
	// DB is the node's database, for the heartbeat that tells a wrong N from a right one; nil writes no heartbeat,
	// which is what a test without a database wants. serve always gives it.
	DB *pg.DB
	// Scope is the platform scope the heartbeat table is read and written in, which only cmd/ builds.
	Scope  pg.Scope
	Meter  metric.Meter // where the client's instruments go; nil for none
	Logger *slog.Logger // nil for none
	// AccessTimeout is the deadline of an access, and zero is Timeout. A test of what the server answers, not of how fast,
	// widens it, so that a busy machine cannot decide the outcome.
	AccessTimeout time.Duration
	// HeartbeatTimeout bounds one round of the heartbeat's database work; zero is defaultHeartbeatTimeout. A test shortens it.
	HeartbeatTimeout time.Duration
	// Now is the local clock, and nil is time.Now. Tests set one that is wrong to see the clock check, and one they
	// advance to open and close the breaker without waiting (README「时钟」「熔断」). A client on such a clock keeps the
	// offset the start measured: a later measurement would read the test's own advances as skew and undo them.
	Now func() time.Time
}

// Client is the node's connection to the shared state: one Valkey, one batch of commands at a time, a deadline and a
// breaker around every access, and the heartbeat that makes a wrong N visible. It holds no business rule: a module keeps
// its script and builds its keys here (internal/platform/kv/README.md).
type Client struct {
	vk         valkey.Client // nil in single-core mode, where every use stays in the process
	ns         string
	now        func() time.Time // the local clock, which Now corrects by skew
	skew       atomic.Int64     // the server's clock less this one's, in nanoseconds
	testClock  bool             // now is a test's (Deps.Now): the loop does not measure the clock again
	live       atomic.Int64     // nodes the heartbeat sees alive, this one included
	config     int              // the configured N
	logger     *slog.Logger
	m          metrics
	br         *breaker
	slots      *semaphore.Weighted // the commands sent and not yet answered, below what a connection's ring holds (maxOutstanding)
	writeSlots *semaphore.Weighted // a subset of slots: sticky writes cannot fill the foreground budget
	hear       *heartbeat          // nil without a database: a test's client
	// hold is the node's name, held by a session of the database; nil when hear is. keepName replaces it while the loop
	// runs, and only then: Close and Crash read it after the loop has ended.
	hold    *pg.Hold
	lost    chan struct{} // closed when another session took the name (Lost); nil when hold is
	timeout time.Duration // the deadline of an access
	// beatTimeout bounds one round of the heartbeat's database work.
	beatTimeout time.Duration

	// deferred is set when the server was unreachable at startup: the checks that need one are owed, and the loop runs
	// them at the first answer and reports what they find (README「配置与启动」).
	deferred   atomic.Bool
	stop       context.CancelFunc
	done       sync.WaitGroup
	closing    sync.Once
	laterMu    sync.RWMutex // closes admission before the writer counts and discards the remaining queue
	closed     bool
	later      chan Cmd
	accounts   writeAccounts
	memoryStop atomic.Bool
}

// Open registers the node, connects to Valkey and checks it as far as the configuration allows (README「配置与启动」).
// Every node registers and keeps a heartbeat when it has a database, with or without Valkey, so that a second node
// against the same database is found whatever it is configured with. Without URLVar it returns the single-core client:
// Enabled is false, and every caller keeps its in-process implementation. A server that cannot be reached is not an
// error, and the node starts on its fallbacks; a server that refuses the user, has no memory limit, or disagrees about
// the time stops the start, because those are deployment mistakes.
func Open(ctx context.Context, cfg Config, deps Deps) (*Client, error) {
	return open(ctx, cfg, deps, deps.Now != nil)
}

// open is Open, with testClock saying whether deps.Now is a test's clock, whose offset the loop keeps (Deps.Now).
func open(ctx context.Context, cfg Config, deps Deps, testClock bool) (*Client, error) {
	deps.fillDefaults()
	namespace := cfg.Namespace
	if namespace == "" {
		namespace = DefaultNamespace
	}
	if err := cfg.Resolve(); err != nil {
		return nil, err
	}
	c := &Client{
		ns: namespace, now: deps.Now, testClock: testClock, config: cfg.Nodes, logger: deps.Logger, m: newMetrics(deps.Meter),
		br: newBreaker(), slots: semaphore.NewWeighted(maxOutstanding), writeSlots: semaphore.NewWeighted(stickyOutstanding),
		beatTimeout: deps.HeartbeatTimeout, timeout: deps.AccessTimeout,
	}
	c.live.Store(int64(cfg.Nodes))
	// The node registers first: a second node that reached this database, with Valkey or without, or an N set too low, is
	// found before the server is touched, and this node's row is written only once it may start.
	if deps.DB != nil {
		if err := c.register(ctx, cfg, deps); err != nil {
			return nil, err
		}
	}
	if cfg.URL != "" {
		if err := c.connect(ctx, cfg); err != nil {
			return nil, errors.Join(err, c.Close(ctx))
		}
	}
	if c.vk != nil || c.hear != nil {
		loop, stop := context.WithCancel(context.WithoutCancel(ctx))
		c.stop = stop
		if c.vk != nil {
			c.later = make(chan Cmd, laterCapacity)
			c.accounts.fullAt = make(map[uuid.UUID]time.Time)
			c.done.Go(func() { c.writeLater(loop) })
		}
		c.done.Go(func() { c.watch(loop) })
		if c.hold != nil {
			c.done.Go(func() { c.keepName(loop) })
		}
	}
	return c, nil
}

// register takes the node's name and writes its row. The name is held by a session of the database for as long as the
// node lives, so a second process under the same name is refused at once while the first is alive, and a node that
// crashed has lost it with its connection, so its restart is let in at once. The row says who is alive besides, and the
// count of the others is the check against N.
func (c *Client) register(ctx context.Context, cfg Config, deps Deps) error {
	id := cmp.Or(cfg.NodeID, uuid.New().String())
	hold, err := deps.DB.Hold(ctx, "axiom node "+id, holdEvery)
	if errors.Is(err, pg.ErrHeld) {
		return fmt.Errorf("%s: another live process already runs as node %q: give every node its own name, and a node that "+
			"restarts the one it had (internal/platform/kv/README.md「配置与启动」)", NodeIDVar, id)
	}
	if err != nil {
		return fmt.Errorf("kv: hold this node's name: %w", err)
	}
	c.hold, c.lost, c.hear = hold, make(chan struct{}), newHeartbeat(deps.DB, deps.Scope, id, cfg.Keys)
	if err := c.hear.start(ctx, cfg.Nodes, &c.live, c.m); err != nil {
		return errors.Join(err, hold.Release(ctx))
	}
	return nil
}

// Lost is closed when the node has lost its name: another process runs as this node, so this one must stop serving. It
// never closes for a client without a database, nor because the database was out (keepName).
func (c *Client) Lost() <-chan struct{} { return c.lost }

// keepName keeps the node's name for as long as the loop runs. A connection that is lost does not lose the name: the
// database restarted or was cut off, which the node serves through on what it holds in memory (docs/performance.md), and
// nobody can take the name from a database nobody reaches. So the node takes the name back on a new connection once the
// database answers, and only a name another session holds by then closes Lost.
func (c *Client) keepName(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.hold.Lost():
		}
		c.logger.WarnContext(ctx, "kv: the connection that holds this node's name was lost; the node serves on and takes the name back once the database answers")
		hold, err := c.retake(ctx)
		if errors.Is(err, pg.ErrHeld) {
			c.logger.ErrorContext(ctx, "kv: another process took this node's name while the database was out of reach")
			close(c.lost)
			return
		}
		if err != nil { // the loop ended
			return
		}
		c.hold = hold
		c.logger.InfoContext(ctx, "kv: this node holds its name again")
	}
}

// retake tries to take the name back every holdEvery until it does, another session holds it (pg.ErrHeld), or ctx ends.
func (c *Client) retake(ctx context.Context) (*pg.Hold, error) {
	ticker := time.NewTicker(holdEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
		attempt, cancel := context.WithTimeout(ctx, holdEvery)
		hold, err := c.hold.Retake(attempt)
		cancel()
		if err == nil || errors.Is(err, pg.ErrHeld) {
			return hold, err
		}
	}
}

// fillDefaults gives the settings a test leaves out their production values.
func (d *Deps) fillDefaults() {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if d.AccessTimeout == 0 {
		d.AccessTimeout = Timeout
	}
	if d.HeartbeatTimeout == 0 {
		d.HeartbeatTimeout = defaultHeartbeatTimeout
	}
}

// connect makes the client and judges the first access. With ForceSingleClient the client comes back even when its first
// dial failed, and the error is that failure: the client dials again by itself at its next access, so a server that is
// not there yet is no reason to have no client.
func (c *Client) connect(ctx context.Context, cfg Config) error {
	vk, dialErr := valkey.NewClient(option(*cfg.resolved))
	if vk == nil {
		return fmt.Errorf("%s: cannot make a client", URLVar)
	}
	c.vk = vk
	return c.probe(ctx, dialErr)
}

// option is the connection README「客户端与连接」 fixes: always pipelining, few connections, no cache and no retry, a
// one-second dial and a one-second write timeout. Each field is read from valkey-go v1.0.78 and re-read on upgrade.
func option(a endpoint) valkey.ClientOption {
	o := valkey.ClientOption{
		InitAddress: []string{a.address},
		// The user is in the URL because the ACL turns `default` off; the password is in its file and never in the URL.
		Username: a.username, Password: a.password,
		// Pipelining is what makes a deadline return at once without closing the connection: the client's own goroutine
		// drops the reply that comes late, where the synchronous path would close the connection and reconnect, so a
		// slow server would turn into a storm of reconnects.
		AlwaysPipelining:  true,
		ForceSingleClient: true, // one Valkey, never a cluster (README「部署」)
		// No cache: it makes the client send CLIENT TRACKING, which the ACL does not allow, and nothing here reads the
		// same key twice. No retry: a script adds to a counter, so a command sent twice counts twice.
		DisableCache: true, DisableRetry: true,
		// The client's name and version are no one else's business and cost a command at every connection.
		ClientSetInfo: valkey.DisableClientSetInfo,
		Dialer:        net.Dialer{Timeout: time.Second, KeepAlive: time.Second},
		// The client's default is the keep-alive times ten, ten seconds: a connection that hears nothing for a second
		// is a connection to a server that is gone.
		ConnWriteTimeout: time.Second,
	}
	if a.tls {
		// The system's roots, against the name the URL gives; a private CA joins with the deployment that needs one.
		o.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return o
}

// probe is the first access, and the only one whose outcome decides whether the node starts: a server that refuses the
// user is a configuration mistake, one that does not answer is an outage the node starts through. A server that answers
// must also pass the checks that need it.
func (c *Client) probe(ctx context.Context, dialErr error) error {
	if dialErr != nil {
		return c.startThrough(ctx, dialErr)
	}
	if err := c.vk.Do(ctx, c.vk.B().Ping().Build()).Error(); err != nil {
		return c.startThrough(ctx, err)
	}
	err := c.verify(ctx)
	if errors.Is(err, ErrUnavailable) {
		return c.startThrough(ctx, err) // the server went away between the two accesses
	}
	return err
}

// startThrough is the start of a node whose server did not answer the probe: refused when the server refused the user,
// and otherwise through the outage, with the breaker open and the checks that need a server owed to its first answer.
func (c *Client) startThrough(ctx context.Context, cause error) error {
	if code, refused := refusedCode(cause); refused {
		return fmt.Errorf("%s: the server refused the user (%s): check the user in the URL and the password in %s",
			URLVar, code, PasswordFileVar)
	}
	c.br.trip(c.now())
	c.m.recordBreaker(ctx, true)
	c.deferred.Store(true)
	// Warn, not Error: a node whose Valkey is not there serves from its own counters, and the loop says when it comes back.
	c.logger.WarnContext(ctx, "valkey is not reachable: the node starts on its own counters, with the breaker open",
		"err", cause)
	return nil
}

// verify runs the checks that need a reachable server. It is the startup's, where a failure stops the start; the loop
// runs them again for a server that was not there at startup, and only reports what it finds.
func (c *Client) verify(ctx context.Context) error {
	skew, mem, err := c.measure(ctx, true)
	if err != nil {
		return err
	}
	return errors.Join(mem.policyProblem(), skewProblem(skew))
}

// policyProblem reports a server with no memory limit, or one that would evict a key: a count and a login bucket must
// live until they expire (README「内存」).
func (m memory) policyProblem() error {
	switch {
	case m.max == 0:
		return fmt.Errorf("%s: the server has no memory limit: start it with maxmemory (internal/platform/kv/README.md「内存」)", URLVar)
	case m.policy != "noeviction":
		return fmt.Errorf("%s: the server's maxmemory_policy is %q, and it must be noeviction: a full server would evict "+
			"the login buckets and the count keys", URLVar, m.policy)
	}
	return nil
}

// skewProblem reports a clock further from the server's than a second, which no machine with a synchronised clock has:
// the windows and the buckets would be computed at the wrong moment (README「时钟」).
func skewProblem(skew time.Duration) error {
	if skew > time.Second || skew < -time.Second {
		return fmt.Errorf("%s: the clock is %v away from the server's, past the second the windows allow: synchronise "+
			"both clocks", URLVar, skew.Round(time.Millisecond))
	}
	return nil
}

// Enabled reports whether the shared state is in use: false is single-core mode, where every use stays in the process.
func (c *Client) Enabled() bool { return c.vk != nil }

// Nodes is the number of core nodes login's fallback divides by: the configured N, or the count the heartbeat sees alive
// when a rolling deploy has more processes than nodes (README「配置与启动」).
func (c *Client) Nodes() int { return max(c.config, int(c.live.Load())) }

// Now is the local clock corrected by the measured offset to the server: a script's "now" and a window's number come
// from here, so every node's windows line up (README「时钟」).
func (c *Client) Now() time.Time {
	now := c.now()
	if c.vk == nil {
		return now
	}
	return now.Add(time.Duration(c.skew.Load()))
}

// Close ends the loop and lets the server go, and deletes this node's row so that a restart is not refused for a row
// only a crash would leave. ctx bounds the deletion, and a returned error is the row's alone.
func (c *Client) Close(ctx context.Context) error {
	c.closing.Do(func() {
		c.laterMu.Lock()
		c.closed = true
		c.laterMu.Unlock()
		if c.stop != nil {
			c.stop()
			c.done.Wait()
		}
		if c.vk != nil {
			c.vk.Close()
		}
	})
	return c.release(ctx)
}

// release deletes this node's heartbeat row and lets its name go, if it took them.
func (c *Client) release(ctx context.Context) error {
	if c.hear == nil {
		return nil
	}
	return errors.Join(c.hear.stop(ctx), c.hold.Release(ctx))
}
