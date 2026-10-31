package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"axiom/internal/platform/config"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/nodetls"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/telemetry"
	"axiom/internal/quality"
)

// probesListenAddr is where the node serves its probes: loopback unless a deployment needs otherwise.
func probesListenAddr(env *config.Env) string { return env.Addr(probesAddrVar, "127.0.0.1:8081") }

// nodeSettings are the node's configuration, all read from the environment before anything starts. A node without the
// core role has no database, key ring or queue settings: what it does not need, it must not be given.
type nodeSettings struct {
	roles                             nodeRoles
	healthMode                        quality.Mode
	modelVerification                 bool // whether the health loop verifies endpoints as the models they claim
	publicAddr, adminAddr, probesAddr string
	nodeAddr                          string
	coreURL                           *url.URL
	nodeTLS, clientTLS                *tls.Config
	adminSources                      []netip.Prefix
	startupTimeout, shutdownTimeout   time.Duration
	databaseURL                       string
	completionReserve                 int
	masterKeyFile, keyHashFile        string
	identityKeyFile                   string
	webAssetsDir                      string
	hashConcurrency                   int
	hashWait                          time.Duration
	trustedProxies                    []netip.Prefix
	valkey                            kv.Config
	smtp                              mail.Config
	objectstore                       objectstore.Config
	retentionURL                      string
	exchangeSource                    string
}

// readSettings reads and checks every variable the node's roles use, so a node started wrong fails with the whole list.
// A listener variable that belongs to another role is named as such; a state variable is left to Env's unknown list,
// so a node without the state never reads a database URL or a key file.
func readSettings(environ []string) (nodeSettings, error) {
	env := config.New(environ)
	roles, roleErr := parseRoles(env.String(nodeRolesVar, string(roleCore)))
	s := nodeSettings{
		roles:           roles,
		publicAddr:      env.Addr(publicAddrVar, ":8080"), // model calls, the console, payment callbacks
		probesAddr:      probesListenAddr(env),
		startupTimeout:  env.Duration(startupTimeoutVar, defaultStartupTimeout),
		shutdownTimeout: env.Duration(shutdownTimeoutVar, 30*time.Second),
	}
	var roleErrs []error
	if roleErr != nil {
		roleErrs = append(roleErrs, roleErr)
	} else if roles.has(roleCore) && roles.has(roleIngress) {
		roleErrs = append(roleErrs, fmt.Errorf("%s: core and ingress cannot be on one node", nodeRolesVar))
	} else if roles.has(roleCore) {
		roleErrs = readCoreSettings(env, &s)
	} else {
		roleErrs = readNonCoreSettings(env, &s)
	}
	if err := errors.Join(append(roleErrs, env.Err())...); err != nil {
		return nodeSettings{}, fmt.Errorf("configuration:\n%w", err)
	}
	return s, nil
}

