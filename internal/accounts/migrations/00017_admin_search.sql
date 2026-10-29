-- +goose Up
UPDATE accounts.roles SET additional_permissions = additional_permissions || ARRAY['calls.search','calls.inspect','audit.view'] WHERE name='admin' AND builtin;
UPDATE accounts.roles SET additional_permissions = array_append(additional_permissions,'calls.search') WHERE name='support' AND builtin;
