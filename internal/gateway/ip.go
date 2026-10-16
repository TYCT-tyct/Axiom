package gateway

import (
	"net"
	"net/http"
	"net/netip"
	"sync"
)

// clientIP is the address the connection came from. M1 trusts no proxy header: the node is reached directly or
// through a CDN whose addresses AXIOM_TRUSTED_PROXIES will name when a deployment needs it (internal/accounts/README.md
// 「登录前的资源」).
func clientIP(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// ipKey is what the per-address limits count by. An IPv6 customer holds a whole /64, so counting its addresses one by
// one would let a single host take as many shares as it has addresses.
func ipKey(a netip.Addr) string {
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().String()
	}
	return a.String()
}

// LimitConnections wraps ln so a client address holds at most Config.IPConnections connections: the next one is
// closed as it is accepted, before a byte is read from it.
func (b *Backpressure) LimitConnections(ln net.Listener) net.Listener {
	return &limitedListener{Listener: ln, limiter: b.IPConns, max: b.cfg.IPConnections}
}

type limitedListener struct {
	net.Listener
	limiter *Limiter
	max     int
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		addr, counted := remoteAddr(c)
		if !counted { // not an IP address, such as a Unix socket: nothing to count by
			return c, nil
		}
		release, err := l.limiter.Acquire(ipKey(addr), l.max)
		if err != nil {
			c.Close() //nolint:errcheck,gosec // refused: the client is turned away, and the listener goes on with the next connection
			continue
		}
		return &countedConn{Conn: c, release: sync.OnceFunc(release)}, nil
	}
}

func remoteAddr(c net.Conn) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(c.RemoteAddr().String())
	return ap.Addr().Unmap(), err == nil
}

// countedConn gives its share back when closed, which net/http does once for every connection it serves.
type countedConn struct {
	net.Conn
	release func()
}

func (c *countedConn) Close() error {
	defer c.release()
	return c.Conn.Close()
}
