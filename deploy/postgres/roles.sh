#!/bin/sh
# Creates the login roles Axiom connects as, with the passwords from deploy/.env: the application's, and the
# migration administrator, which needs no superuser (internal/platform/README.md「角色」). The image runs this when it
# initializes an empty data directory, and `just up` runs it here on every start, so a database created before a role
# existed gets it without the superuser's password leaving this container. A password is set only when its role is
# created, so it reaches the server once; to change one, use ALTER ROLE.
set -eu
psql --set ON_ERROR_STOP=1 --username postgres --dbname "$POSTGRES_DB" \
	--set app="$AXIOM_APP_PASSWORD" --set migrator="$AXIOM_MIGRATOR_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE axiom_app LOGIN PASSWORD %L', :'app')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'axiom_app') \gexec
SELECT format('CREATE ROLE axiom_migrator LOGIN CREATEROLE PASSWORD %L', :'migrator')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'axiom_migrator') \gexec
GRANT CREATE ON DATABASE :"DBNAME" TO axiom_migrator;
-- A database the superuser migrated has an owner role the migrator did not create, so the migrator lacks the admin
-- option on it, which the migration bootstrap needs to grant the role to itself. This is the grant PostgreSQL 16+
-- gives a role that creates another.
SELECT 'GRANT axiom_owner TO axiom_migrator WITH ADMIN TRUE, INHERIT FALSE, SET FALSE'
WHERE EXISTS (SELECT FROM pg_roles WHERE rolname = 'axiom_owner') \gexec
SQL
