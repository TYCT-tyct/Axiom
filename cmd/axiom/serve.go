package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/gateway"
	"axiom/internal/observability"
	"axiom/internal/operations"
	"axiom/internal/payments"
	"axiom/internal/platform/alert"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/kv"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/platform/telemetry"
	"axiom/internal/platform/webui"
	"axiom/internal/suppliers"
)

// The call-record writer's sizes. Capacity is about the node's in-flight limit (internal/gateway README「并发、背压与
// 共享状态」): every admitted call reserves one record, so a full spill refuses new calls instead of dropping a record.
// Batches of 500 records or 200 ms keep the insert rate low (internal/observability README「写入」). Dir comes from
// AXIOM_CALL_LOG_DIR; cmd/axiom/settings.go defaults it to ./call-log.
var callRecords = observability.Config{
	Capacity: 5000, BatchSize: 500, FlushInterval: 200 * time.Millisecond, RetryBase: 100 * time.Millisecond,
	RetryMax:     5 * time.Second,
	WriteTimeout: 10 * time.Second,
}

// bodyTimeout bounds how long a client may take to send a request body. Generous for large uploads over slow
// links; the gateway sets size limits and per-route deadlines on top.
const bodyTimeout = 2 * time.Minute

// defaultStartupTimeout bounds how long a command waits for the database before it starts.
const defaultStartupTimeout = 30 * time.Second

// listening is one of the node's HTTP servers and the address variable it listens on.
type listening struct {
	variable, addr string
	server         *http.Server
	listener       net.Listener
	tls            bool // mutual TLS with the deployment's node certificates
}

// serve runs the node until ctx ends, then shuts down: the node reports not ready, its listeners stop accepting,
// and requests in flight and the jobs of the queue get up to AXIOM_SHUTDOWN_TIMEOUT to finish. It refuses to start against a database whose
// login escapes row-level security, that lacks this program's migrations, or that does not answer within
// AXIOM_STARTUP_TIMEOUT, with key files it cannot read, or with a console contract it cannot enforce.
func serve(ctx context.Context, environ []string, logger *slog.Logger) error {
	return serveWith(ctx, environ, logger, directEgress, startRecorder, catalog.NewFetcher, gateway.DefaultConfig(), func([]*listening) {})
}

// directEgress is the client a node reaches its suppliers through in production.
func directEgress(tel *telemetry.Telemetry) (*egress.Client, error) {
	return egress.New(egress.Direct, egress.WithTracerProvider(tel.Traces))
}

