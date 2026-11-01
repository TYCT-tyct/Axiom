-- +goose Up

-- The precision an endpoint serves a model at, named as OpenRouter names the levels (docs/domains/gateway.md
-- 「与 OpenRouter 的偏离」). An admin records it; the supplier sync never does, because neither reference system reports
-- it. NULL is "not recorded", which is not the level unknown: a request that names levels does not match it.
ALTER TABLE suppliers.endpoints ADD COLUMN quantization text
    CHECK (quantization IN ('int4', 'int8', 'fp4', 'mxfp4', 'nvfp4', 'fp6', 'fp8', 'mxfp8', 'fp16', 'bf16', 'fp32',
                            'unknown'));
GRANT UPDATE (quantization) ON suppliers.endpoints TO axiom_app;

-- +goose Down

-- The levels are configuration, not data another table depends on: dropping the column loses them, and an endpoint
-- stays routable with no level recorded.
ALTER TABLE suppliers.endpoints DROP COLUMN quantization;
