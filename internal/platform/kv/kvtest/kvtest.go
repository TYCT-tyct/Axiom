// Package kvtest gives a test a real Valkey, with the ACL user and the configuration a deployment gives it
// (internal/platform/kv/README.md「测试」).
package kvtest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	mrand "math/rand/v2"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"axiom/internal/platform/containertest"
	"axiom/internal/platform/kv"
)

const (
	// URLEnv names the server that withcluster starts for a whole run: the tests read it the way serve reads its own, so
	// one test of a run costs no container.
	URLEnv = "KVTEST_URL"
	// PasswordFileEnv names the file that holds the password of that server's axiom user.
	PasswordFileEnv = "KVTEST_PASSWORD_FILE"
	// AdminPasswordFileEnv names the file that holds the password of that server's admin user, who may read every key
	// so that a test of a lifetime or a key list costs no container of its own.
	AdminPasswordFileEnv = "KVTEST_ADMIN_PASSWORD_FILE" //nolint:gosec // the name of a variable, not a credential

	// image is the tag deploy/compose.yaml pins: a test that passed on another version proves another version.
	image = "valkey/valkey:9.1.2-alpine"
	// port is the container's, mapped to a free host port; the ACL file is copied in before the server starts.
	port    = "6379/tcp"
	aclPath = "/etc/valkey/users.acl"
	// startTimeout covers a first pull of the image.
	startTimeout = 2 * time.Minute
	// wideAccess keeps tests of server replies independent of the production latency budget.
	wideAccess = time.Minute
)

// Server is a Valkey a test may use: the settings kv.Open takes, and the password file a deployment reads.
type Server struct {
	URL          string // valkey://axiom@host:port
	PasswordFile string // the file that holds the axiom user's password

	// Admin runs commands as the admin user: every permission on a container of this test's own, for CLIENT PAUSE, a
	// memory limit filled to the brim, or a restart, and read-only on the server a run shares, so that a test may read a
	// lifetime or list a namespace without starting a container. Do refuses the shared server; Read does not.
	Admin valkey.Client
	// box is the container of this test's own, which Stop and Start restart; nil for the shared server.
	box *box
}

// New is the server of the run: the one withcluster started when its three variables are set, and a container of this
// test's own otherwise, so `go test ./internal/platform/kv` works alone.
func New(t testing.TB) *Server {
	t.Helper()
	address, file, adminFile := shared()
	if address == "" || file == "" || adminFile == "" {
		return Own(t)
	}
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse %s: %v", URLEnv, err)
	}
	return &Server{
		URL: address, PasswordFile: file,
		Admin: dial(t, parsed.Host, "admin", readPassword(t, adminFile)),
	}
}

