-- +goose Up

-- Partitions do not inherit policies: include every existing month and the default partition, as well as each root.
-- +goose StatementBegin
DO $$
DECLARE
    target regclass;
BEGIN
    FOR target IN
        SELECT relid FROM pg_partition_tree('observability.call_log')
        UNION ALL SELECT relid FROM pg_partition_tree('observability.call_attempt')
    LOOP
        EXECUTE format('ALTER POLICY tenant ON %s USING (
            (SELECT platform.in_platform_scope())
            OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
            OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]))', target);
    END LOOP;
END
$$;
-- +goose StatementEnd

ALTER POLICY platform_only ON observability.alert_configuration
    USING ((SELECT platform.in_platform_scope()));
ALTER POLICY platform_only ON observability.alert_events
    USING ((SELECT platform.in_platform_scope()));
ALTER POLICY platform_only ON observability.alert_receipts
    USING ((SELECT platform.in_platform_scope()));
ALTER POLICY platform_only ON observability.alert_windows
    USING ((SELECT platform.in_platform_scope()));