// serveWith lets a test capture its recorder, opt into price syncing with local datasets, and choose the node's
// in-process limits. A nil prices constructor disables the periodic price job, so tests of the listeners and call path
// never download the production datasets; a test world passes limits sized for the shared machines it runs on, not
// production's. bound observes the claimed listeners before the database check; tests read their actual addresses
// and check closure on the same handles, without trying to reclaim ports another process may have taken.
//
//nolint:funlen,cyclop // startup order is the contract: listeners drain before the recorder, the queue, the cache and the database.
func serveWith(ctx context.Context, environ []string, logger *slog.Logger, outbound func(*telemetry.Telemetry) (*egress.Client, error),
	record func(context.Context, *pg.DB, *telemetry.Telemetry, string, *slog.Logger) (*observability.Recorder, error),
	prices func(*egress.Client) *catalog.Fetcher, limits gateway.Config, bound func([]*listening),
) error {
	cfg, err := readSettings(environ)
	if err != nil {
		return err
	}
	if !cfg.roles.has(roleCore) {
		return serveWithoutCore(ctx, cfg, logger)
	}
	console, err := loadConsole(cfg, limits)
	if err != nil {
		return err
	}
	archive, err := openBucket(cfg.objectstore)
	if err != nil {
		return fmt.Errorf("object store:\n%w", err)
	}
	var archiveStore observability.ArchiveStore
	if archive != nil {
		archiveStore = archive
		defer archive.Close()
	}
	replica, closeReplica, err := openReplica(cfg.auditReplica)
	if err != nil {
		return err
	}
	defer closeReplica()
	// Claim every listener before the slow steps (database check, cache, queue, call path), so a taken address is named
	// at once instead of after them. The same listeners serve; shutdown closes them, and a failed start closes them here.
	servers := listeners(cfg)
	if err := listen(ctx, servers); err != nil {
		return err
	}
	defer closeListeners(servers)
	bound(servers)
	tel, db, master, err := openCoreNode(ctx, cfg, console.master, logger)
	if err != nil {
		return err
	}
	defer tel.Shutdown(context.WithoutCancel(ctx)) //nolint:errcheck // nothing exports yet, so there is nothing to lose
	defer db.Close()                               // after the listeners and the recorder close: requests in flight may still need it
	retentionDB, err := openRetentionDB(ctx, cfg.retentionURL, archive != nil)
	if err != nil {
		return err
	}
	if retentionDB != nil {
		defer retentionDB.Close()
	}
	console.master = master // the same ring, with the check's write version

	exit, err := outbound(tel)
	if err != nil {
		return err
	}
	syncCtx, cancelSync := context.WithCancel(ctx)
	defer cancelSync()
	// The shared store opens before the queue: the mail worker's recipient throttles live in it. The heartbeat is deleted
	// on every way out, so a restart is not refused for a row that only a crash would leave. It runs once the listeners
	// have drained and nothing prechecks any more, and before the database closes.
	cache, stopCache, err := openNodeCache(ctx, syncCtx, cfg, console.master, db, tel, logger)
	if err != nil {
		return err
	}
	defer stopCache()
	queue, recorder, recovery, registration, signals, paymentSignals, err := startBackground(syncCtx, db, retentionDB, console.master, console.masterKeys, cfg.smtp, cache, exit, prices, cfg.exchangeSource, archiveStore, replica, tel, logger, cfg.valkey.NodeID, cfg.shutdownTimeout, cfg.callLogDir, record)
	if err != nil {
		return err
	}
	console.paymentSignals = paymentSignals
	defer startAlerts(ctx, db, recorder, &console, logger, signals)()
	// Every way out closes the recorder, or its writer would outlive a failed start. The normal shutdown closes it
	// earlier, after the listeners drain and with the shutdown deadline; Close is idempotent, so this one then
	// returns at once. It runs before db.Close, which was deferred first.
	defer closeRecorder(ctx, recorder, cfg.shutdownTimeout, logger)
	// Like the recorder, the queue is stopped on every way out; the normal shutdown stops it earlier, with the shutdown
	// deadline, and a second Stop does nothing.
	defer func() {
		cancelSync()
		stopQueue(ctx, queue, cfg.shutdownTimeout, logger)
	}()
	requests, err := requestTelemetry(tel)
	if err != nil {
		return err
	}
	routes, err := console.routes(ctx, db, tel, logger, recorder, exit, cache, recovery, registration)
	if err != nil {
		return err
	}
	defer routes.calls.stop() // every way out ends the loops; the normal shutdown does it earlier, after the drain
	return runNode(ctx, cfg, servers, routes, tel, requests, logger, queue, recorder, cache, cancelSync)
}

// runNode attaches the handlers to the listeners startup claimed, serves until ctx ends, and then shuts the node down
// in stopNode's order.
func runNode(ctx context.Context, cfg nodeSettings, servers []*listening, routes nodeRoutes, tel *telemetry.Telemetry, requests httpserver.Telemetry,
	logger *slog.Logger, queue *pg.Queue, recorder *observability.Recorder, cache *kv.Client, cancelSync context.CancelFunc,
) error {
	health := httpserver.Health{Metrics: tel.MetricsHandler()}
	node(servers, cfg, routes, &health, logger, requests)
	served := startServing(cfg.roles, servers, logger)
	// Not ready until a routing snapshot can route, which startCallPath has already published or will on its next
	// rebuild.
	stopAnnouncing := announceReady(ctx, routes.calls.ready, readinessEvery, &health, logger)
	stopped := awaitEnd(ctx, served, routes.calls.reaped, cache.Lost())
	cancelSync()
	stopAnnouncing()
	logger.Info("shutting down", "timeout", cfg.shutdownTimeout.String(), "edge_delay", drainDelay(cfg.shutdownTimeout).String())
	err := stopNode(context.WithoutCancel(ctx), cfg.shutdownTimeout, &health, servers, routes.calls, queue, recorder)
	return errors.Join(stopped, err)
}

