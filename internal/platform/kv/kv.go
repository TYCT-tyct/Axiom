// Package kv is the node's access to the shared state in Valkey: the deadline and the breaker around every access, the
// clock correction, the heartbeat that makes a wrong node count visible, and the metrics. It holds no business rule: a
// module keeps its script and builds its keys here (README.md).
package kv

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

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
	// HeartbeatTimeout bounds one round of the heartbeat's database work; zero is defaultHeartbeatTimeout. A test shortens it.
	HeartbeatTimeout time.Duration
	// Now is the local clock, and nil is time.Now. Tests set one that is wrong to see the clock check, and one they
	// advance to open and close the breaker without waiting (README「时钟」「熔断」).
	Now func() time.Time
}

// Client is the node's connection to the shared state: one Valkey, one batch of commands at a time, a deadline and a
// breaker around every access, and the heartbeat that makes a wrong N visible. It holds no business rule: a module keeps
// its script and builds its keys here (internal/platform/kv/README.md).
type Client struct {
	vk     valkey.Client // nil in single-core mode, where every use stays in the process
	ns     string
	now    func() time.Time // the local clock, which Now corrects by skew
	skew   atomic.Int64     // the server's clock less this one's, in nanoseconds
	live   atomic.Int64     // nodes the heartbeat sees alive, this one included
	config int              // the configured N
	logger *slog.Logger
	m      metrics
	br     *breaker
	slots  *semaphore.Weighted // the commands sent and not yet answered, below what a connection's ring holds (maxOutstanding)
	hear   *heartbeat          // nil without a database: a test's client
	// beatTimeout bounds one round of the heartbeat's database work.
	beatTimeout time.Duration

	// deferred is set when the server was unreachable at startup: the checks that need one are owed, and the loop runs
	// them at the first answer and reports what they find (README「配置与启动」).
	deferred atomic.Bool
	stop     context.CancelFunc
	done     sync.WaitGroup
	closing  sync.Once
}

// Open registers the node, connects to Valkey and checks it as far as the configuration allows (README「配置与启动」).
// Every node registers and keeps a heartbeat when it has a database, with or without Valkey, so that a second node
// against the same database is found whatever it is configured with. Without URLVar it returns the single-core client:
// Enabled is false, and every caller keeps its in-process implementation. A server that cannot be reached is not an
// error, and the node starts on its fallbacks; a server that refuses the user, has no memory limit, or disagrees about
// the time stops the start, because those are deployment mistakes.
func Open(ctx context.Context, cfg Config, deps Deps) (*Client, error) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	if deps.HeartbeatTimeout == 0 {
		deps.HeartbeatTimeout = defaultHeartbeatTimeout
	}
	namespace := cfg.Namespace
	if namespace == "" {
		namespace = DefaultNamespace
	}
	if err := cfg.Resolve(); err != nil {
		return nil, err
	}
	c := &Client{
		ns: namespace, now: deps.Now, config: cfg.Nodes, logger: deps.Logger, m: newMetrics(deps.Meter),
		br: newBreaker(), slots: semaphore.NewWeighted(maxOutstanding), beatTimeout: deps.HeartbeatTimeout,
	}
	c.live.Store(int64(cfg.Nodes))
	// The heartbeat speaks first: a second node that reached this database, with Valkey or without, or an N set too low,
	// is found before the server is touched, and this node's row is written only once it may start.
	if deps.DB != nil {
		c.hear = newHeartbeat(deps.DB, deps.Scope)
		if err := c.hear.start(ctx, cfg.Nodes, &c.live, c.m); err != nil {
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
		c.done.Go(func() { c.watch(loop) })
	}
	return c, nil
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

// release deletes this node's heartbeat row, if it wrote one.
func (c *Client) release(ctx context.Context) error {
	if c.hear == nil {
		return nil
	}
	return c.hear.stop(ctx)
}
