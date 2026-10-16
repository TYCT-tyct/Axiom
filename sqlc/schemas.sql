-- Each module's schema is created before its migrations run (internal/platform/README.md「迁移」), so no migration
-- creates it, and sqlc, which reads only the migrations, needs it said here. Add a module's line with its first queries.
CREATE SCHEMA platform;
CREATE SCHEMA catalog;
CREATE SCHEMA observability;
CREATE SCHEMA suppliers;
CREATE SCHEMA billing;