// stopNode stops what serve started, in an order that loses nothing it need not: it reports not ready and refuses new
// requests, waits for the edge to stop routing here, then stops the listeners and drains the calls and their settlements
// together under one deadline, so a long stream gets the whole AXIOM_SHUTDOWN_TIMEOUT and the two stages cannot add up
// to more. The calls are aborted and settled within the same deadline. Then the loops that keep the holder alive end,
// and the queue and the recorder, which the calls may still have fed, drain together under a deadline of their own: a
// slow job cannot use up the recorder's time, and a stream that used the drain's time does not take their deadline with
// it. Health also finishes bounded paid probes and flushes their costs before the database closes;
// deploy/compose.yaml's stop_grace_period covers every stage and the edge delay.
func stopNode(ctx context.Context, timeout time.Duration, health *httpserver.Health, servers []*listening,
	calls callNode, queue *pg.Queue, recorder *observability.Recorder,
) error {
	beginDrain(health, timeout)
	drainCtx, cancelDrain := context.WithTimeout(ctx, timeout)
	defer cancelDrain()
	// The listeners and the calls wait on the same deadline: a call in flight is also a request the listener waits for,
	// so running the two side by side makes the deadline the whole answer instead of one stage's share of it. The
	// calls abort early enough inside it to settle.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		calls.Drain(drainCtx)
	}()
	err := shutdown(drainCtx, servers)
	<-drained
	calls.stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var queueErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		queueErr = queue.Stop(ctx)
	}()
	recorderErr := recorder.Close(ctx)
	<-done
	return errors.Join(err, queueErr, recorderErr)
}

// awaitEnd waits until the node is asked to stop (nil), a listener stops, the billing loop ends because this node's
// holder was reaped, or another process takes the node's name (kv.Client.Lost), and says which of the last three it was.
// A database out of reach is none of them: the node serves through it and takes its name back after.
func awaitEnd(ctx context.Context, served <-chan error, reaped <-chan error, lost <-chan struct{}) error {
	select {
	case <-lost:
		return errors.New("another process took this node's name in the database while this one could not reach it: two processes must not run as one node")
	case err := <-served:
		return fmt.Errorf("a listener stopped: %w", err)
	case err := <-reaped:
		return fmt.Errorf("this node's holder is gone: %w", err)
	case <-ctx.Done():
		return nil
	}
}

// consoleParts are what the node builds from its configuration before it touches the database: the secrets read from
// protected files, whose errors name the variable and never the path or the contents, and the contract the guard
// enforces. The master key file is read once here; the database check (openCheckedDatabase) gives that one ring the
// write version the database chose, and the login and mail keys were derived from the same snapshot.
type consoleParts struct {
	cfg           nodeSettings
	gate          gateway.Config     // the in-process limits the call path and the accounts module share
	masterKeys    *secret.MasterKeys // the one master-key read; the mail worker derives the recipient key from it
	master        *secret.Keyring    // seals users' TOTP secrets and suppliers' credentials; openCheckedDatabase sets its write version
	loginKey      secret.LoginKey    // from the same master-key snapshot, under the login label
	hashes        *secret.MACKeys    // keys API keys are hashed with
	identity      []byte             // derives what replaces the client's identifiers upstream
	contract      *contract
	adminContract *contract
	// operationsContract is read even when the operations listener is off, so a contract this program cannot enforce
	// fails every start, not the first one that opens the listener.
	operationsContract *contract
	frontend           *webui.Frontend // the pages of the three apps
	alerts             alert.Sink
	paymentSignals     *payments.SignalState

	sessionCursorKey [32]byte // derived from the master snapshot under its own cursor label
}

