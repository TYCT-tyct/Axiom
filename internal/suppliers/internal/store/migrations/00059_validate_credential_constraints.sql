-- +goose Up

-- The constraints 00056 and 00057 added NOT VALID, checked once 00058 has filled in the rows they hold; VALIDATE takes
-- only SHARE UPDATE EXCLUSIVE, so reads and writes go on (internal/platform/pg/README.md「在线迁移」).
ALTER TABLE suppliers.credentials VALIDATE CONSTRAINT credentials_balance_observed_check;
ALTER TABLE suppliers.credentials VALIDATE CONSTRAINT credentials_egress_id_fkey;
