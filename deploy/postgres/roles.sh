#!/bin/sh
# Creates the login roles Axiom connects as, with the passwords from deploy/.env: the application's, and the
# migration administrator, which needs no superuser (internal/platform/README.md「角色」). The image runs this when it
# initializes an empty data directory, and `just up` runs it here on every start, so a database created before a role
# existed gets it without the superuser's password leaving this container. A password is set only when its role is
# created, so it reaches the server once; to change one, use ALTER ROLE.
set -eu
psql --set ON_ERROR_STOP=1 --username postgres --dbname "$POSTGRES_DB" \
	--set app="$AXIOM_APP_PASSWORD" --set migrator="$AXIOM_MIGRATOR_PASSWORD" <<'SQL'
-- Waiting for a lock counts too, so a session holding one makes `just up` fail with its diagnostics, not hang.
SET statement_timeout = '10s';
SELECT format('CREATE ROLE axiom_app LOGIN PASSWORD %L', :'app')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'axiom_app') \gexec
SELECT format('CREATE ROLE axiom_migrator LOGIN CREATEROLE PASSWORD %L', :'migrator')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'axiom_migrator') \gexec
GRANT CREATE ON DATABASE :"DBNAME" TO axiom_migrator;
-- The migration bootstrap grants the roles it manages (to itself, and axiom_lookup to axiom_owner), which needs the
-- admin option on them. A database migrated by the superuser, or by an earlier migrator that was dropped and created
-- again, has those roles without the current migrator holding that option. This is the grant PostgreSQL 16+ gives a
-- role that creates another.
SELECT format('GRANT %I TO axiom_migrator WITH ADMIN TRUE, INHERIT FALSE, SET FALSE', rolname)
FROM pg_roles WHERE rolname IN ('axiom_owner', 'axiom_lookup') \gexec
SQL
