-- +goose Up

-- Versions order a normalized pair independently of transaction timestamps.
CREATE TABLE billing.exchange_rates (
    id uuid PRIMARY KEY,
    base text NOT NULL CHECK (base IN ('USD','CNY','EUR','GBP','JPY','HKD','SGD','AUD','CAD','CHF')),
    quote text NOT NULL CHECK (quote IN ('USD','CNY','EUR','GBP','JPY','HKD','SGD','AUD','CAD','CHF')),
    version bigint NOT NULL CHECK (version > 0),
    numerator bigint NOT NULL CHECK (numerator BETWEEN 1 AND 1000000000000000000),
    denominator bigint NOT NULL CHECK (denominator BETWEEN 1 AND 1000000000000000000),
    source text NOT NULL DEFAULT 'manual' CHECK (source = 'manual'),
    kind text NOT NULL CHECK (kind IN ('initial','adjustment','correction')),
    reason text NOT NULL CHECK (char_length(btrim(reason)) BETWEEN 1 AND 1000),
    created_by text NOT NULL CHECK (created_by <> ''),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (base < quote),
    CHECK (numerator::numeric * 1000000 >= denominator AND numerator::numeric <= denominator::numeric * 1000000),
    UNIQUE (base, quote, version)
);
ALTER TABLE billing.exchange_rates ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON billing.exchange_rates FOR SELECT USING (platform.in_platform_scope());
CREATE POLICY inserts ON billing.exchange_rates FOR INSERT WITH CHECK (platform.in_platform_scope());
GRANT SELECT, INSERT ON billing.exchange_rates TO axiom_app;

-- NULL distinguishes pre-migration or old-node reservations from a known empty set.
ALTER TABLE billing.reservations ADD COLUMN exchange_rates jsonb,
    ADD COLUMN profit bigint;
GRANT UPDATE (profit) ON billing.reservations TO axiom_app;
