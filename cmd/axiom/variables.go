package main

// The AXIOM_* variables this program reads. One name each: the code that reads a variable, the code that reports a bad
// one, and the listener log must not disagree about its spelling. The connection strings' names are in pg, which also
// reports on them.
const (
	publicAddrVar            = "AXIOM_PUBLIC_ADDR"
	adminAddrVar             = "AXIOM_ADMIN_ADDR"
	probesAddrVar            = "AXIOM_PROBES_ADDR"
	startupTimeoutVar        = "AXIOM_STARTUP_TIMEOUT"
	shutdownTimeoutVar       = "AXIOM_SHUTDOWN_TIMEOUT"
	masterKeyFileVar         = "AXIOM_MASTER_KEY_FILE"
	keyHashFileVar           = "AXIOM_KEY_HASH_FILE"
	hashConcurrencyVar       = "AXIOM_HASH_CONCURRENCY"
	hashWaitVar              = "AXIOM_HASH_WAIT"
	trustedProxiesVar        = "AXIOM_TRUSTED_PROXIES"
	migrateConnectTimeoutVar = "AXIOM_MIGRATE_CONNECT_TIMEOUT"
	migrateTimeoutVar        = "AXIOM_MIGRATE_TIMEOUT"
)
