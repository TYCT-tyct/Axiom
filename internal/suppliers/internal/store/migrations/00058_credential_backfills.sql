-- +goose NO TRANSACTION
-- +goose Up

-- What 00056 and 00057 leave to fill in on the credentials stored before them, in batches of a thousand by id, each
-- committed on its own and selecting only rows still to fill, so an interrupted run picks up where it stopped
-- (internal/platform/pg/README.md「在线迁移」). Every Bedrock credential stored before was an access key, Bedrock's
-- default form, which a node needs named now that Bedrock takes an API key too; Vertex AI keeps one form, and no other
-- type stored before has more than one. An egress_id was never written or read before 00057, so one that names no
-- proxy is cleared: the credential went direct, which NULL says.
-- +goose StatementBegin
DO $$
DECLARE
    after uuid := '00000000-0000-0000-0000-000000000000';
    upto uuid;
BEGIN
    LOOP
        SELECT id INTO upto FROM (
            SELECT id FROM suppliers.credentials WHERE id > after ORDER BY id LIMIT 1000
        ) batch ORDER BY id DESC LIMIT 1;
        EXIT WHEN upto IS NULL;
        UPDATE suppliers.credentials c SET settings = '{"form": "access_key"}'
        FROM suppliers.suppliers s
        WHERE c.id > after AND c.id <= upto AND s.id = c.supplier_id AND s.kind = 'bedrock' AND c.status <> 'erased'
            AND c.settings IS NULL;
        UPDATE suppliers.credentials c SET egress_id = NULL
        WHERE c.id > after AND c.id <= upto AND c.egress_id IS NOT NULL
            AND NOT EXISTS (SELECT 1 FROM suppliers.egress_proxies p WHERE p.id = c.egress_id);
        COMMIT;
        after := upto;
    END LOOP;
END
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS credentials_egress ON suppliers.credentials (egress_id) WHERE egress_id IS NOT NULL;
