-- +goose Up

-- 00051 added the inviter key NOT VALID; VALIDATE takes only SHARE UPDATE EXCLUSIVE, so registrations go on while it
-- checks the rows already there (internal/platform/pg/README.md「在线迁移」).
ALTER TABLE accounts.registration_requests VALIDATE CONSTRAINT registration_requests_site_id_inviter_user_id_fkey;
