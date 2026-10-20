package kv

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
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
)

// maxPassword bounds the password file: a password longer than this is a file chosen wrong.
const maxPassword = 128

// Config is the node's Valkey settings, read from the environment by serve.
type Config struct {
	URL          string // URLVar
	PasswordFile string // PasswordFileVar
	Nodes        int    // NodesVar: how many core nodes the deployment has, at least 1
	// Namespace is the key format version, "v1" in production and one random per test case, so cases that share a
	// Valkey do not see each other's keys (README「键」). Empty is "v1".
	Namespace string
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

// Check reports the configuration's own problems, before anything is reached (serve calls it with the other variables'
// checks, so the database is not touched for them): each names its variable and none quotes a value, which may be a
// secret pasted into the wrong one.
func (c Config) Check() error {
	if c.Nodes < 1 {
		return fmt.Errorf("%s: want a whole number of at least 1", NodesVar)
	}
	if c.URL == "" && c.Nodes > 1 {
		return fmt.Errorf("%s: %d nodes need Valkey: set %s, or run a single node", NodesVar, c.Nodes, URLVar)
	}
	return nil
}

// endpoint parses the URL and reads the password file.
func (c Config) endpoint() (endpoint, error) {
	parsed, err := parseURL(c.URL)
	if err != nil {
		return endpoint{}, err
	}
	if parsed.password, err = readPassword(c.PasswordFile); err != nil {
		return endpoint{}, err
	}
	return parsed, nil
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
// it to the same protection as the master key files: the group and others may not read it.
func readPassword(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s: required when %s is set", PasswordFileVar, URLVar)
	}
	file, err := os.Open(path) // #nosec G304 -- the trusted startup configuration chooses the file
	if err != nil {
		return "", passwordError("open", err)
	}
	defer file.Close() //nolint:errcheck // A file opened only for reading has nothing to flush.
	info, err := file.Stat()
	if err != nil {
		return "", passwordError("stat", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: want a regular file", PasswordFileVar)
	}
	// Windows mode bits cannot express NTFS ACLs; deployment must restrict the file's ACL instead.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s: permissions must deny all group and other access", PasswordFileVar)
	}
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
	reason := "failed"
	switch {
	case errors.Is(err, os.ErrNotExist):
		reason = "file not found"
	case errors.Is(err, os.ErrPermission):
		reason = "permission denied"
	}
	return fmt.Errorf("%s: %s: %s", PasswordFileVar, operation, reason)
}