func loadConsole(cfg nodeSettings, limits gateway.Config) (consoleParts, error) {
	// One read of the master key file gives the ring openCheckedDatabase checks and the snapshot the login and mail keys
	// are derived from under labels of their own, so a replacement during startup cannot mix versions and neither key
	// follows the hash keys' rotation (internal/platform/kv/README.md「键」). The plaintext stays in secret.
	master, err := secret.LoadMaster(cfg.masterKeyFile)
	if err != nil {
		return consoleParts{}, err
	}
	loginKey, err := master.LoginKey()
	if err != nil {
		return consoleParts{}, err
	}
	sessionCursorKey, err := master.DeriveKey("axiom console call sessions cursor v1")
	if err != nil {
		return consoleParts{}, err
	}
	hashes, err := secret.LoadMAC(cfg.keyHashFile)
	if err != nil {
		return consoleParts{}, err
	}
	identity, err := secret.LoadSecret(identityKeyFileVar, cfg.identityKeyFile)
	if err != nil {
		return consoleParts{}, err
	}
	console, err := consoleContract()
	if err != nil {
		return consoleParts{}, err
	}
	admin, err := adminContract()
	if err != nil {
		return consoleParts{}, err
	}
	operations, err := operationsContract()
	if err != nil {
		return consoleParts{}, err
	}
	frontend, err := webui.Open(cfg.webAssetsDir)
	if err != nil {
		return consoleParts{}, err
	}
	return consoleParts{
		cfg: cfg, gate: limits, masterKeys: master, master: master.Keyring(), loginKey: loginKey, sessionCursorKey: sessionCursorKey, hashes: hashes, identity: identity, contract: console, adminContract: admin, operationsContract: operations, frontend: frontend,
	}, nil
}

// startSync starts the queue with supplier syncing and the price datasets' daily round on the syncs queue, and charge
// reconciliation, the ledger's daily check and the expiry of credit on the money queue. Each queue has its own workers,
// so a burst of syncs cannot hold a money job back (internal/platform/README.md「任务队列」). It reaches the suppliers and
// the datasets through exit, the client the whole node shares.
func startSync(ctx context.Context, db, retentionDB *pg.DB, smtp mail.Config, cache *kv.Client, master *secret.MasterKeys,
	keys *secret.Keyring, exit *egress.Client, prices func(*egress.Client) *catalog.Fetcher, exchangeSource string,
	archive observability.ArchiveStore, replica *operations.Replica, tel *telemetry.Telemetry,
	logger *slog.Logger, signals *alert.Bus, paymentSignals *payments.SignalState, nodeID string,
) (*pg.Queue, *accounts.PasswordRecovery, *accounts.Registration, error) {
	queue := db.NewQueue(logger, pg.PlatformScope(), tel.Metrics.Meter("axiom/pg"))
	recovery, registration, err := registerNotifications(ctx, db, queue, smtp, cache, master, keys, exit, logger)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := suppliers.RegisterSync(suppliers.SyncConfig{
		DB: db, Scope: pg.PlatformScope(), Keys: keys, Egress: exit, Queue: queue, Logger: logger,
	}); err != nil {
		return nil, nil, nil, err
	}
	if err := registerCatalogJobs(ctx, db, queue, exit, prices, tel, logger); err != nil {
		return nil, nil, nil, err
	}
	if err := registerExchangeSource(ctx, db, queue, exit, logger, exchangeSource); err != nil {
		return nil, nil, nil, err
	}
	if err := registerSalePrices(db, queue); err != nil {
		return nil, nil, nil, err
	}
	// What the operations agent asked the owner to approve expires once its time is up, on whichever node leads the
	// queue (docs/domains/operations.md「风险分级与审批」).
	// The cap counters are added up again from the money they count; a scope where they differ is paused until the
	// owner repairs it (docs/domains/operations.md「转账的累计金额上限」).
	// Every committed audit entry is linked into a hash chain after it commits, and the whole chain is checked daily
	// and copied to the replica bucket; each UTC day ends with the owner's digest (docs/domains/operations.md「审计」).
	if err := errors.Join(operations.RegisterExpiry(db, pg.PlatformScope(), queue, logger),
		operations.RegisterReconcile(db, pg.PlatformScope(), queue, logger),
		operations.RegisterAuditChain(db, pg.PlatformScope(), queue, logger),
		operations.RegisterAuditDigest(db, pg.PlatformScope(), queue, logger),
		registerAuditReplica(db, queue, replica, logger)); err != nil {
		return nil, nil, nil, err
	}

	if err := registerBillingJobs(db, queue, tel, logger); err != nil {
		return nil, nil, nil, err
	}
	// The call log's partitions move to the object store and leave PostgreSQL on the archive queue; with no bucket
	// they stay, because a drop without a confirmed archive loses records. It connects as its own login, so SQL run as
	// the application cannot own or drop the log tables.
	if err := registerRetention(retentionDB, archive, queue, tel, logger); err != nil {
		return nil, nil, nil, err
	}
	if err := suppliers.RegisterReconcile(suppliers.ReconcileJobConfig{
		DB: db, Scope: pg.PlatformScope(), Queue: queue, Logger: logger,
	}); err != nil {
		return nil, nil, nil, err
	}
	// Orders past their deadline expire every minute, on whichever node leads the queue
	// (docs/domains/payments.md「补单与过期」). Callbacks and polling register the same protocol plugins.
	if err = registerPaymentJobs(db, keys, exit, logger, queue, signals, paymentSignals, nodeID); err != nil {
		return nil, nil, nil, err
	}
	if err := queue.Start(ctx); err != nil {
		return nil, nil, nil, err
	}
	return queue, recovery, registration, nil
}

