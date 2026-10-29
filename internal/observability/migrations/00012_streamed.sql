-- +goose Up
-- Whether the client asked for a stream, and the upstream's physical prompt total (-1 when it reported none), which
-- the token counts alone cannot recover: cache reads and writes may overlap the prompt they belong to. The generation
-- endpoint shows both (docs/domains/gateway.md「用量与费用」).
ALTER TABLE observability.call_log
    ADD COLUMN streamed boolean NOT NULL DEFAULT false,
    ADD COLUMN physical_input bigint NOT NULL DEFAULT -1 CHECK (physical_input >= -1);
