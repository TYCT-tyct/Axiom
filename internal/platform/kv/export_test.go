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

// Beat is a whole round of the loop, the clock included, run by a test now.
func (c *Client) Beat(ctx context.Context) {
	var over since
	c.beat(ctx, &over, time.Time{})
}
