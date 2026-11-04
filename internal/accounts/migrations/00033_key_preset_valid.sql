-- +goose Up

-- 00032's check against the keys already there. VALIDATE takes a lock that lets reads and writes go on.
ALTER TABLE accounts.api_keys VALIDATE CONSTRAINT api_keys_preset_call;

-- +goose Down
SELECT 1;
