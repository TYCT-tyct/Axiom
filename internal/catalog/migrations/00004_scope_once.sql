-- +goose NO TRANSACTION
-- +goose Up

-- Keep command, role, terminal-state and write-check boundaries; only scope reads move into InitPlans.

ALTER POLICY public_read ON catalog.author
    USING ((SELECT platform.in_any_scope()));

ALTER POLICY platform_insert ON catalog.author
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_update ON catalog.author
    USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY public_read ON catalog.model
    USING (
        (SELECT platform.in_any_scope()) AND (status <> 'draft'
        OR (SELECT platform.in_platform_scope()))
    );

ALTER POLICY platform_insert ON catalog.model
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_update ON catalog.model
    USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY public_read ON catalog.name
    USING (
        (SELECT platform.in_any_scope()) AND ((SELECT platform.in_platform_scope())
        OR EXISTS (SELECT FROM catalog.model m WHERE m.id = model_id AND m.status = 'listed'))
    );

ALTER POLICY platform_insert ON catalog.name
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY public_read ON catalog.reference_price
    USING (
        (SELECT platform.in_any_scope()) AND ((SELECT platform.in_platform_scope())
        OR EXISTS (SELECT FROM catalog.model m WHERE m.id = model_id AND m.status = 'listed'))
    );

ALTER POLICY platform_insert ON catalog.reference_price
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY public_read ON catalog.revision
    USING ((SELECT platform.in_any_scope()));

ALTER POLICY platform_update ON catalog.revision
    USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_read ON catalog.change
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_insert ON catalog.change
    WITH CHECK ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_read ON catalog.sync_state
    USING ((SELECT platform.in_platform_scope()));

ALTER POLICY platform_update ON catalog.sync_state
    USING ((SELECT platform.in_platform_scope()))
    WITH CHECK ((SELECT platform.in_platform_scope()));

