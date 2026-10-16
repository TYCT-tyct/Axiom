-- +goose Up

-- The admin API's idempotency keys (internal/platform/README.md「幂等」): a caller picks a UUID for one intended change,
-- and sending the request again returns the first result instead of doing the change twice. internal/platform/idem
-- claims the key in the transaction of the change itself and writes the response there too, so a key is claimed
-- exactly when its change is done, and a concurrent request with the same key waits on the claim and then replays it.
-- A key is bound to the actor, the operation and the request it was first used with, so no one replays another's
-- result and a key reused for another change is refused. Retention comes with the audit log's (待定): a replay needs
-- the row, so none is deleted before then.
CREATE TABLE platform.idempotency (
    key uuid PRIMARY KEY,
    actor text NOT NULL CHECK (actor <> ''),
    operation text NOT NULL CHECK (operation ~ '^[A-Za-z][A-Za-z0-9]*$'),
    -- SHA-256 of the request's canonical JSON.
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    -- The response, written in the claiming transaction after the change; a row without one is never visible.
    response jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE platform.idempotency ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON platform.idempotency FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY inserts ON platform.idempotency FOR INSERT WITH CHECK (platform.in_platform_scope());
CREATE POLICY updates ON platform.idempotency FOR UPDATE USING (platform.in_platform_scope() AND response IS NULL)
    WITH CHECK (platform.in_platform_scope());

GRANT SELECT, INSERT (key, actor, operation, fingerprint), UPDATE (response) ON platform.idempotency TO axiom_app;
