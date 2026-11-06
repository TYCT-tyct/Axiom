-- +goose Up

-- The envelope the agent changes a site's sale prices within (docs/domains/operations.md「售价的累计变动上限」). Prices
-- are capped by how far and how often they move, never by an amount of money. One row per site; no row is no change:
-- a site the owner set no envelope for refuses the agent's price writes. Ratios are integer basis points.
CREATE TABLE operations.price_envelopes (
    site_id uuid PRIMARY KEY,
    -- The least share (price − cost) ÷ price any price published within the envelope keeps of its cost.
    min_margin_bps integer NOT NULL CHECK (min_margin_bps BETWEEN 0 AND 10000),
    -- How far one model's prices may move in a UTC day, up and down added together. A move beyond the largest limit
    -- counts as one more than it, so the bound keeps such a model outside every envelope for the rest of the day.
    model_daily_change_bps bigint NOT NULL CHECK (model_daily_change_bps BETWEEN 0 AND 100000000),
    -- How many publications, and how many models, a UTC day may change.
    daily_changes integer NOT NULL CHECK (daily_changes >= 0),
    daily_models integer NOT NULL CHECK (daily_models >= 0),
    rev bigint NOT NULL CHECK (rev >= 1),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- What the agent's publications moved in a UTC day: a model row (model_id set) adds up its moves and counts them; the
-- site row (model_id NULL) counts every publication and the models moved. Rows start at zero and only grow: moving a
-- price back is another move.
CREATE TABLE operations.price_counters (
    site_id uuid NOT NULL,
    day_start timestamptz NOT NULL,
    model_id uuid,
    change_bps bigint NOT NULL DEFAULT 0 CHECK (change_bps >= 0),
    count integer NOT NULL DEFAULT 0 CHECK (count >= 0),
    models integer NOT NULL DEFAULT 0 CHECK (models >= 0),
    CHECK (model_id IS NULL OR models = 0),
    CHECK (model_id IS NOT NULL OR change_bps = 0),
    UNIQUE NULLS NOT DISTINCT (site_id, day_start, model_id)
);

ALTER TABLE operations.operations DROP CONSTRAINT operations_risk_check,
    ADD CONSTRAINT operations_risk_check CHECK (risk IN ('routine', 'delisting', 'top_up', 'price'));

-- Platform data, like the operations and the caps.
ALTER TABLE operations.price_envelopes ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.price_envelopes FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.price_envelopes FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.price_envelopes FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
ALTER TABLE operations.price_counters ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON operations.price_counters FOR SELECT USING ((SELECT platform.in_platform_scope()));
CREATE POLICY inserts ON operations.price_counters FOR INSERT WITH CHECK ((SELECT platform.in_platform_scope()));
CREATE POLICY updates ON operations.price_counters FOR UPDATE USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));
GRANT SELECT, INSERT ON operations.price_envelopes, operations.price_counters TO axiom_app;
GRANT UPDATE (min_margin_bps, model_daily_change_bps, daily_changes, daily_models, rev, updated_at)
    ON operations.price_envelopes TO axiom_app;
GRANT UPDATE (change_bps, count, models) ON operations.price_counters TO axiom_app;

-- +goose Down
ALTER TABLE operations.operations DROP CONSTRAINT operations_risk_check,
    ADD CONSTRAINT operations_risk_check CHECK (risk IN ('routine', 'delisting', 'top_up'));
DROP TABLE operations.price_counters;
DROP TABLE operations.price_envelopes;
