-- +goose Up

-- What no code reads or writes any more (docs/domains/billing.md「免单规则」「用量来源与暂估」): the correction window and
-- the daily cap on waivers were removed from the design, and with them the states that held a charge open for a
-- correction. Neither the previous release nor this one touches them, so a rolling deploy never meets a missing table
-- or column.
--
-- billing.corrections and billing.waivers had no writer; their policies and grants go with them. The partial index on
-- the estimated state and the columns unpriced and correct_by (and their column grants) likewise. The states the
-- reservation can be in are the five README.md「预留的状态」 lists; a row left in 'estimated' or 'cleanup_pending' by an
-- older release fails the new constraint, and must be settled or released first. fallbacks.markup stays: the fallback
-- body's parser still takes it.
DROP TABLE billing.corrections, billing.waivers;
DROP INDEX billing.estimated_by_correct_by;
ALTER TABLE billing.reservations
    DROP CONSTRAINT reservations_state_check,
    ADD CONSTRAINT reservations_state_check CHECK (state IN ('pending', 'released', 'settled', 'cleanup_settled',
        'cleanup_released')),
    DROP COLUMN unpriced,
    DROP COLUMN correct_by;
