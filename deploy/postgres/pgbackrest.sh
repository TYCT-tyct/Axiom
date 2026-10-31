#!/bin/sh
# The image's pgBackRest command: it exports the repository path derived by wal-path.sh, then runs the packaged binary.
set -eu
. /usr/local/bin/wal-path.sh
PGBACKREST_REPO1_PATH=$(wal_repo_path)
export PGBACKREST_REPO1_PATH
exec /usr/bin/pgbackrest "$@"
