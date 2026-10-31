package kv

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"axiom/internal/platform/privfile"
)

// The variables serve reads; kv reports on them, as pg reports on its own (cmd/axiom/variables.go).
const (
	// URLVar is the Valkey URL, valkey://<user>@<host>:<port>. Unset is single-core mode: every use stays in the
	// process, as without Valkey (README「配置与启动」).
	URLVar = "AXIOM_VALKEY_URL"
	// PasswordFileVar names the file that holds the ACL user's password. It is a file and not a variable because
	// `docker inspect` shows a container's environment to anyone who can ask the daemon (README「客户端与连接」).
	PasswordFileVar = "AXIOM_VALKEY_PASSWORD_FILE" //nolint:gosec // the name of a variable, not a credential
	// NodesVar is the number of core nodes, N: login's fallback divides by it and the heartbeat checks it.
	NodesVar = "AXIOM_CORE_NODES"
	// NodeIDVar names this node: the heartbeat row is its own, so a node that restarts after a crash replaces the row it
	// left and is not counted beside it, and only a different live node counts against N. serve defaults it to the host
	// name; two nodes must not share one (README「配置与启动」).
	NodeIDVar = "AXIOM_NODE_ID"
)

// maxNodeID bounds the identity: a name longer than this is a setting chosen wrong.
const maxNodeID = 128

// maxPassword bounds the password file: a password longer than this is a file chosen wrong.
const maxPassword = 128

// KeyReport is what the node's heartbeat says about the master key ring: the versions it has loaded and the one it
// writes. It carries identifiers only, never material; the protected key file is the distribution channel.
type KeyReport struct {
	Versions []uint32
	Active   uint32
}

// Config is the node's Valkey settings, read from the environment by serve.
type Config struct {
	URL          string // URLVar
	PasswordFile string // PasswordFileVar
	Nodes        int    // NodesVar: how many core nodes the deployment has, at least 1
	NodeID       string // NodeIDVar; empty makes a new identity at every start, which tests want
	// Keys reports the live ring, so the key ring's lifecycle can wait for every live node instead of stopping every
	// writer. Nil reports no versions, as a node without a key ring in a test does.
	Keys func() KeyReport
	// Namespace is the key format version, "v1" in production and one random per test case, so cases that share a
	// Valkey do not see each other's keys (README「键」). Empty is "v1".
	Namespace string

	resolved *endpoint // the URL parsed and the password read, by Resolve; Open reads it again only when it is missing
}

// DefaultNamespace is the key format version of production.
const DefaultNamespace = "v1"

// endpoint is the URL with the password read: everything one connection needs.
type endpoint struct {
	address  string // host:port, as valkey-go takes it
	username string
	password string
	tls      bool
}

// Resolve reports the configuration's own problems and keeps what it parsed, so that nothing is read twice. serve calls it
// with the other variables' checks, before the database is touched, and Open calls it again for a Config that was not
// resolved. Each problem names its variable and none quotes a value, which may be a secret pasted into the wrong one.
func (c *Config) Resolve() error {
	if c.Nodes < 1 {
		return fmt.Errorf("%s: want a whole number of at least 1", NodesVar)
	}
	if len(c.NodeID) > maxNodeID || strings.ContainsFunc(c.NodeID, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return fmt.Errorf("%s: want a name of at most %d printable characters without spaces", NodeIDVar, maxNodeID)
	}
	if c.URL == "" {
		if c.Nodes > 1 {
			return fmt.Errorf("%s: %d nodes need Valkey: set %s, or run a single node", NodesVar, c.Nodes, URLVar)
		}
		return nil
	}
	if c.resolved != nil {
		return nil
	}
	parsed, err := parseURL(c.URL)
	if err != nil {
		return err
	}
	if parsed.password, err = readPassword(c.PasswordFile); err != nil {
		return err
	}
	c.resolved = &parsed
	return nil
}

// parseURL reads the URL's parts without ever quoting it back: a password pasted into it must not reach the logs.
func parseURL(raw string) (endpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// Not err itself: url.Parse's error carries the URL's text, which may hold a password pasted into it.
		return endpoint{}, fmt.Errorf("%s: want valkey://[user@]host:port", URLVar)
	}
	if u.Scheme != "valkey" && u.Scheme != "valkeys" {
		return endpoint{}, fmt.Errorf("%s: want a valkey:// or valkeys:// URL", URLVar)
	}
	if err := checkShape(u); err != nil {
		return endpoint{}, err
	}
	if _, ok := u.User.Password(); ok {
		return endpoint{}, fmt.Errorf("%s: must not carry a password: put it in %s", URLVar, PasswordFileVar)
	}
	if port, err := strconv.ParseUint(u.Port(), 10, 16); err != nil || port == 0 {
		return endpoint{}, fmt.Errorf("%s: want a port such as 6379", URLVar)
	}
	// valkeys is for the machines of M3, checked against the system's roots; a private CA joins with that deployment.
	return endpoint{address: u.Host, username: u.User.Username(), tls: u.Scheme == "valkeys"}, nil
}

// checkShape holds the URL to the one form there is: a host, a port, a user, and nothing after them.
func checkShape(u *url.URL) error {
	switch {
	case u.Hostname() == "" || u.Port() == "":
		return fmt.Errorf("%s: want valkey://[user@]host:port", URLVar)
	case u.User.Username() == "":
		return fmt.Errorf("%s: want a user, such as valkey://axiom@valkey:6379", URLVar)
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "", u.Opaque != "":
		return fmt.Errorf("%s: want valkey://[user@]host:port and nothing else", URLVar)
	}
	return nil
}

// readPassword reads the ACL user's password, which the deployment writes as one line into a protected file, and holds
// it to the same protection as the master key files: the group and others may not read it. The check and the read are
// of one open file, so a path replaced after the check cannot become the password the node sends.
func readPassword(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s: required when %s is set", PasswordFileVar, URLVar)
	}
	file, err := privfile.Open(path, os.Open)
	if err != nil {
		return "", passwordError("open", err)
	}
	defer file.Close() //nolint:errcheck // A file opened only for reading has nothing to flush.
	raw, err := io.ReadAll(io.LimitReader(file, maxPassword+2))
	if err != nil {
		return "", passwordError("read", err)
	}
	password := strings.TrimRight(string(raw), "\r\n")
	switch {
	case password == "":
		return "", fmt.Errorf("%s: want a password", PasswordFileVar)
	case len(password) > maxPassword:
		return "", fmt.Errorf("%s: want a password of at most %d bytes", PasswordFileVar, maxPassword)
	case strings.ContainsFunc(password, func(r rune) bool { return r <= ' ' || r > '~' }):
		return "", fmt.Errorf("%s: want one line of printable characters without spaces", PasswordFileVar)
	}
	return password, nil
}

// passwordError names the variable and the operation, never the path or the value: either may be a secret pasted
// into the wrong setting.
func passwordError(operation string, err error) error {
	if errors.Is(err, privfile.ErrNotRegular) || errors.Is(err, privfile.ErrPermissions) {
		return fmt.Errorf("%s: %w", PasswordFileVar, err)
	}
	reason := "failed"
	switch {
	case errors.Is(err, os.ErrNotExist):
		reason = "file not found"
	case errors.Is(err, os.ErrPermission):
		reason = "permission denied"
	}
	return fmt.Errorf("%s: %s: %s", PasswordFileVar, operation, reason)
}
