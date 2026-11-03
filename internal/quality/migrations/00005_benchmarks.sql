-- +goose Up

-- The product score's shared record (docs/domains/quality.md「产品成绩（固定测评题）」). A baseline is the official
-- direct endpoint's run for one catalog model in one benchmark version: the endpoint that ran it, its time, the call
-- format, and the fraction each answered question passed. The per-question fractions, not the replies, are what the
-- scorer needs, and they keep a row small. A score is one endpoint's run against the baseline it names. Both tables
-- carry the set's version and hash; a node reads only its own pair, so a newer set's rows never disturb an older node
-- and an older set's rows can never be mistaken for the current one. Old nodes ignore both tables.
CREATE TABLE quality.benchmark_baselines (
    model_id uuid NOT NULL,
    version text NOT NULL CHECK (version ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    hash text NOT NULL CHECK (hash ~ '^[0-9a-f]{64}$'),
    format smallint NOT NULL CHECK (format BETWEEN 0 AND 5),
    endpoint_id uuid NOT NULL,
    run_at bigint NOT NULL CHECK (run_at > 0),
    scores jsonb NOT NULL CHECK (jsonb_typeof(scores) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (model_id, version)
);
CREATE TABLE quality.benchmark_scores (
    endpoint_id uuid NOT NULL,
    version text NOT NULL CHECK (version ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    hash text NOT NULL CHECK (hash ~ '^[0-9a-f]{64}$'),
    model_id uuid NOT NULL,
    upstream text NOT NULL,
    formats integer NOT NULL CHECK (formats BETWEEN 0 AND 63),
    score double precision NOT NULL CHECK (score BETWEEN 0 AND 100),
    confidence double precision NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    run_at bigint NOT NULL CHECK (run_at > 0),
    baseline_at bigint NOT NULL CHECK (baseline_at > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (endpoint_id, version)
);
ALTER TABLE quality.benchmark_baselines ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
ALTER TABLE quality.benchmark_scores ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY platform ON quality.benchmark_baselines USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY platform ON quality.benchmark_scores USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT, UPDATE ON quality.benchmark_baselines TO axiom_app;
GRANT SELECT, INSERT, UPDATE ON quality.benchmark_scores TO axiom_app;

-- +goose Down
DROP TABLE quality.benchmark_scores;
DROP TABLE quality.benchmark_baselines;