// readCoreSettings reads what only the node that owns the state uses. Everything here is required or has a default that
// keeps a single machine working; the node roles decide whether this function runs at all.
func readCoreSettings(env *config.Env, s *nodeSettings) []error {
	var errs []error
	s.adminAddr = env.Addr(adminAddrVar, "127.0.0.1:8082") // the admin app; loopback until a deployment opens it
	if env.Set(coreURLVar) {
		errs = append(errs, fmt.Errorf("%s: only an ingress forwards to a core", coreURLVar))
	}
	s.healthMode = quality.Mode(env.String("AXIOM_HEALTH_MODE", ""))
	switch verify := env.String("AXIOM_MODEL_VERIFICATION", "on"); verify {
	case "on":
		s.modelVerification = true
	case "off":
	default:
		errs = append(errs, fmt.Errorf("AXIOM_MODEL_VERIFICATION: want on or off, got %q", verify))
	}
	s.exchangeSource = env.String("AXIOM_EXCHANGE_SOURCE", "manual")
	s.databaseURL = env.Required(pg.DatabaseURLVar)
	// The retention job connects as its own login role; without the URL it does not run, and a configured bucket without
	// it is a startup error in serveWith. It is optional so a deployment that archives nothing needs no third role.
	s.retentionURL = env.String(retentionDatabaseURLVar, "")
	s.completionReserve = env.Int(pg.CompletionReserveVar, pg.CompletionReserve, 1)
	s.masterKeyFile = env.Required(masterKeyFileVar)
	s.keyHashFile = env.Required(keyHashFileVar)
	// The identity key stands in for the client's session and end-user identifiers upstream, so rotating the key
	// hashes must not rotate it: the upstream's prompt caches are keyed by them (#205).
	s.identityKeyFile = env.Required(identityKeyFileVar)
	s.webAssetsDir = env.String(webAssetsDirVar, "")
	// The node's argon2id computations at once, and how long a sign-in waits for a turn (0: the module's defaults,
	// GOMAXPROCS and 2 seconds).
	s.hashConcurrency = env.Int(hashConcurrencyVar, 0, 1)
	s.hashWait = env.Duration(hashWaitVar, 0)
	s.trustedProxies = env.Prefixes(trustedProxiesVar)
	// The shared cache: without a URL the three uses stay in this process, as before the cache existed. The password
	// is read from its file by kv, which reports on it (internal/platform/kv/README.md「配置与启动」).
	s.valkey = kv.Config{
		URL:          env.String(kv.URLVar, ""),
		PasswordFile: env.String(kv.PasswordFileVar, ""),
		Nodes:        env.Int(kv.NodesVar, 1, 1),
		NodeID:       env.String(kv.NodeIDVar, hostName()),
	}
	// Email is optional: none of its variables set means the node starts with email disabled, and a partly set
	// configuration joins the problems above, so the node still reports everything wrong at once. The cache's own settings
	// are judged with the rest, so a node that is wrong about them does not touch the database.
	var healthErr error
	var modeErr error
	s.healthMode, modeErr = quality.ParseMode(string(s.healthMode))
	if modeErr != nil {
		healthErr = fmt.Errorf("AXIOM_HEALTH_MODE: %w", modeErr)
	}
	smtp, mailErr := mail.ReadConfig(env)
	// The bucket is optional: with none, the call log's retention keeps every partition. Read it here so a node whose
	// deployment sets it starts, and so half a configuration is a startup error rather than a surprise for the first
	// archive run; `axiom objectstore` and the backup job use the same client, the latter with its own bucket.
	var storeErr error
	s.objectstore, storeErr = objectstore.ReadConfig(env)
	var exchangeErr error
	if s.exchangeSource != "manual" && s.exchangeSource != "ecb" {
		exchangeErr = errors.New("AXIOM_EXCHANGE_SOURCE: want manual or ecb")
	}
	s.smtp = smtp
	sources, sourceErr := readAdminSources(env, s.adminAddr)
	if sourceErr != nil {
		errs = append(errs, sourceErr)
	} else {
		s.adminSources = sources
	}
	if err := readNodeTLS(env, s); err != nil {
		errs = append(errs, err)
	}
	return append(errs, mailErr, storeErr, s.valkey.Resolve(), healthErr, exchangeErr)
}

// readNonCoreSettings reads what a node that only forwards or only probes uses, and refuses the settings of the roles it
// does not have. A node without the core role never reads a database URL, a key file or the queue's settings, so those
// variables are reported by Env.Err as unknown and the node refuses to start rather than carry credentials it does not
// use.
func readNonCoreSettings(env *config.Env, s *nodeSettings) []error {
	errs := rejectCoreSettings(env)
	if !s.roles.has(roleIngress) {
		return append(errs, rejectIngressSettings(env, s)...)
	}
	return append(errs, readIngressSettings(env, s)...)
}

// rejectCoreSettings names the settings a node without the core role may not carry. Reading them here, rather than
// letting Env report them as unknown, says which role they belong to.
func rejectCoreSettings(env *config.Env) []error {
	var errs []error
	for _, name := range []string{adminAddrVar, adminSourcesVar, nodeAddrVar} {
		if env.Set(name) {
			errs = append(errs, fmt.Errorf("%s: not used by a node without the core role", name))
		}
	}
	return errs
}

// rejectIngressSettings names what only an ingress uses, for the ops node that has none of it.
func rejectIngressSettings(env *config.Env, s *nodeSettings) []error {
	var errs []error
	for _, name := range []string{publicAddrVar, coreURLVar, nodeTLSDirVar} {
		if env.Set(name) {
			errs = append(errs, fmt.Errorf("%s: not used by a node with the %s role", name, s.roles))
		}
	}
	return errs
}

