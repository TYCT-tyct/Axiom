-- +goose Up

-- A site's own limits on the keys of one account (README.md「API key」): how many call keys an account may hold that
-- are not deleted, and how many it may make in an hour. No value is the default in code, and there is no second way to
-- spell it; the bounds keep a typo from refusing every key of the site, and the largest is no practical limit.
ALTER TABLE accounts.site_settings
    ADD COLUMN max_keys_per_account integer CHECK (max_keys_per_account BETWEEN 1 AND 10000),
    ADD COLUMN key_creations_per_hour integer CHECK (key_creations_per_hour BETWEEN 1 AND 10000);

GRANT UPDATE (max_keys_per_account, key_creations_per_hour) ON accounts.site_settings TO axiom_app;
