-- +goose Up

-- Approving what the operations agent proposed is the owner's (docs/domains/operations.md「风险分级与审批」), as the
-- control plane is: the built-in admin role holds it, and granting that role is a platform grant made with two-step
-- verification. No operations credential may hold it: agent_credentials' check has refused it since 00034.
UPDATE accounts.roles SET additional_permissions = array_append(additional_permissions, 'operations.approve')
WHERE name = 'admin' AND builtin AND NOT 'operations.approve' = ANY (additional_permissions);

-- +goose Down
UPDATE accounts.roles SET additional_permissions = array_remove(additional_permissions, 'operations.approve') WHERE builtin;