// Own is a container of this test's own, which it may pause, restart, or fill: a test that changes the server's state
// uses one, so that no other test of the run sees the change.
func Own(t testing.TB, opts ...Option) *Server {
	t.Helper()
	settings := options{memory: defaultMemory, policy: defaultPolicy}
	for _, opt := range opts {
		opt(&settings)
	}
	var err error
	password, admin := newPassword(), newPassword()
	dir := t.TempDir()
	file := filepath.Join(dir, "password")
	if err := os.WriteFile(file, []byte(password+"\n"), 0o600); err != nil {
		t.Fatalf("write the password file: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), startTimeout)
	t.Cleanup(cancel)
	b, err := startOnAPort(ctx, acl(password, admin, false), settings)
	if err != nil {
		t.Fatalf("start valkey: %v", err)
	}
	t.Cleanup(func() {
		if err := b.terminate(context.WithoutCancel(ctx)); err != nil {
			t.Errorf("remove the valkey container: %v", err)
		}
	})
	return &Server{
		URL: "valkey://axiom@" + b.address, PasswordFile: file,
		Admin: dial(t, b.address, "admin", admin), box: b,
	}
}

// Stop halts the server and keeps its container, for the test of a restart: the port and the ACL file stay, and the
// dataset does not, which is a deployment's restart (README「不持久化」).
func (s *Server) Stop(t testing.TB) {
	t.Helper()
	if s.box == nil {
		t.Skip("the server that a run shares is not for a test that changes its state")
	}
	ctx, cancel := context.WithTimeout(t.Context(), startTimeout)
	defer cancel()
	if err := s.box.stop(ctx); err != nil {
		t.Fatalf("stop valkey: %v", err)
	}
}

// Start brings a server Stop halted back, on the same address.
func (s *Server) Start(t testing.TB) {
	t.Helper()
	if s.box == nil {
		t.Skip("the server that a run shares is not for a test that changes its state")
	}
	ctx, cancel := context.WithTimeout(t.Context(), startTimeout)
	defer cancel()
	if err := s.box.start(ctx); err != nil {
		t.Fatalf("start valkey again: %v", err)
	}
}

// Option changes the server of a container.
type Option func(*options)

type options struct {
	memory   string
	policy   string
	hostPort string // the port on this machine, fixed so that a restarted container is found where it was
}

const (
	defaultMemory = "256mb"
	defaultPolicy = "noeviction"
)

// startOnAPort starts a server of a test's own on a port of this machine that it chooses. Docker gives a restarted
// container a new port when it is left to choose one, and a client dials the address it was given: a test that restarts the
// server needs the same port back. The port is picked below the range Docker and the system hand out, and another test
// may pick the same one between the check and the bind, so a bind that fails is tried again on another port.
func startOnAPort(ctx context.Context, users string, settings options) (*box, error) {
	var err error
	for range 8 {
		if settings.hostPort, err = unusedPort(ctx); err != nil {
			return nil, err
		}
		b, startErr := startServer(ctx, users, settings)
		if startErr == nil {
			return b, nil
		}
		if err = startErr; !strings.Contains(startErr.Error(), "programming external connectivity") {
			break
		}
	}
	return nil, err
}

// unusedPort is a port from 20000 to 29999 that nothing on this machine listens on now.
func unusedPort(ctx context.Context) (string, error) {
	for range 100 {
		port := strconv.Itoa(20000 + mrand.IntN(10000)) //nolint:gosec // a port to try, not a secret
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
		if err == nil {
			return port, listener.Close()
		}
	}
	return "", errors.New("no unused port from 20000 to 29999")
}

// Memory sets maxmemory, for the test that fills the server: a limit small enough to fill in a test.
func Memory(limit string) Option { return func(o *options) { o.memory = limit } }

// Policy sets maxmemory_policy, for the test of a server that would evict a key.
func Policy(policy string) Option { return func(o *options) { o.policy = policy } }

// Namespace is a random key namespace: two test cases sharing one server do not see each other's keys (README「键」).
func Namespace() string {
	var raw [16]byte
	rand.Read(raw[:]) // never fails: crypto/rand.Read crashes the program instead
	return hex.EncodeToString(raw[:])
}

// Deps applies the test deadline unless the test sets one. Tests that call kv.Open directly to inspect startup errors
// use it too; otherwise they inherit the production latency budget even when they only test the server's reply.
func Deps(deps kv.Deps) kv.Deps {
	if deps.AccessTimeout == 0 {
		deps.AccessTimeout = wideAccess
	}
	return deps
}

// Config is this server's settings under namespace: the three variables' values as serve would hold them.
func (s *Server) Config(namespace string) kv.Config {
	return kv.Config{URL: s.URL, PasswordFile: s.PasswordFile, Nodes: 1, Namespace: namespace}
}

// Open connects a client of this test's own, in a namespace of its own, and closes it when the test ends.
func (s *Server) Open(t testing.TB, deps kv.Deps) *kv.Client {
	t.Helper()
	return s.OpenIn(t, Namespace(), deps)
}

// OpenIn is Open in the given namespace: two clients of one namespace are two nodes of one deployment.
func (s *Server) OpenIn(t testing.TB, namespace string, deps kv.Deps) *kv.Client {
	t.Helper()
	client, err := kv.Open(t.Context(), s.Config(namespace), Deps(deps))
	if err != nil {
		t.Fatalf("open valkey: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		if err := client.Close(ctx); err != nil {
			t.Errorf("close valkey: %v", err)
		}
	})
	return client
}

// Do runs commands as the admin user and returns their replies, for the tests that change the server's state.
func (s *Server) Do(t testing.TB, ctx context.Context, cmds ...valkey.Completed) []valkey.ValkeyResult {
	t.Helper()
	if s.box == nil {
		t.Skip("the server that a run shares is not for a test that changes its state")
	}
	return s.Read(t, ctx, cmds...)
}

// Read runs commands as the admin user and returns their replies. It works on the shared server, whose admin user may
// only read: a lifetime (PTTL), a key list (KEYS), or a scan.
func (s *Server) Read(t testing.TB, ctx context.Context, cmds ...valkey.Completed) []valkey.ValkeyResult {
	t.Helper()
	if s.Admin == nil {
		t.Fatal("this server has no admin user")
	}
	results := s.Admin.DoMulti(ctx, cmds...)
	for i, result := range results {
		if err := result.Error(); err != nil {
			t.Fatalf("admin command %d: %v", i, err)
		}
	}
	return results
}

// TTL is key's remaining life in milliseconds, read as the admin user: reading it needs no container of the test's own.
func (s *Server) TTL(t testing.TB, ctx context.Context, key string) (int64, error) {
	t.Helper()
	if s.Admin == nil {
		t.Fatal("this server has no admin user")
	}
	return s.Admin.Do(ctx, s.Admin.B().Pttl().Key(key).Build()).AsInt64()
}

// Down is a client of an address where nothing listens: the outage a deployment has when its server is gone, without a
// container to stop. Under train load, waiting for the container was what made an outage test hang, not the outage.
func Down(t testing.TB) *kv.Client {
	t.Helper()
	file := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(file, []byte(newPassword()+"\n"), 0o600); err != nil {
		t.Fatalf("write the password file: %v", err)
	}
	var listen net.ListenConfig
	listener, err := listen.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find an address nothing listens on: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close the address: %v", err)
	}
	client, err := kv.Open(t.Context(), kv.Config{
		URL: "valkey://axiom@" + address, PasswordFile: file, Nodes: 1, Namespace: Namespace(),
	}, Deps(kv.Deps{AccessTimeout: kv.Timeout}))
	if err != nil {
		t.Fatalf("open a client of an address nothing listens on: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		if err := client.Close(ctx); err != nil {
			t.Errorf("close the down client: %v", err)
		}
	})
	return client
}

