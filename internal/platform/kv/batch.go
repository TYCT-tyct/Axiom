package kv

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/valkey-io/valkey-go"
)

// Timeout bounds one synchronous access, and with it this step's share of a model call's first-token budget: the
// caller's context and this, whichever ends first (README「客户端与连接」, docs/performance.md「延迟预算」).
const Timeout = 20 * time.Millisecond

// ErrUnavailable is what every failed access returns, wrapped around the cause: the shared state is not there, so the
// caller takes its fallback (README「每种用途」).
var ErrUnavailable = errors.New("valkey unavailable")

// Use is what a batch of commands is for: the label of the metrics and of the log line (README「指标与告警」).
type Use string

// The uses of a batch, as the metrics label them. K2 and K3 add theirs: login and the sticky write.
const (
	Precheck Use = "precheck" // the precheck of a model call, before admission
	// check is the client's own reading of the server: the startup checks and the loop's measurements.
	check Use = "check"
)

// uses is every Use, in the order the metrics prebuild their options.
var uses = [...]Use{Precheck, check}

// Space is the purpose a key serves, its third field (README「键」). K2 and K3 add the login and the sticky ones.
type Space string

// RateSpace is a rate counter's.
const RateSpace Space = "rate"

// Script is a module's Lua script. kv runs it and holds none of its rules: the keys are the module's, and so are the
// arguments, which carry the time the module read from Now (README「接口」).
type Script struct{ src string }

// NewScript is a script as EVAL takes it. Every call sends the source: EVALSHA after a restart of the server answers
// NOSCRIPT, and the second round trip would break the one round trip a precheck has.
func NewScript(src string) Script { return Script{src} }

// Cmd is one command of a batch. The type is opaque so that a module runs its own script and nothing else: the client's
// two readings of the server are the other commands it knows.
type Cmd struct {
	kind cmdKind
	src  string   // the script's source, for a script
	keys []string // the keys the script declares, which the server checks against the ACL user
	args []string
}

// cmdKind is the command a Cmd runs.
type cmdKind uint8

const (
	scriptCmd cmdKind = iota
	timeCmd
	memoryCmd
)

// Cmd is one run of the script over keys with arguments.
func (s Script) Cmd(keys []string, args ...string) Cmd {
	return Cmd{kind: scriptCmd, src: s.src, keys: keys, args: args}
}

// serverTime and serverMemory are the client's own readings: TIME and INFO memory, which the startup checks and the
// loop measure the clock and the memory with.
func serverTime() Cmd   { return Cmd{kind: timeCmd} }
func serverMemory() Cmd { return Cmd{kind: memoryCmd} }

// Value is one command's reply. The command is a module's, so the module reads the reply the way it asked.
type Value struct{ r valkey.ValkeyResult }

// Ints is the reply as a list of integers: a script returning pairs of counts, or TIME's two fields.
func (v Value) Ints() ([]int64, error) { return v.r.AsIntSlice() }

// String is the reply as text: INFO's fields, or a sticky key's endpoint.
func (v Value) String() (string, error) { return v.r.ToString() }

