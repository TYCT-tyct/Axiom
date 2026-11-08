package main

import (
	"fmt"
	"log/slog"

	"axiom/internal/operations"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
)

// registerAuditReplica copies the audit chain to replica, or says it is not copied: without a bucket the chain is only
// in this database, which the daily digest reports as unconfigured.
func registerAuditReplica(db *pg.DB, queue *pg.Queue, replica operations.ReplicaStore, logger *slog.Logger) error {
	if replica == nil {
		logger.Info("the audit chain is not copied off-site: " + auditReplicaPrefix + "_ENDPOINT is not set")
		return nil
	}
	return operations.RegisterAuditReplica(db, pg.PlatformScope(), queue, replica, logger)
}

// openReplica opens the audit chain's replica bucket, or none (a nil store); close releases what it opened.
func openReplica(cfg objectstore.Config) (store operations.ReplicaStore, closeIt func(), err error) {
	client, err := openBucket(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("audit replica:\n%w", err)
	}
	if client == nil {
		return nil, func() {}, nil
	}
	return client, client.Close, nil
}
