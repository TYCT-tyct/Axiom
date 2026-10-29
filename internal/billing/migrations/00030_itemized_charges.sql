-- +goose Up
-- NULL is an old writer whose tier declaration and execution decision cannot be reconstructed.
ALTER TABLE billing.reservations ADD COLUMN pricing_tiers smallint;
-- Every existing value is NULL; enforce the four-tier mask on writes without scanning historical reservations.
ALTER TABLE billing.reservations ADD CONSTRAINT reservations_pricing_tiers_check
    CHECK (pricing_tiers BETWEEN -1 AND 15) NOT VALID;
GRANT UPDATE (pricing_tiers) ON billing.reservations TO axiom_app;
