-- +goose Up

-- A finite per-minute ceiling, so a node's rolling-window count and the memory of unlimited traffic stay bounded
-- (internal/suppliers/README.md「并发与速率」, issue #382). A value above the cap is lowered to it rather than cleared:
-- clearing would remove the ceiling and leave the account with no bound at all.
--
-- This migration only lowers values that are already there. An old node still running during the rolling release may
-- write above the cap; the new code reads such a value as the cap. The CHECK that refuses new writes above the cap is
-- a later migration, once old nodes no longer write (CONTRIBUTING.md「发布与缓存」).
--
-- pg.Migrate supplies platform scope on its dedicated connections so this UPDATE sees every supplier's credentials
-- through forced RLS; modules must not set their own tenant scope.
UPDATE suppliers.credentials SET requests_per_minute = 100000 WHERE requests_per_minute > 100000;