// readIngressSettings reads the address an ingress forwards to and the certificates it presents to it.
func readIngressSettings(env *config.Env, s *nodeSettings) []error {
	var errs []error
	if value := env.Required(coreURLVar); value != "" {
		core, err := parseCoreURL(value)
		if err != nil {
			errs = append(errs, err)
		} else {
			s.coreURL = core
		}
	}
	if dir := env.Required(nodeTLSDirVar); dir != "" && s.coreURL != nil {
		client, err := nodetls.Client(dir, s.coreURL.Hostname())
		if err != nil {
			errs = append(errs, err)
		} else {
			s.clientTLS = client
		}
	}
	return errs
}

// readAdminSources is the admin listener's source restriction: the configured networks, or loopback when the listener is
// bound to loopback and nothing else is named. A listener on every address without a source list is a configuration
// error, so publishing the admin port cannot happen by forgetting one variable.
func readAdminSources(env *config.Env, adminAddr string) ([]netip.Prefix, error) {
	if env.Set(adminSourcesVar) {
		sources := env.Prefixes(adminSourcesVar)
		if len(sources) == 0 {
			return nil, nil // Prefixes reported the malformed value
		}
		return sources, nil
	}
	host, _, err := net.SplitHostPort(adminAddr)
	if err != nil {
		return nil, nil //nolint:nilerr // Addr already reported the malformed address; nothing can be served with it.
	}
	ip, err := netip.ParseAddr(host)
	if err == nil && ip.IsLoopback() {
		return []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}, nil
	}
	return nil, fmt.Errorf("%s: required when %s is not loopback", adminSourcesVar, adminAddrVar)
}

// readNodeTLS prepares the core's mutual-TLS listener when one is configured. The certificates live in the key ring's
// directory unless AXIOM_NODE_TLS_DIR names another, so one protected volume carries everything the node must keep
// secret (docs/security.md「密钥管理」). A certificate directory without the listener it belongs to is a contradiction.
func readNodeTLS(env *config.Env, s *nodeSettings) error {
	s.nodeAddr = env.Addr(nodeAddrVar, "")
	explicit := env.Set(nodeTLSDirVar)
	if s.nodeAddr == "" {
		if explicit {
			return fmt.Errorf("%s: set without %s", nodeTLSDirVar, nodeAddrVar)
		}
		return nil
	}
	dir := env.String(nodeTLSDirVar, "")
	if dir == "" && s.masterKeyFile != "" {
		dir = filepath.Dir(s.masterKeyFile)
	}
	server, err := nodetls.Server(dir)
	if err != nil {
		return err
	}
	s.nodeTLS = server
	return nil
}

// parseCoreURL checks the one address an ingress forwards to. Node-to-node traffic is always mutual TLS, so the scheme is
// https and the certificate must carry the URL's host; credentials in the URL would end up in errors and logs.
func parseCoreURL(value string) (*url.URL, error) {
	core, err := url.Parse(value)
	if err != nil || core.Scheme != "https" || core.Host == "" || core.Hostname() == "" || core.User != nil ||
		(core.Path != "" && core.Path != "/") || core.RawQuery != "" || core.Fragment != "" {
		return nil, fmt.Errorf("%s: want an https URL such as https://core.internal:8083, with no path, query or credentials", coreURLVar)
	}
	return core, nil
}

// hostName is the node's identity unless AXIOM_NODE_ID names it: a container keeps its host name when it restarts, so a
// restarted node finds its own heartbeat row and replaces it. A host that cannot say its name gets a new identity at every
// start (kv.Config.NodeID), which is the cost of a restart being counted beside the old row for half a minute.
func hostName() string {
	name, _ := os.Hostname() //nolint:errcheck // the fallback is described above
	return name
}

// requestTelemetry builds the request-level traces and metrics both the core and the forwarding nodes report through.
func requestTelemetry(tel *telemetry.Telemetry) (httpserver.Telemetry, error) {
	return httpserver.NewTelemetry(tel.Traces, tel.Metrics)
}
