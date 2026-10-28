-- +goose Up

-- Limits on the call keys of one account (README.md「API key」): how many it may hold that are not deleted, and how many
-- it may make in an hour. A site sets its own in site_settings and an account may carry its own over the site's; a
-- column that is null is not set and the next level applies, and the default is in code only. The bounds keep a typo
-- from refusing every key, and the largest is no practical limit.
ALTER TABLE accounts.site_settings
    ADD COLUMN max_keys_per_account integer CHECK (max_keys_per_account BETWEEN 1 AND 10000),
    ADD COLUMN key_creations_per_hour integer CHECK (key_creations_per_hour BETWEEN 1 AND 10000);
ALTER TABLE accounts.accounts
    ADD COLUMN max_keys integer CHECK (max_keys BETWEEN 1 AND 10000),
    ADD COLUMN key_creations_per_hour integer CHECK (key_creations_per_hour BETWEEN 1 AND 10000);

GRANT UPDATE (max_keys_per_account, key_creations_per_hour) ON accounts.site_settings TO axiom_app;
GRANT UPDATE (max_keys, key_creations_per_hour) ON accounts.accounts TO axiom_app;

-- When a key was made, stamped by the database so that the hour's limit is judged on one clock whichever node makes
-- the key. Keys already made count as made now. The index serves the account's keys of the last hour, deleted ones too:
-- deleting a key does not give its turn back.
ALTER TABLE accounts.api_keys ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX api_keys_made_by_account ON accounts.api_keys (account_id, created_at) WHERE kind = 'call';
