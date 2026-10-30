-- +goose Up

-- A site's brand colour, the one input the console derives both colour schemes from (docs/frontend.md「主题与自由度」).
-- Null is the default palette. The table keeps the canonical lower-case #rrggbb the API also writes, so a page may pass
-- it to brandStyle as it is; contrast is not checked here because brandStyle corrects a colour rather than refusing it.
ALTER TABLE accounts.sites ADD COLUMN brand_color text
    CHECK (brand_color IS NULL OR brand_color ~ '^#[0-9a-f]{6}$');
GRANT UPDATE (brand_color) ON accounts.sites TO axiom_app;

-- +goose Down
ALTER TABLE accounts.sites DROP COLUMN brand_color;