// Do runs the batch in one round trip, through the breaker, under the deadline the caller's context and Timeout set,
// and counts it. Any failure is ErrUnavailable with its cause, and the caller falls back; the replies are read only
// when every command succeeded.
func (c *Client) Do(ctx context.Context, use Use, cmds ...Cmd) ([]Value, error) {
	if c.vk == nil || len(cmds) == 0 {
		return nil, ErrUnavailable
	}
	started := c.now()
	probe, allowed := c.br.allow(started)
	if !allowed {
		c.m.recordCall(ctx, use, unavailable)
		return nil, ErrUnavailable
	}
	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	values, err := c.send(ctx, caller, c.build(cmds))
	took := c.now().Sub(started)
	result := outcomeOf(caller, err)
	c.m.duration.Record(caller, took.Seconds(), c.m.useOption(use))
	c.m.recordCall(caller, use, result)
	if changed, open := c.br.done(started, probe, result); changed {
		c.m.recordBreaker(caller, open)
	}
	if err != nil {
		if result == failed {
			c.logger.ErrorContext(caller, "valkey refused a command: a script or its keys are wrong", "err", err)
			if isOOM(err) {
				c.m.oom.Add(caller, 1)
			}
		}
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return values, nil
}

// The most commands that may be sent and not yet answered, and how long one may stay out. A connection's ring holds 2^10
// commands, and putting a command into a full ring waits for a slot without looking at any context (valkey-go v1.0.78
// ring.go). So the client never lets more than half a ring be out, whichever connection a batch takes, and a call that
// cannot get its place before its deadline leaves without sending. A command whose caller has left stays out until its
// reply or until the connection is closed for its silence, which the one-second dial and write timeouts make about two
// seconds; settleBy cuts the wait there.
const (
	maxOutstanding = 512
	settleBy       = 3 * time.Second
)

// flight is one batch on its way, which the caller may leave before it lands.
type flight struct {
	values []Value
	err    error
	done   chan struct{}
}

// send puts the batch on the wire and waits for its replies for as long as ctx lasts. The batch is sent by a goroutine of
// its own that holds its place until the replies come, not until the caller stops waiting: the place stands for the ring
// slot, which an abandoned command keeps. Without a place by the deadline nothing is sent.
func (c *Client) send(ctx, caller context.Context, built []valkey.Completed) ([]Value, error) {
	weight := int64(len(built))
	if err := c.slots.Acquire(ctx, weight); err != nil {
		return nil, err
	}
	sent := &flight{done: make(chan struct{})}
	go func() {
		work, cancel := context.WithTimeout(context.WithoutCancel(caller), settleBy)
		defer cancel()
		sent.values, sent.err = c.run(work, built)
		c.slots.Release(weight)
		close(sent.done)
	}()
	select {
	case <-sent.done:
		return sent.values, sent.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// build turns the batch into commands. It runs in the caller's goroutine, so the caller may reuse its Cmds once Do returns.
func (c *Client) build(cmds []Cmd) []valkey.Completed {
	builder := c.vk.B()
	built := make([]valkey.Completed, len(cmds))
	for i, cmd := range cmds {
		switch cmd.kind {
		case timeCmd:
			built[i] = builder.Time().Build()
		case memoryCmd:
			built[i] = builder.Info().Section("memory").Build()
		case scriptCmd:
			built[i] = builder.Eval().Script(cmd.src).Numkeys(int64(len(cmd.keys))).Key(cmd.keys...).Arg(cmd.args...).Build()
		}
	}
	return built
}

// run sends the commands and reads every reply: a command the server refused fails the batch, because a module that lost
// one of two counts cannot tell what the other one means.
func (c *Client) run(ctx context.Context, built []valkey.Completed) ([]Value, error) {
	replies := c.vk.DoMulti(ctx, built...)
	values := make([]Value, len(replies))
	for i, reply := range replies {
		if err := reply.Error(); err != nil {
			return nil, err
		}
		values[i] = Value{reply}
	}
	return values, nil
}

// outcomeOf tells a failure of ours from one of the caller's: only ours may open the breaker, or a client that sends a
// request and disconnects could push it open on everyone's behalf (README「熔断」). The caller's context is judged first:
// once it has ended, whatever stopped the batch may be its own deadline or cancel, and says nothing about the server. A
// deadline that ends the batch while the caller's context is alive is Timeout's.
func outcomeOf(caller context.Context, err error) outcome {
	switch {
	case err == nil:
		return ok
	case caller.Err() != nil:
		return skipped
	case errors.Is(err, context.DeadlineExceeded):
		return timedOut
	case isServerError(err):
		return failed // a script or a command the server refused: a mistake to fix, not an outage to route around
	default:
		return unavailable // a connection that failed, or was closed under the command
	}
}

// isOOM reports the refusal of a server whose memory is full.
func isOOM(err error) bool {
	var reply *valkey.ValkeyError
	return errors.As(err, &reply) && strings.HasPrefix(reply.Error(), "OOM")
}

// isServerError reports an error the server sent, as opposed to a connection that failed.
func isServerError(err error) bool {
	var reply *valkey.ValkeyError
	return errors.As(err, &reply) && !reply.IsNil()
}

// refusedCode is the code of a server that would not authenticate or authorise this client, which is a configuration
// mistake and not an outage, and "" for anything else.
func refusedCode(err error) (string, bool) {
	var serverErr *valkey.ValkeyError
	if !errors.As(err, &serverErr) {
		return "", false
	}
	message := serverErr.Error()
	for _, code := range [...]string{"WRONGPASS", "NOAUTH", "NOPERM"} {
		if strings.HasPrefix(message, code) {
			return code, true
		}
	}
	return "", false
}

// Key is one key's name: ax:<ns>:<space>:<site>:<parts>. The namespace is settled here and nowhere else, and an ID goes
// in as Hex writes it (README「键」). It makes one allocation, the key: the call path builds two for every precheck.
func (c *Client) Key(space Space, site uuid.UUID, parts ...string) string {
	size := len("ax:") + len(c.ns) + 1 + len(space) + 1 + 2*len(site)
	for _, part := range parts {
		size += 1 + len(part)
	}
	var key strings.Builder
	key.Grow(size)
	key.WriteString("ax:")
	key.WriteString(c.ns)
	key.WriteByte(':')
	key.WriteString(string(space))
	key.WriteByte(':')
	var id [32]byte
	hex.Encode(id[:], site[:])
	key.Write(id[:])
	for _, part := range parts {
		key.WriteByte(':')
		key.WriteString(part)
	}
	return key.String()
}

// Hex is an ID as a key carries it: a UUID's 32 lowercase hex digits, without the dashes String writes.
func Hex(id uuid.UUID) string {
	var text [32]byte
	hex.Encode(text[:], id[:])
	return string(text[:])
}
