package main

import (
	"fmt"
	"log/slog"

	"axiom/internal/operations"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
)

// registerAuditReplica copies the audit chain to replica, or says it is not copied: without a bucket the chain is only
// in this database.
func registerAuditReplica(db *pg.DB, queue *pg.Queue, replica *operations.Replica, logger *slog.Logger) error {
	if replica == nil {
		logger.Info("the audit chain is not copied off-site: " + auditReplicaPrefix + "_ENDPOINT is not set")
		return nil
	}
	return operations.RegisterAuditReplica(db, pg.PlatformScope(), queue, *replica, logger)
}

// openReplica opens the audit chain's replica bucket, or none (nil); close releases what it opened. The target names
// the bucket by its endpoint and name, which is what the copy's progress is kept by.
func openReplica(cfg objectstore.Config) (replica *operations.Replica, closeIt func(), err error) {
	client, err := openBucket(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("audit replica:\n%w", err)
	}
	if client == nil {
		return nil, func() {}, nil
	}
	return &operations.Replica{Store: client, Target: cfg.Endpoint + "/" + cfg.Bucket}, client.Close, nil
}
