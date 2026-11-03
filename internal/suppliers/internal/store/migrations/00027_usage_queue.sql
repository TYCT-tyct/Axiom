-- +goose Up

-- Usage records from a supplier whose per-call list is a queue that is consumed once (CPA, docs/domains/suppliers.md
-- 「CPA 的用量」). The resident collector stores the adapter's sanitised record here before it parses or ingests it, so a
-- crash after the pop leaves a durable row to replay instead of a record only in memory. request_id is the supplier's
-- request id when it had one; the unique index makes a replay of the same pop insert once. rejected rows are kept for a
-- person: the record could not be parsed, and deleting it would lose the only copy.
CREATE TABLE suppliers.usage_records (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    supplier_id uuid NOT NULL REFERENCES suppliers.suppliers,
    request_id text CHECK (request_id IS NULL OR (request_id <> '' AND char_length(request_id) <= 200)),
    received_at timestamptz NOT NULL DEFAULT now(),
    record jsonb NOT NULL CHECK (jsonb_typeof(record) = 'object'),
    state text NOT NULL DEFAULT 'new' CHECK (state IN ('new', 'rejected')),
    reason text CHECK (reason = 'unreadable'),
    CONSTRAINT usage_records_state_reason CHECK ((state = 'new') = (reason IS NULL))
);

CREATE UNIQUE INDEX usage_records_request ON suppliers.usage_records (supplier_id, request_id)
    WHERE request_id IS NOT NULL;
CREATE INDEX usage_records_new ON suppliers.usage_records (supplier_id, id) WHERE state = 'new';

ALTER TABLE suppliers.usage_records ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_only ON suppliers.usage_records USING (platform.in_platform_scope());
GRANT SELECT, INSERT, UPDATE, DELETE ON suppliers.usage_records TO axiom_app;

-- +goose Down

DROP TABLE suppliers.usage_records;
