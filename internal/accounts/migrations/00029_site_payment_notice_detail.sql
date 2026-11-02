-- +goose Up
-- The recipient's site_id stays the owner's main site (the tenant that owns the notice); detail names the secondary
-- site whose payment configuration changed.
ALTER TABLE accounts.user_notices ADD COLUMN detail text NOT NULL DEFAULT '';