// registerCatalogJobs keeps both catalog schedules on the node's shared queue and egress client.
func registerCatalogJobs(ctx context.Context, db *pg.DB, queue *pg.Queue, exit *egress.Client,
	prices func(*egress.Client) *catalog.Fetcher, tel *telemetry.Telemetry, logger *slog.Logger,
) error {
	if err := catalog.RegisterPages(ctx, catalog.PageJobConfig{DB: db, Scope: pg.PlatformScope(), Queue: queue, Client: exit, Logger: logger}); err != nil {
		return err
	}
	if prices == nil {
		return nil
	}
	return catalog.RegisterSync(ctx, catalog.SyncJobConfig{DB: db, Scope: pg.PlatformScope(), Queue: queue, Fetcher: prices(exit), Logger: logger, Meter: tel.Metrics.Meter("axiom/catalog")})
}

// registerBillingJobs adds billing's jobs, which need only the database, to the queue before it starts.
func registerBillingJobs(db *pg.DB, queue *pg.Queue, tel *telemetry.Telemetry, logger *slog.Logger) error {
	// The ledger checks itself once a day, on whichever node leads the queue (the check needs only the database, so it
	// is registered here and not with the call path, which starts after the queue).
	if err := billing.RegisterReconcile(billing.ReconcileConfig{
		DB: db, Scope: pg.PlatformScope(), Queue: queue, Logger: logger, Meter: tel.Metrics.Meter("axiom/billing"),
	}); err != nil {
		return err
	}
	// Credit past its expiry is written off every minute by whichever node leads the queue (docs/domains/billing.md「额度批次」).
	if err := billing.RegisterExpire(billing.ExpireConfig{
		DB: db, Scope: pg.PlatformScope(), Queue: queue, Logger: logger, Meter: tel.Metrics.Meter("axiom/billing"),
	}); err != nil {
		return err
	}
	// A subscription's windows pass, its term ends and what is left of its liability is written off every minute, by
	// whichever node leads the queue (docs/domains/billing.md「订阅套餐」).
	if err := billing.RegisterSubscriptions(billing.SubscriptionConfig{
		DB: db, Scope: pg.PlatformScope(), Queue: queue, Logger: logger, Meter: tel.Metrics.Meter("axiom/billing"),
	}); err != nil {
		return err
	}
	// Estimated charges are corrected from the suppliers' records by whichever node leads the queue
	// (docs/domains/billing.md「用量来源与暂估」).
	return billing.RegisterCorrections(billing.CorrectConfig{
		DB: db, Scope: pg.PlatformScope(), Queue: queue, Logger: logger, Meter: tel.Metrics.Meter("axiom/billing"),
	})
}

