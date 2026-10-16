-- name: InsertTenant :exec
INSERT INTO platform.tenants (site_id, tenant_id) VALUES ((@site_id::text)::uuid, (@tenant_id::text)::uuid);
