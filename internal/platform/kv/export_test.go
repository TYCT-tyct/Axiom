package kv

import (
	"context"
	"time"
)

// MaxOutstanding is how many commands a client lets be out at once, for the test that counts what reached the server.
const MaxOutstanding = maxOutstanding

// Measure is one round of the loop's reading of the server, run by a test now and not at the loop's pace of ten
// seconds: the memory, and the clock while the start still owes its checks.
func (c *Client) Measure(ctx context.Context) error {
	_, _, err := c.measure(ctx, false)
	return err
}

// StopChecks waits for the background checks to finish, so a test can run the next measurement itself without racing
// the loop for the breaker's half-open probe.
func (c *Client) StopChecks() {
	if c.stop != nil {
		c.stop()
	}
	c.done.Wait()
}

// SetAccessTimeout lets a deadline test start with the wide test budget and narrow it only after pausing the server.
// The test must have no accesses running; stopping the background checks here also keeps them from racing the change.
func (c *Client) SetAccessTimeout(timeout time.Duration) {
	c.StopChecks()
	c.timeout = timeout
}

// Crash ends the client the way a crashed process ends: its loops and connections go and its row stays, and the name it
// held is freed with the connection, which is all a restart needs.
func (c *Client) Crash(ctx context.Context) error {
	c.StopChecks()
	if c.vk != nil {
		c.vk.Close()
	}
	return c.hold.Release(ctx)
}

// Beat is a whole round of the loop, the clock included, run by a test now.
func (c *Client) Beat(ctx context.Context) {
	var over since
	c.beat(ctx, &over, true)
}

// OpenAsTheMachine is Open for a client whose clock the test controls but whose loop measures the clock again, as on
// the machine's clock: the server's clock and the client's are both the test's, so the offset is exact.
func OpenAsTheMachine(ctx context.Context, cfg Config, deps Deps) (*Client, error) {
	return open(ctx, cfg, deps, false)
}
