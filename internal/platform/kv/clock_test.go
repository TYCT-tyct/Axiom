package kv_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
)

// timeServer is just enough of a Valkey to be started against: it answers HELLO, PING, TIME and INFO memory, and its
// clock is a moment the test names. The client's own clock is a moment the test names too, so the offset between them is
// a fact of the test and not of how long the machine took to get from one to the other.
type timeServer struct {
	now     atomic.Int64 // the server's clock, in nanoseconds since the epoch
	address string
}

func startTimeServer(t *testing.T, now time.Time) *timeServer {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &timeServer{address: listener.Addr().String()}
	s.now.Store(now.UnixNano())
	t.Cleanup(func() { _ = listener.Close() }) //nolint:errcheck // the test is over
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
			t.Cleanup(func() { _ = conn.Close() }) //nolint:errcheck // the test is over
		}
	}()
	return s
}

// serve answers the commands of one connection until it ends.
func (s *timeServer) serve(conn net.Conn) {
	in := bufio.NewReader(conn)
	for {
		command, err := readCommand(in)
		if err != nil {
			return
		}
		var reply string
		switch strings.ToUpper(command[0]) {
		case "HELLO":
			reply = "%1\r\n$5\r\nproto\r\n:3\r\n"
		case "PING":
			reply = "+PONG\r\n"
		case "TIME":
			at := time.Unix(0, s.now.Load())
			seconds, micros := strconv.FormatInt(at.Unix(), 10), strconv.Itoa(at.Nanosecond()/1000)
			reply = fmt.Sprintf("*2\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n", len(seconds), seconds, len(micros), micros)
		case "INFO":
			info := "# Memory\r\nused_memory:1024\r\nmaxmemory:268435456\r\nmaxmemory_policy:noeviction\r\n"
			reply = fmt.Sprintf("$%d\r\n%s\r\n", len(info), info)
		default:
			reply = "-ERR unknown command\r\n"
		}
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

// readCommand reads one command, an array of bulk strings.
func readCommand(in *bufio.Reader) ([]string, error) {
	header, err := in.ReadString('\n')
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "*")))
	if err != nil {
		return nil, err
	}
	words := make([]string, count)
	for i := range words {
		size, err := in.ReadString('\n')
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(size, "$")))
		if err != nil {
			return nil, err
		}
		word := make([]byte, n+2)
		if _, err := io.ReadFull(in, word); err != nil {
			return nil, err
		}
		words[i] = string(word[:n])
	}
	return words, nil
}

// TestTheOffsetToTheServerIsMeasuredAndKept: with the client's clock at a moment and the server's at another, the
// client's corrected time is its own plus exactly the difference, whatever the machine is doing, and a difference of more
// than a second stops the start, one of exactly a second does not (README「时钟」).
func TestTheOffsetToTheServerIsMeasuredAndKept(t *testing.T) {
	t.Parallel()
	local := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	password := writePassword(t, "password\n")
	for _, tt := range []struct {
		name   string
		offset time.Duration // the server's clock less the client's
		refuse bool
	}{
		{"in step", 0, false},
		{"the server ahead", 500 * time.Millisecond, false},
		{"the server behind", -700 * time.Millisecond, false},
		{"a second ahead", time.Second, false},
		{"a second behind", -time.Second, false},
		{"over a second ahead", time.Second + time.Millisecond, true},
		{"over a second behind", -time.Second - time.Millisecond, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := startTimeServer(t, local.Add(tt.offset))
			cfg := kv.Config{URL: "valkey://axiom@" + server.address, PasswordFile: password, Nodes: 1, Namespace: kvtest.Namespace()}
			client, err := kv.Open(t.Context(), cfg, kvtest.Deps(kv.Deps{Now: func() time.Time { return local }}))
			if tt.refuse {
				if err == nil || !strings.Contains(err.Error(), kv.URLVar) || !strings.Contains(err.Error(), "away from the server's") {
					t.Fatalf("a clock %v from the server's: %v, want a refusal naming %s", tt.offset, err, kv.URLVar)
				}
				return
			}
			if err != nil {
				t.Fatalf("a clock %v from the server's: %v", tt.offset, err)
			}
			t.Cleanup(func() { closeQuietly(t, client) })
			if got := client.Now().Sub(local); got != tt.offset {
				t.Fatalf("the corrected time is %v from the client's, want the %v the server is", got, tt.offset)
			}
		})
	}
}

// TestTheLoopMeasuresOnlyTheMachinesClock: a round of the loop measures the machine's clock again and follows a server
// whose time moved, while a client on a test's clock keeps the offset the start measured, so an advance of the test's
// clock is not read as skew and undone (README「时钟」, #624: an accounts test's advance undone by the 30-second
// measurement failed its sign-ins under load). Both clocks are the test's in both cases, so the offsets are exact.
func TestTheLoopMeasuresOnlyTheMachinesClock(t *testing.T) {
	t.Parallel()
	password := writePassword(t, "password\n")
	for _, c := range []struct {
		name string
		open func(context.Context, kv.Config, kv.Deps) (*kv.Client, error)
		// step moves the server's clock or the client's by an hour, before the round of the loop.
		step func(server *timeServer, local *atomic.Int64)
		want time.Duration // the corrected time less the client's after the round
	}{
		{
			"the machine's clock follows the server", kv.OpenAsTheMachine,
			func(server *timeServer, _ *atomic.Int64) { server.now.Add(int64(time.Hour)) }, time.Hour,
		},
		{
			"a test's clock keeps its advance", kv.Open,
			func(_ *timeServer, local *atomic.Int64) { local.Add(int64(time.Hour)) }, 0,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var local atomic.Int64
			local.Store(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).UnixNano())
			now := func() time.Time { return time.Unix(0, local.Load()) }
			server := startTimeServer(t, now())
			cfg := kv.Config{URL: "valkey://axiom@" + server.address, PasswordFile: password, Nodes: 1, Namespace: kvtest.Namespace()}
			client, err := c.open(t.Context(), cfg, kvtest.Deps(kv.Deps{Now: now}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeQuietly(t, client) })
			c.step(server, &local)
			client.Beat(t.Context())
			if got := client.Now().Sub(now()); got != c.want {
				t.Fatalf("after the round, the corrected time is %v from the client's, want %v", got, c.want)
			}
		})
	}
}
