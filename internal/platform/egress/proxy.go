package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/net/proxy"
)

// Proxy is a forward proxy an exit's connections go through: an http or https proxy that opens a tunnel with CONNECT,
// or a socks5 one, with an optional username and password. Its address is an operator's choice and is not held to the
// address policy, so it may be a private one; the targets reached through it still are.
type Proxy struct {
	Scheme   string // "http", "https" or "socks5"
	Host     string
	Port     int
	Username string
	Password string
}

// String is the proxy without its credentials, so that formatting one never prints the password.
func (p Proxy) String() string {
	return p.Scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
}

// ErrInvalidProxy is a proxy that is not http, https or socks5 with a host and a port, or whose SOCKS5 username or
// password is longer than the protocol's 255 bytes.
var ErrInvalidProxy = errors.New("egress: a proxy is http, https or socks5 with a host and a port")

// Via is a client like c, with its address policy, timeouts, trust roots and tracing, whose connections go through p.
// A target is checked as c checks it before the proxy is asked for it: a private or internal address, or a name that
// resolves to one here, is refused and the proxy never hears of it. A name is then handed to the proxy unresolved,
// so the exit resolves it where it stands (the reason to use one is that its network sees what this one does not);
// a name that resolves publicly here and privately there reaches the proxy's network, which is its operator's. The
// whole of a connection — to the proxy, through its handshake and to the target — has the direct client's 10 seconds.
func (c *Client) Via(p Proxy) (*Client, error) {
	through, err := p.dialer(c.transport.TLSClientConfig)
	if err != nil {
		return nil, err
	}
	d := *c.dialer
	d.through = through
	v := &Client{transport: c.transport.Clone(), dialer: &d, tracer: c.tracer}
	v.transport.DialContext = d.dialContext
	return v, nil
}

// dialer is how p opens a connection to a target; base holds the trust roots an https proxy's certificate is checked
// against, nil for the system's.
func (p Proxy) dialer(base *tls.Config) (func(context.Context, string, string) (net.Conn, error), error) {
	if p.Host == "" || p.Port < 1 || p.Port > 65535 {
		return nil, ErrInvalidProxy
	}
	address := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	forward := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	switch p.Scheme {
	case "socks5":
		return p.socks(address, forward)
	case "http", "https":
		t := tunnel{address: address, forward: forward}
		if p.Scheme == "https" {
			t.tls = &tls.Config{ServerName: p.Host, MinVersion: tls.VersionTLS12}
			if base != nil {
				t.tls.RootCAs = base.RootCAs
			}
		}
		if p.Username != "" {
			t.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(p.Username+":"+p.Password))
		}
		return t.dial, nil
	}
	return nil, ErrInvalidProxy
}

// socks is a SOCKS5 proxy's dial, which hands the target's name to the proxy unresolved.
func (p Proxy) socks(address string, forward *net.Dialer) (func(context.Context, string, string) (net.Conn, error), error) {
	if len(p.Username) > 255 || len(p.Password) > 255 { // RFC 1929 gives each one length byte
		return nil, ErrInvalidProxy
	}
	var auth *proxy.Auth
	if p.Username != "" {
		auth = &proxy.Auth{User: p.Username, Password: p.Password}
	}
	d, err := proxy.SOCKS5("tcp", address, auth, forward)
	if err != nil {
		return nil, fmt.Errorf("egress socks5 proxy: %w", err)
	}
	c, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("egress socks5 proxy: the dialer does not take a context")
	}
	return c.DialContext, nil
}

// tunnel opens a connection through an HTTP proxy with CONNECT, over TLS to the proxy for an https one.
type tunnel struct {
	address string
	forward *net.Dialer
	tls     *tls.Config // nil for an http proxy
	auth    string      // the Proxy-Authorization value; empty for none
}

// errTunnelRefused is a proxy that answered CONNECT with anything but 200; only the status is kept.
var errTunnelRefused = errors.New("egress: the proxy refused the tunnel")

// dial opens the tunnel within ctx's deadline, which dialContext always sets: the handshakes read and write a
// connection that ctx does not reach, so the deadline is the connection's until the tunnel is open.
func (t tunnel) dial(ctx context.Context, _, target string) (net.Conn, error) {
	conn, err := t.forward.DialContext(ctx, "tcp", t.address)
	if err != nil {
		return nil, fmt.Errorf("egress connect to the proxy: %w", err)
	}
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	if conn, err = t.open(ctx, conn, target); err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	return conn, nil
}

// open asks the proxy, on conn, for a tunnel to target; conn is closed when it fails.
func (t tunnel) open(ctx context.Context, conn net.Conn, target string) (net.Conn, error) {
	if t.tls != nil {
		tc := tls.Client(conn, t.tls)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, errors.Join(fmt.Errorf("egress proxy TLS: %w", err), conn.Close())
		}
		conn = tc
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: http.Header{}}
	if t.auth != "" {
		req.Header.Set("Proxy-Authorization", t.auth)
	}
	if err := req.Write(conn); err != nil {
		return nil, errors.Join(fmt.Errorf("egress proxy CONNECT: %w", err), conn.Close())
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("egress proxy CONNECT: %w", err), conn.Close())
	}
	// A 200 to CONNECT has no body, and the connection is the tunnel; any other status ends the connection.
	if err := resp.Body.Close(); err != nil || resp.StatusCode != http.StatusOK {
		return nil, errors.Join(fmt.Errorf("%w: %d", errTunnelRefused, resp.StatusCode), err, conn.Close())
	}
	return conn, nil
}
