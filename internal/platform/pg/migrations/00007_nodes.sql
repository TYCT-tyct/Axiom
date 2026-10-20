-- +goose Up

-- The core nodes that are alive, one row each, written by the process itself: the id is the node's stable identity
-- (AXIOM_NODE_ID, the host name unless set), so a restarted node replaces its own row and is not counted twice, and
-- seen_at is the last heartbeat. The number of core nodes is
-- configuration (AXIOM_CORE_NODES), and nothing else would notice it set too low, or a second node that reached the
-- same database without Valkey (internal/platform/kv/README.md「配置与启动」). Rows are deleted when stale, so the
-- table holds about as many rows as there are nodes. It is platform's, holds no tenant's rows, and only the platform
-- scope reads or writes it; no index: every query scans a handful of rows.
CREATE TABLE platform.nodes (
    id text PRIMARY KEY,
    seen_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE platform.nodes ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON platform.nodes
    USING (platform.in_platform_scope())
    WITH CHECK (platform.in_platform_scope());

GRANT SELECT, INSERT, UPDATE, DELETE ON platform.nodes TO axiom_app;
