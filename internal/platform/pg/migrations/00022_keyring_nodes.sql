-- +goose Up

-- Every heartbeat also reports the master-key versions this node has loaded and the one it writes, so activation and
-- retirement can wait for every live node instead of stopping every writer. The columns hold version identifiers only;
-- key material stays in protected files (internal/platform/README.md「凭证加密」).
ALTER TABLE platform.nodes
    ADD COLUMN IF NOT EXISTS key_versions bigint[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS key_active bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS key_active_since timestamptz NOT NULL DEFAULT now();
