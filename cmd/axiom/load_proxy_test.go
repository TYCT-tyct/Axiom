package main

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
)

// faultProxy sits between a node and its database and cuts the database off on demand: while it is down, every
// connection through it is closed and every new one is closed as it is accepted, which is what a node sees of a server
// that is gone. The database itself is untouched: the cluster is shared with other jobs, and the harness still reads
// and checks it directly.
type faultProxy struct {
	listener net.Listener
	target   string

	mu    sync.Mutex
	down  bool
	conns map[net.Conn]struct{}
}

func newFaultProxy(t testing.TB, target string) *faultProxy {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &faultProxy{listener: listener, target: target, conns: map[net.Conn]struct{}{}}
	go p.accept()
	t.Cleanup(func() {
		_ = listener.Close() //nolint:errcheck // ends the accept loop; there is nothing to report
		p.setDown(true)
	})
	return p
}

func (p *faultProxy) addr() string { return p.listener.Addr().String() }

// setDown cuts the connections through the proxy and refuses new ones, or lets them through again.
func (p *faultProxy) setDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
	if !down {
		return
	}
	for c := range p.conns {
		_ = c.Close() //nolint:errcheck // being cut off is the point
	}
	clear(p.conns)
}

func (p *faultProxy) accept() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.pipe(client)
	}
}

// track remembers a connection so that setDown can cut it, unless the proxy is down already.
func (p *faultProxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.down {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *faultProxy) pipe(client net.Conn) {
	server, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", p.target)
	if err != nil || !p.track(client) || !p.track(server) {
		_ = client.Close() //nolint:errcheck // the proxy is down or the database does not answer; the client sees a closed connection
		if err == nil {
			_ = server.Close() //nolint:errcheck // as above
		}
		return
	}
	done := make(chan struct{}, 2)
	for _, pair := range [][2]net.Conn{{client, server}, {server, client}} {
		go func() {
			_, _ = io.Copy(pair[0], pair[1]) //nolint:errcheck // either side closing ends the copy, which is the only signal
			done <- struct{}{}
		}()
	}
	<-done
	_ = client.Close() //nolint:errcheck // one side is gone, so the other goes too
	_ = server.Close() //nolint:errcheck // as above
	<-done
	p.mu.Lock()
	delete(p.conns, client)
	delete(p.conns, server)
	p.mu.Unlock()
}
