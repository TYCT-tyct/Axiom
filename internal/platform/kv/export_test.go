package kv

import (
	"context"
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