// stopQueue stops the queue within timeout, on a context that outlives the one that ended the node.
func stopQueue(ctx context.Context, queue *pg.Queue, timeout time.Duration, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := queue.Stop(ctx); err != nil {
		logger.Error("stopping the job queue", "err", err)
	}
}

// openCoreNode opens the providers and the database a serving core needs, shutting the providers down if the database
// refuses the start so a failed start does not leave them running.
func openCoreNode(ctx context.Context, cfg nodeSettings, master *secret.Keyring,
	logger *slog.Logger,
) (*telemetry.Telemetry, *pg.DB, *secret.Keyring, error) {
	tel, err := telemetry.New()
	if err != nil {
		return nil, nil, nil, err
	}
	db, ring, err := openCheckedDatabase(ctx, cfg, tel, master, logger)
	if err != nil {
		return nil, nil, nil, errors.Join(err, tel.Shutdown(context.WithoutCancel(ctx)))
	}
	return tel, db, ring, nil
}

// openCheckedDatabase opens the node's database and checks that the key ring it needs is complete and agrees with the
// database, both under one AXIOM_STARTUP_TIMEOUT. The key ring check reads every envelope store, so a lock held on one of
// them is the database not answering, like a lock held on the fallback prices. master is the ring loadConsole read
// before the database, so a file the node cannot use is named as such instead of as a connection failure, and the
// login key came from the same read; CheckRing checks this ring under the lock instead of reading the file again.
func openCheckedDatabase(ctx context.Context, cfg nodeSettings, tel *telemetry.Telemetry,
	master *secret.Keyring, logger *slog.Logger,
) (*pg.DB, *secret.Keyring, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, cfg.startupTimeout, errNoAnswer)
	defer cancel()
	db, err := openNodeDatabase(ctx, cfg.databaseURL, cfg.startupTimeout, tel,
		pg.WithCompletionReserve(cfg.completionReserve), pg.WithNodes(cfg.valkey.Nodes))
	if err != nil {
		return nil, nil, err
	}
	logConnectionBudget(logger, db)
	ring, _, err := keyRotation(db, cfg.masterKeyFile).CheckRing(ctx, master)
	if err == nil {
		return db, ring, nil
	}
	db.Close()
	if errors.Is(context.Cause(ctx), errNoAnswer) {
		return nil, nil, errNoAnswer
	}
	return nil, nil, err
}

// logConnectionBudget reports the numbers the startup check derived from the database's limits, so a deployment can
// see what it will use before the listeners serve.
func logConnectionBudget(logger *slog.Logger, db *pg.DB) {
	if budget, ok := db.Budget(); ok {
		logger.Info("database connection budget", "max_connections", budget.MaxConnections, "reserved", budget.Reserved,
			"nodes", budget.Nodes, "share", budget.Share, "pool", budget.Pool, "completion", budget.Completion)
	}
}

