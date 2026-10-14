#!/bin/sh
# Creates the application's login role on a new database, with the password from deploy/.env. The image runs this
# only when it initializes an empty data directory; to change the password later, use ALTER ROLE.
set -eu
psql --set ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --set password="$AXIOM_APP_PASSWORD" <<'SQL'
CREATE ROLE axiom_app LOGIN PASSWORD :'password';
SQL
