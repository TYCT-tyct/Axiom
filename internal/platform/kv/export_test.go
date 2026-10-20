package kv

import "context"

// Measure is one round of the loop's reading of the server, run by a test now and not at the loop's pace of ten
// seconds: the memory, and the clock while the start still owes its checks.
func (c *Client) Measure(ctx context.Context) error {
	_, _, err := c.measure(ctx, false)
	return err
}