// Client connects as the deployed user, axiom, for the tests of what its ACL refuses: no other test can run a command
// the program does not send.
func (s *Server) Client(t testing.TB) valkey.Client {
	t.Helper()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatalf("parse %s: %v", URLEnv, err)
	}
	return dial(t, u.Host, u.User.Username(), readPassword(t, s.PasswordFile))
}

// StartServer runs the container a whole run shares and returns its URL, the two files its axiom and admin passwords
// were written to, and the function that removes them. The ACL user and the configuration are a deployment's, because
// the tests must run as the user a deployment gives them; the admin user may only read, because another test's keys are
// in this server too.
func StartServer(ctx context.Context) (url, passwordFile, adminPasswordFile string, terminate func(context.Context) error, err error) {
	password, admin := newPassword(), newPassword()
	dir, err := os.MkdirTemp("", "kvtest-")
	if err != nil {
		return "", "", "", nil, err
	}
	passwordFile, adminPasswordFile = filepath.Join(dir, "password"), filepath.Join(dir, "admin-password")
	for _, f := range []struct{ path, password string }{{passwordFile, password}, {adminPasswordFile, admin}} {
		if err := os.WriteFile(f.path, []byte(f.password+"\n"), 0o600); err != nil {
			return "", "", "", nil, errors.Join(err, os.RemoveAll(dir))
		}
	}
	b, err := startServer(ctx, acl(password, admin, true), options{memory: defaultMemory, policy: defaultPolicy})
	if err != nil {
		return "", "", "", nil, errors.Join(err, os.RemoveAll(dir))
	}
	terminate = func(ctx context.Context) error {
		return errors.Join(b.terminate(ctx), os.RemoveAll(dir))
	}
	return "valkey://axiom@" + b.address, passwordFile, adminPasswordFile, terminate, nil
}

// shared is the server withcluster started. Its URL and password files identify infrastructure, not test inputs.
func shared() (address, passwordFile, adminPasswordFile string) {
	return containertest.Untracked(URLEnv), containertest.Untracked(PasswordFileEnv), containertest.Untracked(AdminPasswordFileEnv)
}

// readPassword reads a password file as a deployment wrote it.
func readPassword(t testing.TB, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- the file the test's own server was given
	if err != nil {
		t.Fatalf("read the password file: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

// acl is the ACL file: the axiom user with the commands of README「权限」 under ax:*, `default` off, and an admin user
// with every permission on a test's own container or reads only on the shared one. Only a password's SHA-256 goes in the
// file, as in a deployment.
func acl(password, admin string, readOnly bool) string {
	users := []string{
		"user default off",
		"user axiom on #" + digest(password) + " ~ax:* +hello +auth +ping +client|setinfo +info +time +get +set +del " +
			"+incr +pexpire +eval",
	}
	if admin != "" {
		commands := "+@all"
		if readOnly {
			commands = "+hello +auth +ping +client|setinfo +get +pttl +keys +scan +info +time"
		}
		users = append(users, "user admin on #"+digest(admin)+" ~* "+commands)
	}
	return strings.Join(users, "\n") + "\n"
}

// digest is a password as the ACL file holds it.
func digest(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

// newPassword is a random password of 32 bytes, printable and without spaces, as a deployment's is. crypto/rand.Read
// does not return an error: it ends the program when the system has no randomness.
func newPassword() string {
	var raw [32]byte
	rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

// dial connects as a user, for the admin commands.
func dial(t testing.TB, address, user, password string) valkey.Client {
	t.Helper()
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{address}, Username: user, Password: password,
		AlwaysPipelining: true, ForceSingleClient: true, DisableCache: true, DisableRetry: true,
		ClientSetInfo: valkey.DisableClientSetInfo,
	})
	if err != nil {
		t.Fatalf("connect as %s: %v", user, err)
	}
	t.Cleanup(client.Close)
	return client
}
