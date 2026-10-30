-- +goose Up

-- A site's logo, served from the console's own address so a page never reaches a third party for it (docs/frontend.md
-- 「主题与自由度」). The bytes live beside the site rather than in object storage: a logo is at most 256 KiB and only
-- an operator uploads one, so the database keeps it in the transaction that audits the change and a node needs no
-- second storage system. Rows are kept by (site, hash): a content-hash URL must never change meaning, so replacing a
-- logo adds a row and moves sites.logo_hash; a page opened before the change still loads the old image.
CREATE TABLE accounts.site_logos (
    site_id uuid NOT NULL REFERENCES accounts.sites,
    hash bytea NOT NULL CHECK (octet_length(hash) = 32),
    content_type text NOT NULL CHECK (content_type IN ('image/svg+xml', 'image/png', 'image/webp')),
    -- An SVG may have no intrinsic pixel size, which is 0 here. Raster logos are decoded before this check.
    width integer NOT NULL CHECK (width BETWEEN 0 AND 1024),
    height integer NOT NULL CHECK (height BETWEEN 0 AND 1024),
    bytes bytea NOT NULL CHECK (octet_length(bytes) BETWEEN 1 AND 262144),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, hash)
);

ALTER TABLE accounts.sites ADD COLUMN logo_hash bytea
    CHECK (logo_hash IS NULL OR octet_length(logo_hash) = 32);
ALTER TABLE accounts.sites ADD CONSTRAINT sites_logo
    FOREIGN KEY (id, logo_hash) REFERENCES accounts.site_logos (site_id, hash);
GRANT UPDATE (logo_hash) ON accounts.sites TO axiom_app;
GRANT SELECT, INSERT ON accounts.site_logos TO axiom_app;

ALTER TABLE accounts.site_logos ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts.site_logos FORCE ROW LEVEL SECURITY;
CREATE POLICY reads ON accounts.site_logos FOR SELECT USING (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);
CREATE POLICY inserts ON accounts.site_logos FOR INSERT WITH CHECK (
    (SELECT platform.in_platform_scope()) OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
);

-- +goose Down
ALTER TABLE accounts.sites DROP CONSTRAINT sites_logo;
ALTER TABLE accounts.sites DROP COLUMN logo_hash;
DROP TABLE accounts.site_logos;
