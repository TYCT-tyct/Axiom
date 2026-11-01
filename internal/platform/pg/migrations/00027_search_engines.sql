-- +goose Up

-- The web plugin's search backends: any number of them, each with its own sealed key and its published price. A row
-- is one engine; the partial index keeps at most one default. Prices and costs are decimal text because a unit price
-- is exact (docs/domains/billing.md「钱的表示」), and price_version records the publication a call locks into its
-- reservation. The old single-platform.search_config row stays readable as the fallback for a deployment that has not
-- saved an engine list yet.
CREATE TABLE IF NOT EXISTS platform.search_engines (
 id uuid PRIMARY KEY,
 name text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,31}$' AND name <> 'native'),
 kind text NOT NULL CHECK (kind IN ('searxng', 'firecrawl')),
 url text NOT NULL CHECK (char_length(url) BETWEEN 1 AND 2048),
 enabled boolean NOT NULL,
 is_default boolean NOT NULL,
 cost_search text NOT NULL,
 cost_result text NOT NULL,
 markup text NOT NULL,
 price_search text NOT NULL,
 price_result text NOT NULL,
 price_version bigint NOT NULL CHECK (price_version > 0),
 ciphertext bytea NOT NULL,
 nonce bytea NOT NULL CHECK (octet_length(nonce) = 12),
 wrapped_dek bytea NOT NULL,
 key_version bigint NOT NULL CHECK (key_version BETWEEN 1 AND 4294967295),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK (NOT is_default OR enabled)
);
CREATE UNIQUE INDEX IF NOT EXISTS search_engines_one_default ON platform.search_engines (is_default) WHERE is_default;
ALTER TABLE platform.search_engines ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS platform_only ON platform.search_engines;
CREATE POLICY platform_only ON platform.search_engines USING ((SELECT platform.in_platform_scope()));
GRANT SELECT,INSERT,UPDATE,DELETE ON platform.search_engines TO axiom_app;

CREATE OR REPLACE TRIGGER fence_envelope_key_version BEFORE INSERT OR UPDATE OF key_version ON platform.search_engines
FOR EACH ROW EXECUTE FUNCTION platform.fence_envelope_key_version('key_version');
