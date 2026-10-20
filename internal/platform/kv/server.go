package kv

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// clockEvery is how often the loop measures the clock: a synchronised machine drifts far less than a second in
	// 30 seconds, and the correction only has to keep the windows aligned (README「时钟」).
	clockEvery = 30 * time.Second
	// overFor is how long the heartbeat's live count may stay above N before it is an error: a rolling deploy runs two
	// processes of one node for a moment, and only a count that stays up is a wrong N (README「配置与启动」).
	overFor = 2 * time.Minute
)

// memory is what INFO memory says about the server's limit and its use of it.
type memory struct {
	used, max int64
	policy    string
}

// watch is the client's own work: the heartbeat and the memory every beatEvery, and the clock when its turn comes.
// Nothing here stops the node, because the shared state is not a dependency: a failure is logged and retried, and the
// breaker has already counted it (README「配置与启动」).
func (c *Client) watch(ctx context.Context) {
	ticker := time.NewTicker(beatEvery)
	defer ticker.Stop()
	var over since
	// The cadence of the clock check is real elapsed time, not the node's clock, which is what is being checked (and a
	// clock that was stepped must not skip or repeat the check). The start measured the clock just now.
	lastClock := time.Now()
	for {
		due := time.Since(lastClock) >= clockEvery
		c.beat(ctx, &over, due)
		if due {
			lastClock = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// beat is one round of the loop: the server's memory, and its time when the clock is due, then this node's heartbeat.
// The heartbeat goes last and under a deadline of its own, so a database that does not answer delays the round by that
// deadline at most and never stops the checks of Valkey.
func (c *Client) beat(ctx context.Context, over *since, clockDue bool) {
	if c.vk != nil {
		if _, _, err := c.measure(ctx, clockDue); err != nil &&
			ctx.Err() == nil && !errors.Is(err, ErrUnavailable) {
			// An outage is the breaker's to report; what reaches here answered, but not as one of our deployments must.
			c.logger.ErrorContext(ctx, "valkey does not answer as it must: the node keeps serving on its own counters", "err", err)
		}
	}
	if c.hear != nil {
		c.heartbeat(ctx, over)
	}
}

// heartbeat writes this node's row, and reports a live count above N once per episode.
func (c *Client) heartbeat(ctx context.Context, over *since) {
	round, cancel := context.WithTimeout(ctx, c.beatTimeout)
	defer cancel()
	live, err := c.hear.beat(round)
	if err != nil {
		if ctx.Err() == nil {
			c.logger.ErrorContext(ctx, "the node heartbeat failed: a wrong core-node count would not be noticed", "err", err)
		}
		return
	}
	c.live.Store(int64(live))
	c.m.nodes.Record(ctx, int64(live))
	if live <= c.config {
		over.released()
		return
	}
	// A rolling deploy runs two processes of one node for a moment; a count that stays up is an N set too low, or a
	// node that reached this database without Valkey (README「配置与启动」).
	if over.holding(c.now()) {
		c.logger.ErrorContext(ctx, "more core nodes are alive than "+NodesVar+" says: set it to the number of nodes, "+
			"and give every node Valkey", "live", live, "configured", c.config)
	}
}

// measure reads the server's memory, and its time when withClock says so, in one round trip. The clock is corrected with
// the offset it finds, and both are recorded for the alerts. A failure comes back as it is, counted by the breaker.
//
// A start that could not reach the server owes the checks that need one, and the clock is one of them: until they have
// run, every measurement takes the clock with it.
func (c *Client) measure(ctx context.Context, withClock bool) (time.Duration, memory, error) {
	withClock = withClock || c.deferred.Load()
	started := c.now()
	cmds := []Cmd{serverMemory()}
	if withClock {
		cmds = []Cmd{serverTime(), serverMemory()}
	}
	values, err := c.Do(ctx, check, cmds...)
	if err != nil {
		return 0, memory{}, err
	}
	memoryAt := 0
	var skew time.Duration
	if withClock {
		ended := c.now()
		parts, err := values[0].Ints()
		if err != nil {
			return 0, memory{}, err
		}
		if skew, err = serverOffset(parts, started, ended); err != nil {
			return 0, memory{}, err
		}
		c.skew.Store(int64(skew))
		c.m.skew.Record(ctx, skew.Seconds())
		memoryAt = 1
	}
	info, err := values[memoryAt].String()
	if err != nil {
		return 0, memory{}, err
	}
	mem, err := parseMemory(info)
	if err != nil {
		return 0, memory{}, err
	}
	c.m.memory.Record(ctx, mem.used)
	// The checks the start could not make. A failure is reported and the node keeps serving: an outage may not turn into
	// a refusal to start later (README「配置与启动」).
	if c.deferred.CompareAndSwap(true, false) {
		if err := errors.Join(mem.policyProblem(), skewProblem(skew)); err != nil {
			c.logger.ErrorContext(ctx, "valkey answered, and a check a starting node refuses over failed; this node keeps "+
				"serving, and the check does not run again", "err", err)
		}
	}
	return skew, mem, nil
}

// serverOffset is the server's time less the middle of the round trip: the server read its clock half of the round trip
// into the call. Its argument is TIME's two fields, so a test can compute an offset without a server.
func serverOffset(parts []int64, started, ended time.Time) (time.Duration, error) {
	if len(parts) != 2 {
		return 0, errors.New("valkey: TIME did not give seconds and microseconds")
	}
	server := time.Unix(parts[0], parts[1]*int64(time.Microsecond))
	return server.Sub(started.Add(ended.Sub(started) / 2)), nil
}

// parseMemory reads the three fields of INFO memory that matter: the limit, the use, and what happens at the limit.
func parseMemory(info string) (memory, error) {
	var m memory
	for line := range strings.SplitSeq(info, "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		var err error
		switch name {
		case "used_memory":
			m.used, err = strconv.ParseInt(value, 10, 64)
		case "maxmemory":
			m.max, err = strconv.ParseInt(value, 10, 64)
		case "maxmemory_policy":
			m.policy = value
		}
		if err != nil {
			return memory{}, fmt.Errorf("valkey: INFO memory gave an unreadable %s", name)
		}
	}
	if m.policy == "" {
		return memory{}, errors.New("valkey: INFO memory did not give maxmemory_policy")
	}
	return m, nil
}

// since is how long a condition has held, and whether it was reported: a rolling deploy's overlap is not an error, and
// an error that keeps holding is one log line rather than one every beat.
type since struct {
	at       time.Time
	reported bool
}

// holding counts a condition that holds now, and reports true the first time it has held for overFor.
func (s *since) holding(now time.Time) bool {
	if s.at.IsZero() {
		s.at = now
		return false
	}
	if s.reported || now.Sub(s.at) < overFor {
		return false
	}
	s.reported = true
	return true
}

// released clears the count, for a condition that stopped holding.
func (s *since) released() { *s = since{} }