// openNodeDatabase opens the database for serving, traced, and refuses it when its fallback prices fail billing's check
// (docs/domains/billing.md「价目表缺项」). One deadline covers the connection and the check, so a database that stops
// answering, or a lock held on the fallbacks, fails the start instead of holding it. The other commands open the
// database without the check: save-fallbacks is how a refused row is repaired.
func openNodeDatabase(ctx context.Context, url string, timeout time.Duration, tel *telemetry.Telemetry, opts ...pg.Option) (*pg.DB, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errNoAnswer)
	defer cancel()
	db, err := openDatabase(ctx, url, timeout, append(opts, pg.WithTracerProvider(tel.Traces))...)
	if err != nil {
		return nil, err
	}
	err = db.ReadTx(ctx, pg.PlatformScope(), func(tx pg.Tx) error { return billing.CheckFallbacks(ctx, tx) })
	if err != nil && errors.Is(context.Cause(ctx), errNoAnswer) {
		err = errNoAnswer
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// startBackground starts what serves the call path and outlives it: the call-record writer and the job queue. Both are
// closed on every way out (closeRecorder, stopQueue), before the database they use; a recorder that started before a
// failing step, including the queue, is closed here, since the caller has no defer of its own yet.
func startBackground(ctx context.Context, db, retentionDB *pg.DB, keys *secret.Keyring, master *secret.MasterKeys, smtp mail.Config,
	cache *kv.Client, exit *egress.Client, prices func(*egress.Client) *catalog.Fetcher, exchangeSource string,
	archive observability.ArchiveStore, replica *operations.Replica, tel *telemetry.Telemetry,
	logger *slog.Logger, nodeID string, timeout time.Duration, callLogDir string,
	record func(context.Context, *pg.DB, *telemetry.Telemetry, string, *slog.Logger) (*observability.Recorder, error),
) (*pg.Queue, *observability.Recorder, *accounts.PasswordRecovery, *accounts.Registration, *alert.Bus, *payments.SignalState, error) {
	recorder, err := record(ctx, db, tel, callLogDir, logger)
	if err != nil {
		if recorder != nil {
			closeRecorder(ctx, recorder, timeout, logger)
		}
		return nil, nil, nil, nil, nil, nil, err
	}
	signals := alert.New(ctx, db, pg.PlatformScope(), logger)
	paymentSignals := payments.NewSignalState()
	queue, recovery, registration, err := startSync(ctx, db, retentionDB, smtp, cache, master, keys, exit, prices, exchangeSource, archive, replica, tel, logger, signals, paymentSignals, nodeID)
	if err != nil {
		closeRecorder(ctx, recorder, timeout, logger)
		drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		if closeErr := signals.Close(drain); closeErr != nil {
			logger.Error("closing alert signals", "err", closeErr)
		}
		cancel()
		return nil, nil, nil, nil, nil, nil, err
	}
	return queue, recorder, recovery, registration, signals, paymentSignals, nil
}

// startRecorder starts the call-record writer, reporting to the node's metrics. It completes a pending record from
// billing's ledger when the call's settlement committed after all.
func startRecorder(ctx context.Context, db *pg.DB, tel *telemetry.Telemetry, dir string, logger *slog.Logger) (*observability.Recorder, error) {
	cfg := callRecords
	cfg.Dir = dir
	cfg.Meter = tel.Metrics.Meter("axiom/observability")
	cfg.Ledger = gateway.LedgerAmounts(billing.NewChargeReader(logger, tel.Metrics.Meter("axiom/billing")))
	return observability.NewRecorder(ctx, db, pg.PlatformScope(), cfg, logger)
}

// closeRecorder drains the recorder within timeout, on a context that outlives the one that ended the node.
func closeRecorder(ctx context.Context, recorder *observability.Recorder, timeout time.Duration, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := recorder.Close(ctx); err != nil {
		logger.Error("closing the call-record writer", "err", err)
	}
}

// openCache connects the node's shared cache and checks it within the startup timeout: a server whose user, memory
// policy or clock is wrong stops the start, and one that does not answer does not
// (internal/platform/kv/README.md「配置与启动」). Without a URL it is the single-core client, and every use stays in this
// process.
func openCache(ctx context.Context, cfg kv.Config, timeout time.Duration, db *pg.DB, tel *telemetry.Telemetry,
	logger *slog.Logger,
) (*kv.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cache, err := kv.Open(ctx, cfg, kv.Deps{
		DB: db, Scope: pg.PlatformScope(), Meter: tel.Metrics.Meter("axiom/kv"), Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("shared cache:\n%w", err)
	}
	return cache, nil
}

// openRetentionDB opens the retention job's own login. Without the URL the job does not run; a configured archive bucket
// without it is refused at startup, because "the archive is on" must not mean a job that never starts.
func openRetentionDB(ctx context.Context, url string, archive bool) (*pg.DB, error) {
	if url == "" {
		if archive {
			return nil, fmt.Errorf("%s is set but %s is not: the archive needs the retention job's own login", "AXIOM_OBJECTSTORE_ENDPOINT", retentionDatabaseURLVar)
		}
		return nil, nil //nolint:nilnil // no URL is a valid state: retention is off
	}
	db, err := pg.Open(ctx, url, pg.WithCompletionReserve(1))
	if err != nil {
		return nil, fmt.Errorf("retention database:\n%w", err)
	}
	if err := observability.CheckRetentionRole(ctx, db, pg.PlatformScope()); err != nil {
		db.Close()
		return nil, fmt.Errorf("retention database:\n%w", err)
	}
	return db, nil
}

// registerRetention adds the job when the node has its login, and says so once when it does not.
func registerRetention(db *pg.DB, archive observability.ArchiveStore, queue *pg.Queue, tel *telemetry.Telemetry, logger *slog.Logger) error {
	if db == nil {
		logger.Info("call-log retention is off: " + retentionDatabaseURLVar + " is not set")
		return nil
	}
	return observability.RegisterRetention(observability.RetentionConfig{
		DB: db, Scope: pg.PlatformScope(), Queue: queue, Store: archive, Logger: logger,
		Meter: tel.Metrics.Meter("axiom/observability"),
	})
}

// openBucket opens a configured bucket, or none: a bucket that is not configured turns off what writes to it (the call
// log's archive leaves every partition in PostgreSQL; the audit chain is not copied off-site).
func openBucket(cfg objectstore.Config) (*objectstore.Client, error) {
	client, err := objectstore.Open(cfg)
	if errors.Is(err, objectstore.ErrNotConfigured) {
		return nil, nil //nolint:nilnil // no bucket is a valid state: what writes to it is off
	}
	return client, err
}

// closeCache ends the cache's loops and deletes the node's heartbeat within timeout, on a context that outlives the one
// that ended the node. A second call finds nothing to do, so the shutdown and the defer below may both make it.
func closeCache(ctx context.Context, cache *kv.Client, timeout time.Duration, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := cache.Close(ctx); err != nil {
		// The row goes stale by itself, and a node that has emptied itself is not a failed shutdown for it.
		logger.Error("closing the shared cache", "err", err)
	}
}

var errNoAnswer = errors.New("database: " + pg.DatabaseURLVar + ": no answer within AXIOM_STARTUP_TIMEOUT")

// openDatabase connects to the database and checks it within timeout: a server that accepts connections and never
// answers would otherwise hold the node before its listeners serve, where no probe or log reports it.
func openDatabase(ctx context.Context, url string, timeout time.Duration, opts ...pg.Option) (*pg.DB, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errNoAnswer)
	defer cancel()
	db, err := pg.Open(ctx, url, opts...)
	if err != nil {
		if errors.Is(context.Cause(ctx), errNoAnswer) {
			return nil, errNoAnswer // the budget read is part of opening, and pgx reports only that the context ended
		}
		return nil, err
	}
	if err := db.Check(ctx, modules()...); err != nil {
		db.Close()
		if errors.Is(context.Cause(ctx), errNoAnswer) {
			return nil, errNoAnswer // pgx reports only that the context ended
		}
		return nil, fmt.Errorf("database: %w", err)
	}
	if err := suppliers.CheckAdapters(ctx, db, pg.PlatformScope()); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func registerExchangeSource(ctx context.Context, db *pg.DB, queue *pg.Queue, exit *egress.Client, logger *slog.Logger, source string) error {
	if source != "ecb" {
		return nil
	}
	return billing.RegisterExchangeSync(ctx, billing.ExchangeSyncConfig{DB: db, Scope: pg.PlatformScope(), Queue: queue, Client: exit, Logger: logger})
}
