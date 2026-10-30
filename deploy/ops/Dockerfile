# The backup job's own image (docs/ops/backup-restore.md). It is not the program's image: pg_dump must be at least as
# new as the server it dumps, so it comes from the postgres image the database already runs, and the encryption tools and
# the bucket credentials must never sit inside the image facing the internet. The S3 client is the program's own binary,
# taken from the program image so the backup speaks exactly the client this tree tests.
ARG PROGRAM_IMAGE=axiom:local
FROM ${PROGRAM_IMAGE} AS program

FROM postgres:18.6-alpine

# bash is what the scripts are written in; the program carries its own root certificates (cmd/axiom/main.go).
RUN apk add --no-cache age bash
COPY --from=program /axiom /usr/local/bin/axiom

COPY common.sh backup.sh restore.sh /ops/
RUN chmod 0755 /ops/*.sh

# The backup is what a bare `docker run` of this image should do; deploy/compose.yaml picks the job per service. Root,
# because writing the key ring back has to set its owner.
ENTRYPOINT ["/ops/backup.sh"]
