-- +goose Up

-- Only a call key is routed, so only a call key names a routing preset (docs/domains/routing.md「滚动发布、失败与性能」).
-- NOT VALID holds the table's lock only for the catalog change; 00033 checks the rows already there.
ALTER TABLE accounts.api_keys ADD CONSTRAINT api_keys_preset_call CHECK (kind = 'call' OR preset_id IS NULL) NOT VALID;

-- +goose Down
ALTER TABLE accounts.api_keys DROP CONSTRAINT api_keys_preset_call;
